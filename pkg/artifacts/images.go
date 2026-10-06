// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// RemotePrefix is the reserved path prefix of the files the service fetches
// at publish time. Uploads may not use it.
const RemotePrefix = "_remote/"

// RemotePath returns the manifest path of the remote file fetched from
// sourceURL: _remote/<hex sha256 of the URL>. It is keyed by the URL, not
// the content, so it is known before the fetch succeeds or fails.
func RemotePath(sourceURL string) string {
	sum := sha256.Sum256([]byte(sourceURL))
	return RemotePrefix + hex.EncodeToString(sum[:])
}

// isReservedPath reports whether an upload path uses the reserved remote
// prefix (or is that directory name itself).
func isReservedPath(p string) bool {
	return p == strings.TrimSuffix(RemotePrefix, "/") || strings.HasPrefix(p, RemotePrefix)
}

// Extraction bounds. Every scan below moves forward only and every
// lookahead is either capped or consumed, so the time of one extraction is
// linear in the scanned window, whose size is fixed.
const (
	// maxRefDefinitions bounds the reference definitions remembered from
	// one document.
	maxRefDefinitions = 4096
	// maxLabelBytes bounds an image's alt text or reference label.
	maxLabelBytes = 999
	// maxDestinationBytes bounds an image destination; longer ones are
	// rejected.
	maxDestinationBytes = 2049
	// maxDestinationParens bounds parenthesis nesting in a destination.
	maxDestinationParens = 32
	// ctxCheckEvery is how many bytes are scanned between context checks.
	ctxCheckEvery = 1 << 16
	// maxImageScanBytes bounds the part of a document scanned for images:
	// images are taken from the first 2 MiB.
	maxImageScanBytes = 2 << 20
	// maxAllocPerByte bounds the memory one extraction allocates, in bytes
	// per scanned byte; minAllocBudget is added so small documents are
	// never cut short. Every step that allocates charges an amount at or
	// above what it allocates before it allocates; once the budget is
	// spent, extraction stops and the images found so far are kept.
	maxAllocPerByte = 8
	minAllocBudget  = 64 << 10
	// longRun is the backtick run length above which code spans are paired
	// through a short list instead of a table indexed by run length.
	longRun = 4096
)

// Allocation charges for fixed-size structures, at or above their real
// size.
const (
	extractionCharge = 512 // the extraction's own state and result
	urlParseCharge   = 144 // a parsed URL
	userinfoCharge   = 48  // a parsed URL's user info
	mapBaseCharge    = 512 // a map's first table
	defEntryCharge   = 192 // one entry of the reference definition map
	seenEntryCharge  = 128 // one entry of the found-URL set
)

// allocSize is the charge for allocating n bytes: n rounded up past the
// allocator's size classes (at most a quarter more below 32 KiB, at most
// one 8 KiB page more above).
func allocSize(n int) int {
	if n > 32<<10 {
		return n + 8<<10
	}
	return n + n/4 + 16
}

// imageHit is one candidate image URL and where it first appears.
type imageHit struct {
	pos int
	url string
}

// extractImageURLs returns at most limit absolute http(s) image URLs a
// markdown document references, in order of first appearance and without
// duplicates: inline images, reference-style images resolved through their
// definitions, and <img> tags where the web renderer shows them (inline in
// a paragraph, or a lone <img> forming its own HTML block). Fenced and
// indented code, code spans, and HTML blocks other than a lone <img> are
// skipped, as the renderer shows them as code or text.
//
// Extraction is bounded. Images are taken from the first maxImageScanBytes
// of the document, and every pass is linear in that window. Memory is
// metered: each step that allocates charges its size against
// maxAllocPerByte bytes per scanned byte. When the budget is spent the
// images found so far are kept, and the outcome depends only on the
// document. A quarter of the budget is kept for the image scan, so images
// in the part of the document already read are found even when an earlier
// pass stops. Each destination occurrence is normalized and charged;
// reference definitions are normalized once and stored. It returns nil
// once ctx is done. It is a best-effort scan for the URLs to fetch; the
// renderer matches what it finds against the manifest and shows a
// placeholder for anything missing.
func extractImageURLs(ctx context.Context, markdown string, limit int) []string {
	return extractImages(ctx, markdown, limit, maxAllocPerByte).urls
}

// extractResult is the outcome of one extraction: the URLs found, whether
// the document was longer than the scanned window, whether the allocation
// budget stopped extraction early, and the bytes charged.
type extractResult struct {
	urls          []string
	truncated     bool
	budgetReached bool
	allocUsed     int
}

// extractImages is extractImageURLs with the full result and the
// allocation allowance per scanned byte as a parameter, so tests can
// exercise the budget.
func extractImages(ctx context.Context, markdown string, limit, allocPerByte int) extractResult {
	if limit <= 0 || ctx.Err() != nil {
		return extractResult{}
	}
	var res extractResult
	if len(markdown) > maxImageScanBytes {
		markdown = markdown[:maxImageScanBytes]
		res.truncated = true
	}
	budget := allocPerByte*len(markdown) + minAllocBudget
	e := &extraction{ctx: ctx, limit: limit, allocCap: budget - budget/4}
	// The extraction's state and the visible-text buffer are always
	// needed; they are charged first.
	e.allocUsed = extractionCharge + allocSize(len(markdown))
	visible := e.visibleText(markdown)
	if !e.stopped && !e.cancelled {
		e.referenceDefinitions(visible)
	}
	if e.cancelled {
		return extractResult{}
	}
	// The image scan may use the whole budget, including the reserve.
	e.allocCap, e.stopped = budget, false
	for lineStart := 0; lineStart < len(visible) && !e.done(); {
		lineEnd := strings.IndexByte(visible[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(visible)
		} else {
			lineEnd += lineStart
		}
		line := visible[lineStart:lineEnd]
		if !e.tick(len(line) + 1) {
			break
		}
		hits := e.lineImages(line, lineStart, e.hits[:0])
		hits = e.lineImgTags(line, lineStart, hits)
		e.hits = hits
		slices.SortStableFunc(hits, func(a, b imageHit) int { return a.pos - b.pos })
		for _, h := range hits {
			if len(e.out) == e.limit {
				break
			}
			if len(e.out) == cap(e.out) {
				n := max(16, 2*cap(e.out))
				if !e.spendAlloc(allocSize(16 * n)) {
					break
				}
				e.out = append(make([]string, 0, n), e.out...)
			}
			e.out = append(e.out, h.url)
		}
		lineStart = lineEnd + 1
	}
	if e.cancelled || ctx.Err() != nil {
		return extractResult{}
	}
	res.urls, res.budgetReached, res.allocUsed = e.out, e.budgetReached, e.allocUsed
	return res
}

// extraction is the state of one extractImageURLs call: the allocation
// meter, the URLs found, the reference definitions, and scratch buffers
// reused across lines.
type extraction struct {
	ctx           context.Context
	limit         int
	read          int               // bytes scanned, for context checks
	allocCap      int               // the allocation charge the current pass may reach
	allocUsed     int               // bytes charged for allocations
	stopped       bool              // the current pass reached allocCap
	budgetReached bool              // some pass reached its cap (keeps what was found)
	cancelled     bool              // ctx is done (returns nothing)
	seen          map[string]bool   // URLs already found
	defs          map[string]string // reference label -> normalized URL
	out           []string

	hits      []imageHit // the images found on one line
	labelBuf  []byte     // a folded label
	runStarts []int32    // the backtick runs of one line
	runNext   []int32    // the next run of the same length, or -1
	lastOfLen []int32    // by run length, the nearest later run seen
	longRuns  []int32    // runs longer than longRun
	longLens  []int32    // their lengths
}

// tick records n scanned bytes and checks ctx every ctxCheckEvery bytes. It
// returns false once ctx is done.
func (e *extraction) tick(n int) bool {
	if e.cancelled {
		return false
	}
	before := e.read
	e.read += n
	if e.read/ctxCheckEvery != before/ctxCheckEvery && e.ctx.Err() != nil {
		e.cancelled = true
	}
	return !e.cancelled
}

// spendAlloc charges n bytes about to be allocated. It returns false, and
// the caller does not allocate, once the current pass would exceed its cap
// or ctx is done.
func (e *extraction) spendAlloc(n int) bool {
	if e.stopped || e.cancelled {
		return false
	}
	if e.allocUsed+n > e.allocCap {
		e.stopped, e.budgetReached = true, true
		return false
	}
	e.allocUsed += n
	return true
}

// growScratch makes *buf hold at least n elements, charging a new array.
func (e *extraction) growScratch(buf *[]int32, n int) bool {
	if cap(*buf) >= n {
		*buf = (*buf)[:n]
		return true
	}
	if !e.spendAlloc(allocSize(4 * n)) {
		return false
	}
	*buf = make([]int32, n)
	return true
}

// done reports whether the image scan should stop: the limit is reached,
// the allocation budget is spent, or ctx is done.
func (e *extraction) done() bool {
	return e.stopped || e.cancelled || len(e.seen) >= e.limit
}

// normalize unwraps, unescapes and validates one image destination. It
// returns "" unless the result is an absolute http or https URL. Each copy
// and the URL parse are charged before they are made; a destination equal
// to a URL already found is not parsed again.
func (e *extraction) normalize(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
	if s == "" || len(s) > maxDestinationBytes-1 {
		return ""
	}
	// Only absolute http(s) URLs are kept; checking the scheme first keeps
	// relative destinations free of any further work.
	if !hasPrefixFold(s, "http://") && !hasPrefixFold(s, "https://") {
		return ""
	}
	if strings.IndexByte(s, '\\') >= 0 {
		if !e.spendAlloc(allocSize(len(s))) {
			return ""
		}
		s = unescapeMarkdown(s)
	}
	if strings.IndexByte(s, '&') >= 0 {
		if !e.spendAlloc(2 * allocSize(len(s))) { // a copy and the result
			return ""
		}
		s = html.UnescapeString(s)
	}
	s = strings.TrimSpace(s)
	if len(s) > maxDestinationBytes-1 {
		return ""
	}
	if e.seen[s] {
		return s
	}
	if !e.spendAlloc(urlParseCost(s)) || !isHTTPURL(s) {
		return ""
	}
	return s
}

// urlParseCost is the charge for url.Parse(s): the URL structure, its
// user info when s may hold one, and the unescaped and re-escaped copies of
// its path and fragment when s holds a byte the parser may rewrite.
func urlParseCost(s string) int {
	cost := urlParseCharge
	if strings.IndexByte(s, '@') >= 0 {
		cost += userinfoCharge
	}
	for i := 0; i < len(s); i++ {
		if !urlPlainByte(s[i]) {
			return cost + 8*len(s)
		}
	}
	return cost
}

// urlPlainByte reports whether url.Parse keeps c as it is in every part
// of a URL, so it makes no copy for it.
func urlPlainByte(c byte) bool {
	if isASCIILetter(c) || c >= '0' && c <= '9' {
		return true
	}
	return strings.IndexByte("-._~/:?=&+,;$#@", c) >= 0
}

// isHTTPURL reports whether s parses as a URL with an http or https scheme
// and a host.
func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
}

// addURL appends a found URL (already normalized) to hits unless it was
// found before or is empty.
func (e *extraction) addURL(hits []imageHit, pos int, u string) []imageHit {
	if u == "" || len(e.seen) >= e.limit || e.seen[u] {
		return hits
	}
	charge, n := seenEntryCharge, 0
	if e.seen == nil {
		charge += mapBaseCharge
	}
	if len(hits) == cap(hits) {
		n = max(16, 2*cap(hits))
		charge += allocSize(24 * n) // imageHit is 24 bytes
	}
	if !e.spendAlloc(charge) {
		return hits
	}
	if e.seen == nil {
		e.seen = map[string]bool{}
	}
	if n > 0 {
		hits = append(make([]imageHit, 0, n), hits...)
	}
	e.seen[u] = true
	return append(hits, imageHit{pos, u})
}

// lineImages finds the ![alt](dest), ![alt][ref], ![ref][] and ![ref]
// images on one line. offset is the line's position in the document.
func (e *extraction) lineImages(line string, offset int, hits []imageHit) []imageHit {
	nextClose := newNextIndex(line, "]")
	nextAngle := newNextIndex(line, ">")
	for pos := 0; pos < len(line) && !e.done(); {
		i := strings.Index(line[pos:], "![")
		if i < 0 {
			break
		}
		start := pos + i
		labelStart := start + 2
		labelEnd := nextClose.from(labelStart)
		if labelEnd < 0 {
			break // no ] anywhere after: no further image on this line
		}
		if labelEnd-labelStart > maxLabelBytes {
			pos = labelStart
			continue
		}
		label := line[labelStart:labelEnd]
		after := labelEnd + 1
		switch {
		case after < len(line) && line[after] == '(':
			dest, end := parseDestination(line, after+1, &nextAngle)
			hits = e.addURL(hits, offset+start, e.normalize(dest))
			pos = end
		case after < len(line) && line[after] == '[':
			refStart := after + 1
			refEnd := nextClose.from(refStart)
			if refEnd < 0 || refEnd-refStart > maxLabelBytes {
				pos = refStart
				continue
			}
			ref := line[refStart:refEnd]
			if ref == "" {
				ref = label
			}
			hits = e.addURL(hits, offset+start, e.definition(ref))
			pos = refEnd + 1
		default:
			hits = e.addURL(hits, offset+start, e.definition(label))
			pos = after
		}
	}
	return hits
}

// definition returns the normalized URL a reference label is defined as,
// or "". The lookup itself does not allocate; folding a label that needs
// it uses the reused label buffer.
func (e *extraction) definition(label string) string {
	if len(e.defs) == 0 {
		return ""
	}
	if labelIsPlain(label) {
		return e.defs[label]
	}
	if !e.foldLabel(label) {
		return ""
	}
	return e.defs[string(e.labelBuf)]
}

// definedLabel reports whether a label is already defined: the label
// itself when plain, else its folded form in e.labelBuf.
func (e *extraction) definedLabel(label string, plain bool) bool {
	if plain {
		_, ok := e.defs[label]
		return ok
	}
	_, ok := e.defs[string(e.labelBuf)]
	return ok
}

// foldLabel writes the normalized form of a label that is not plain into
// e.labelBuf, growing it (charged) when needed. The folded form is at
// most three times as long as the label.
func (e *extraction) foldLabel(l string) bool {
	if need := 3 * len(l); cap(e.labelBuf) < need {
		if !e.spendAlloc(allocSize(need)) {
			return false
		}
		e.labelBuf = make([]byte, 0, need)
	}
	e.labelBuf = appendFoldedLabel(e.labelBuf[:0], l)
	return true
}

// parseDestination reads an inline link destination starting at i (just
// after the opening parenthesis): optional spaces, then <...> or a run of
// characters that are neither space nor ')'. It returns the destination,
// capped at maxDestinationBytes, and the position after what it consumed.
func parseDestination(line string, i int, nextAngle *nextIndex) (string, int) {
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i < len(line) && line[i] == '<' {
		if gt := nextAngle.from(i + 1); gt >= 0 && gt-i <= maxDestinationBytes {
			return line[i+1 : gt], gt + 1
		}
	}
	// A bare destination may hold balanced parentheses (nesting capped as
	// CommonMark implementations do) and backslash escapes.
	end, depth := i, 0
	for end < len(line) && end-i < maxDestinationBytes {
		c := line[end]
		if c == ' ' || c == '\t' {
			break
		}
		if c == '\\' && end+1 < len(line) {
			end += 2
			continue
		}
		if c == '(' {
			if depth == maxDestinationParens {
				break
			}
			depth++
		} else if c == ')' {
			if depth == 0 {
				break
			}
			depth--
		}
		end++
	}
	if end-i > maxDestinationBytes {
		end = i + maxDestinationBytes
	}
	if end == i {
		return "", i + 1
	}
	return line[i:end], end
}

// lineImgTags finds the complete <img ...> tags on one line and adds their
// src attributes. A tag runs to the next '>'; an unclosed one ends at the
// next '<', where the scan resumes.
func (e *extraction) lineImgTags(line string, offset int, hits []imageHit) []imageHit {
	nextStop := newNextIndex(line, "<>")
	for pos := 0; pos < len(line) && !e.done(); {
		lt := strings.IndexByte(line[pos:], '<')
		if lt < 0 {
			break
		}
		lt += pos
		if !hasTagName(line[lt+1:], "img") {
			pos = lt + 1
			continue
		}
		stop := nextStop.from(lt + 1)
		if stop < 0 {
			break
		}
		if line[stop] == '<' {
			pos = stop
			continue
		}
		src := rawImgSrc(line[lt : stop+1])
		if strings.IndexByte(src, '&') >= 0 {
			if !e.spendAlloc(2 * allocSize(len(src))) { // decoding: a copy and the result
				break
			}
			src = html.UnescapeString(src)
		}
		hits = e.addURL(hits, offset+lt, e.normalize(src))
		pos = stop + 1
	}
	return hits
}

// hasTagName reports whether s begins with tag name (case-insensitive)
// followed by a character that ends a tag name.
func hasTagName(s, name string) bool {
	if len(s) < len(name) || !strings.EqualFold(s[:len(name)], name) {
		return false
	}
	if len(s) == len(name) {
		return true
	}
	switch s[len(name)] {
	case ' ', '\t', '\n', '\r', '\f', '/', '>':
		return true
	}
	return false
}

// nextIndex answers "the first position at or after i holding one of
// chars" for non-decreasing i. A search starts where the query starts and
// stops at the answer, and the next search starts past it, so each byte is
// scanned at most once in total.
type nextIndex struct {
	s     string
	chars string
	pos   int // cached answer; -1 none left, -2 not computed
}

func newNextIndex(s, chars string) nextIndex {
	return nextIndex{s: s, chars: chars, pos: -2}
}

func (n *nextIndex) from(i int) int {
	if n.pos == -1 || n.pos >= i {
		return n.pos
	}
	if i >= len(n.s) {
		n.pos = -1
		return -1
	}
	j := strings.IndexAny(n.s[i:], n.chars)
	if j < 0 {
		n.pos = -1
	} else {
		n.pos = i + j
	}
	return n.pos
}

// referenceDefinitions collects [label]: destination lines, at most
// maxRefDefinitions, normalizing each destination once and keeping only
// usable (absolute http(s)) ones. Each line is read once.
func (e *extraction) referenceDefinitions(text string) {
	for lineStart := 0; lineStart < len(text) && len(e.defs) < maxRefDefinitions && !e.stopped; {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := text[lineStart:lineEnd]
		lineStart = lineEnd + 1
		if !e.tick(len(line) + 1) {
			return
		}

		i := leadingSpaces(line, 3)
		if i < 0 || i >= len(line) || line[i] != '[' {
			continue
		}
		end := strings.IndexByte(line[i+1:], ']')
		if end < 0 || end > maxLabelBytes || end == 0 {
			continue
		}
		label := line[i+1 : i+1+end]
		j := i + 1 + end + 1
		if j >= len(line) || line[j] != ':' {
			continue
		}
		nextAngle := newNextIndex(line, ">")
		dest, _ := parseDestination(line, j+1, &nextAngle)
		plain := labelIsPlain(label)
		if !plain && !e.foldLabel(label) {
			return
		}
		if e.definedLabel(label, plain) {
			continue
		}
		u := e.normalize(dest)
		if u == "" {
			continue
		}
		key := label
		if !plain {
			if !e.spendAlloc(allocSize(len(e.labelBuf))) { // the key
				return
			}
			key = string(e.labelBuf)
		}
		charge := defEntryCharge
		if e.defs == nil {
			charge += mapBaseCharge
		}
		if !e.spendAlloc(charge) {
			return
		}
		if e.defs == nil {
			e.defs = map[string]string{}
		}
		e.defs[key] = u
	}
}

// imgSrc returns the src attribute of one <img> tag, with character
// references decoded.
func imgSrc(tag string) string {
	return html.UnescapeString(rawImgSrc(tag))
}

// rawImgSrc returns the src attribute of one <img> tag as written, with
// character references not decoded. It does not allocate.
func rawImgSrc(tag string) string {
	t, ok := parseTag(tag)
	if !ok || t.closing || !strings.EqualFold(t.name, "img") {
		return ""
	}
	return t.src
}

// tagInfo is the result of parseTag.
type tagInfo struct {
	name    string
	closing bool
	src     string // as written, character references not decoded
	end     int    // position just past the closing '>'
}

// parseTag parses the HTML tag at the start of s: '<', an optional '/', a
// name, then attributes (name, optional '=' and a quoted or unquoted
// value) up to '>'. It returns the tag's name, its src attribute (as
// written) and where it ends. It reads s once and does not allocate.
func parseTag(s string) (tagInfo, bool) {
	var t tagInfo
	if len(s) < 2 || s[0] != '<' {
		return t, false
	}
	i := 1
	if s[i] == '/' {
		t.closing = true
		i++
	}
	n := i
	for n < len(s) && (isASCIILetter(s[n]) || (n > i && (s[n] >= '0' && s[n] <= '9' || s[n] == '-'))) {
		n++
	}
	if n == i {
		return t, false
	}
	t.name = s[i:n]
	i = n
	for i < len(s) {
		for i < len(s) && (isHTMLSpace(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) {
			return t, false
		}
		if s[i] == '>' {
			t.end = i + 1
			return t, true
		}
		nameStart := i
		for i < len(s) && !isHTMLSpace(s[i]) && s[i] != '=' && s[i] != '>' && s[i] != '/' {
			i++
		}
		attr := s[nameStart:i]
		for i < len(s) && isHTMLSpace(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			continue
		}
		i++
		for i < len(s) && isHTMLSpace(s[i]) {
			i++
		}
		var value string
		if i < len(s) && (s[i] == '"' || s[i] == '\'') {
			q := s[i]
			j := strings.IndexByte(s[i+1:], q)
			if j < 0 {
				return t, false
			}
			value = s[i+1 : i+1+j]
			i += j + 2
		} else {
			vStart := i
			for i < len(s) && !isHTMLSpace(s[i]) && s[i] != '>' {
				i++
			}
			value = s[vStart:i]
		}
		if t.src == "" && strings.EqualFold(attr, "src") {
			t.src = value
		}
	}
	return t, false
}

func isHTMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// leadingSpaces returns the index after up to max leading spaces, or -1 if
// the line is indented further.
func leadingSpaces(line string, max int) int {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	if i > max {
		return -1
	}
	return i
}

// labelIsPlain reports whether a reference label is already in normalized
// form: no uppercase letters, no whitespace other than single spaces
// between words, and ASCII only.
func labelIsPlain(l string) bool {
	if len(l) == 0 || l[0] == ' ' || l[len(l)-1] == ' ' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		if c >= 'A' && c <= 'Z' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r' || c >= 0x80 || (c == ' ' && l[i-1] == ' ') {
			return false
		}
	}
	return true
}

// appendFoldedLabel appends the normalized form of a reference label to
// dst, in one pass: whitespace runs collapse to one space, leading and
// trailing whitespace is dropped, and letters are case-folded. The result
// is at most three times as long as l (an invalid byte becomes U+FFFD, and
// lowercasing grows a rune by at most one byte).
func appendFoldedLabel(dst []byte, l string) []byte {
	start := len(dst)
	pendingSpace := false
	for _, r := range l {
		if unicode.IsSpace(r) {
			pendingSpace = len(dst) > start
			continue
		}
		if pendingSpace {
			dst = append(dst, ' ')
			pendingSpace = false
		}
		dst = utf8.AppendRune(dst, unicode.ToLower(r))
	}
	return dst
}

// unescapeMarkdown removes the backslash in front of each ASCII punctuation
// character, in one pass into one buffer.
func unescapeMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isASCIIPunct reports whether c is ASCII punctuation, the characters a
// backslash escapes in markdown.
func isASCIIPunct(c byte) bool {
	return c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// htmlBlock is an open HTML block: its CommonMark kind (1-7), the end
// marker of kinds 1-5, whether it opened with a lone <img>, and its line
// count. Only the first line is held, until it is known whether the block
// is that one lone <img>; later lines are blanked as they come. It is held
// by value, so opening a block does not allocate.
type htmlBlock struct {
	kind         int
	end          string
	loneImg      bool
	first        string
	firstWritten bool
	lines        int
}

// type1Tags start CommonMark HTML blocks of kind 1, which end at their
// closing tag (type1Ends).
var (
	type1Tags = []string{"script", "pre", "style", "textarea"}
	type1Ends = []string{"</script>", "</pre>", "</style>", "</textarea>"}
)

// type6Tags start CommonMark HTML blocks of kind 6, which end at a blank
// line and may interrupt a paragraph.
var type6Tags = map[string]bool{
	"address": true, "article": true, "aside": true, "base": true, "basefont": true, "blockquote": true,
	"body": true, "caption": true, "center": true, "col": true, "colgroup": true, "dd": true, "details": true,
	"dialog": true, "dir": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "frame": true, "frameset": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "head": true, "header": true, "hr": true, "html": true,
	"iframe": true, "legend": true, "li": true, "link": true, "main": true, "menu": true, "menuitem": true,
	"nav": true, "noframes": true, "ol": true, "optgroup": true, "option": true, "p": true, "param": true,
	"search": true, "section": true, "summary": true, "table": true, "tbody": true, "td": true, "tfoot": true,
	"th": true, "thead": true, "title": true, "tr": true, "track": true, "ul": true,
}

// visibleText returns the document with every part the web renderer shows
// as code or text blanked to spaces, offsets and newlines preserved: fenced
// and indented code, code spans, and HTML blocks (CommonMark kinds 1-7)
// other than a block that is exactly one lone <img> tag. Each line is
// examined a bounded number of times. Only the code-span tables allocate,
// from reused buffers; when they cannot be charged, the rest of the
// document is left out.
func (e *extraction) visibleText(md string) string {
	var b strings.Builder
	b.Grow(len(md))
	var (
		fenceChar     byte
		fenceLen      int
		block         htmlBlock
		inBlock       bool
		prevParagraph bool
		inIndented    bool // inside an indented code block
		inList        bool // inside a list, where indentation is item content
		prevBlank     = true
		stopped       bool // the code-span tables could not be charged
	)
	blank := func(line string) { writeSpaces(&b, len(line)) }
	writeFirst := func() {
		if !block.firstWritten {
			blank(block.first)
			block.firstWritten = true
		}
	}
	finish := func() {
		if !block.firstWritten {
			if block.kind == 7 && block.loneImg && block.lines == 1 {
				b.WriteString(block.first)
			} else {
				blank(block.first)
			}
		}
		inBlock = false
	}
	for lineStart, first := 0, true; lineStart <= len(md); first = false {
		lineEnd := strings.IndexByte(md[lineStart:], '\n')
		last := lineEnd < 0
		if last {
			lineEnd = len(md)
		} else {
			lineEnd += lineStart
		}
		line := md[lineStart:lineEnd]
		if !e.tick(len(line) + 1) {
			break
		}
		isBlank := strings.TrimSpace(line) == ""
		if !first && !inBlock {
			b.WriteByte('\n')
		}

		switch {
		case inBlock:
			if block.kind >= 6 && isBlank {
				finish()
				b.WriteByte('\n')
				b.WriteString(line)
				prevParagraph = false
			} else {
				writeFirst()
				b.WriteByte('\n')
				blank(line)
				block.lines++
				if block.kind < 6 && containsFold(line, block.end) {
					finish()
				}
			}
		case fenceChar != 0:
			if c, n := fenceRun(line); c == fenceChar && n >= fenceLen {
				fenceChar = 0
			}
			blank(line)
		case inIndented && (isBlank || isIndentedCode(line)):
			blank(line)
		default:
			inIndented = false
			if !isBlank && !isIndentedCode(line) {
				// A list goes on through its items and lines that continue
				// them directly; after a blank line, any other unindented
				// line ends it.
				inList = isListItem(line) || (inList && !prevBlank)
			}
			if !isBlank && !prevParagraph && !inList && isIndentedCode(line) {
				inIndented = true
				blank(line)
				break
			}
			if c, n := fenceRun(line); n >= 3 {
				fenceChar, fenceLen = c, n
				blank(line)
				prevParagraph = false
				break
			}
			if blk, ok := startHTMLBlock(line, prevParagraph); ok {
				block, inBlock = blk, true
				block.first, block.lines = line, 1
				if blk.kind < 6 && containsFold(line[strings.IndexByte(line, '<')+1:], blk.end) {
					finish()
				}
				prevParagraph = false
				break
			}
			if !e.writeCodeSpansBlanked(&b, line) {
				stopped = true
				break
			}
			prevParagraph = !isBlank && !isATXHeading(line)
		}
		if stopped {
			break
		}
		prevBlank = isBlank
		if last {
			break
		}
		lineStart = lineEnd + 1
	}
	if inBlock {
		finish()
	}
	return b.String()
}

// fenceRun returns the fence character and run length if line opens or
// closes a code fence (up to three spaces, then ``` or ~~~).
func fenceRun(line string) (byte, int) {
	i := leadingSpaces(line, 3)
	if i < 0 || i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0
	}
	c := line[i]
	n := 0
	for i+n < len(line) && line[i+n] == c {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return c, n
}

// startHTMLBlock reports the HTML block line opens, if any, following the
// CommonMark start conditions. A kind-7 block (a lone complete tag on its
// line) cannot interrupt a paragraph.
func startHTMLBlock(line string, prevParagraph bool) (htmlBlock, bool) {
	i := leadingSpaces(line, 3)
	if i < 0 || i >= len(line) || line[i] != '<' {
		return htmlBlock{}, false
	}
	rest := line[i:]
	for k, t := range type1Tags {
		if hasTagName(rest[1:], t) {
			return htmlBlock{kind: 1, end: type1Ends[k]}, true
		}
	}
	switch {
	case strings.HasPrefix(rest, "<!--"):
		return htmlBlock{kind: 2, end: "-->"}, true
	case strings.HasPrefix(rest, "<?"):
		return htmlBlock{kind: 3, end: "?>"}, true
	case strings.HasPrefix(rest, "<![CDATA["):
		return htmlBlock{kind: 5, end: "]]>"}, true
	case len(rest) > 2 && rest[1] == '!' && isASCIILetter(rest[2]):
		return htmlBlock{kind: 4, end: ">"}, true
	}
	name := rest[1:]
	name = strings.TrimPrefix(name, "/")
	n := 0
	for n < len(name) && (isASCIILetter(name[n]) || (n > 0 && (name[n] >= '0' && name[n] <= '9' || name[n] == '-'))) {
		n++
	}
	if n > 0 && n <= 16 && hasTagName(name, name[:n]) {
		var buf [16]byte
		for k := 0; k < n; k++ {
			c := name[k]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			buf[k] = c
		}
		if type6Tags[string(buf[:n])] {
			return htmlBlock{kind: 6}, true
		}
	}
	if prevParagraph {
		return htmlBlock{}, false
	}
	if lone, isImg := loneTag(strings.TrimRight(rest, " \t\r")); lone {
		return htmlBlock{kind: 7, loneImg: isImg}, true
	}
	return htmlBlock{}, false
}

// loneTag reports whether s is exactly one complete open or closing tag,
// and whether that tag is an opening <img>.
func loneTag(s string) (lone, isImg bool) {
	if !strings.HasSuffix(s, ">") {
		return false, false
	}
	t, ok := parseTag(s)
	if !ok || t.end != len(s) {
		return false, false
	}
	for _, name := range type1Tags {
		if strings.EqualFold(t.name, name) {
			return false, false
		}
	}
	return true, !t.closing && strings.EqualFold(t.name, "img")
}

// writeCodeSpansBlanked writes one line to b with its code spans blanked:
// a run of backticks up to the next run of the same length. Runs are
// paired through a "next run of this length" table built backwards, so the
// line is processed in linear time. The tables are reused across lines and
// grown (charged) only when a line needs more; runs longer than longRun
// are paired through a separate list, which holds at most one entry per
// longRun bytes. It returns false, writing nothing, when a table cannot be
// charged.
func (e *extraction) writeCodeSpansBlanked(b *strings.Builder, line string) bool {
	if strings.IndexByte(line, '`') < 0 {
		b.WriteString(line)
		return true
	}
	count, long, maxShort := 0, 0, 0
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		count++
		if n := j - i; n > longRun {
			long++
		} else if n > maxShort {
			maxShort = n
		}
		i = j
	}
	if !e.growScratch(&e.runStarts, count) || !e.growScratch(&e.runNext, count) ||
		!e.growScratch(&e.lastOfLen, maxShort+1) || !e.growScratch(&e.longRuns, long) ||
		!e.growScratch(&e.longLens, long) {
		return false
	}
	starts, next, last := e.runStarts, e.runNext, e.lastOfLen
	longRuns, longLens := e.longRuns[:0], e.longLens[:0]
	runLen := func(k int) int {
		j := int(starts[k])
		for j < len(line) && line[j] == '`' {
			j++
		}
		return j - int(starts[k])
	}
	k := 0
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		starts[k] = int32(i)
		if j-i > longRun {
			longRuns = append(longRuns, int32(k))
			longLens = append(longLens, int32(j-i))
		}
		k++
		i = j
	}
	for n := range last {
		last[n] = -1
	}
	for k := count - 1; k >= 0; k-- {
		next[k] = -1
		if n := runLen(k); n <= longRun {
			next[k] = last[n]
			last[n] = int32(k)
		}
	}
	for x := range longRuns {
		for y := x + 1; y < len(longRuns); y++ {
			if longLens[y] == longLens[x] {
				next[longRuns[x]] = longRuns[y]
				break
			}
		}
	}
	pos := 0
	for k := 0; k < count; {
		j := next[k]
		if j < 0 {
			k++
			continue
		}
		start, end := int(starts[k]), int(starts[j])+runLen(int(j))
		b.WriteString(line[pos:start])
		writeSpaces(b, end-start)
		pos = end
		k = int(j) + 1
	}
	b.WriteString(line[pos:])
	return true
}

// isIndentedCode reports whether line is indented enough (four spaces or a
// tab) to be indented code when it does not continue a paragraph.
func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
}

// isListItem reports whether line starts a list item ("- ", "* ", "+ " or
// "1. " style, up to three spaces in).
func isListItem(line string) bool {
	i := leadingSpaces(line, 3)
	if i < 0 || i >= len(line) {
		return false
	}
	if c := line[i]; c == '-' || c == '*' || c == '+' {
		return i+1 == len(line) || line[i+1] == ' ' || line[i+1] == '\t'
	}
	j := i
	for j < len(line) && j-i < 9 && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	return j > i && j < len(line) && (line[j] == '.' || line[j] == ')') && (j+1 == len(line) || line[j+1] == ' ' || line[j+1] == '\t')
}

func isATXHeading(line string) bool {
	i := leadingSpaces(line, 3)
	if i < 0 {
		return false
	}
	n := 0
	for i+n < len(line) && line[i+n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && (i+n == len(line) || line[i+n] == ' ' || line[i+n] == '\t')
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// containsFold reports whether s contains substr, ignoring ASCII case. It
// checks each position where substr's first byte (in either case) occurs.
func containsFold(s, substr string) bool {
	if substr == "" {
		return true
	}
	first := substr[0]
	alt := first
	if isASCIILetter(first) {
		alt = first ^ 0x20
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if (s[i] == first || s[i] == alt) && strings.EqualFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

const spaces64 = "                                                                "

// writeSpaces writes n spaces to b without allocating.
func writeSpaces(b *strings.Builder, n int) {
	for n > len(spaces64) {
		b.WriteString(spaces64)
		n -= len(spaces64)
	}
	b.WriteString(spaces64[:n])
}
