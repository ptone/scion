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
	"regexp"
	"sort"
	"strings"

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

var mdEscape = regexp.MustCompile(`\\([!-/:-@\[-\x60{-~])`)

// Extraction bounds. Every scan below moves forward only and every
// lookahead is either capped or consumed, so extraction is linear in the
// document.
const (
	// maxRefDefinitions bounds the reference definitions remembered from
	// one document.
	maxRefDefinitions = 4096
	// maxLabelBytes bounds an image's alt text or reference label.
	maxLabelBytes = 999
	// maxDestinationBytes bounds an image destination; normalizeDestination
	// rejects anything longer.
	maxDestinationBytes = 2049
	// maxDestinationParens bounds parenthesis nesting in a destination.
	maxDestinationParens = 32
	// ctxCheckEvery is how many units of work run between context checks.
	ctxCheckEvery = 1 << 16
	// maxImageScanBytes bounds the part of a document scanned for images:
	// images are taken from the first 8 MiB.
	maxImageScanBytes = 8 << 20
	// maxWorkPerByte bounds the work of one extraction, in units per
	// scanned byte; minWorkBudget is added so small documents are never
	// cut short. Images found before the bound are kept.
	maxWorkPerByte = 32
	minWorkBudget  = 1 << 16
)

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
// Scanning is bounded: images are taken from the first maxImageScanBytes of
// the document, and the work is metered (bytes examined, plus the bytes of
// each destination normalized) against maxWorkPerByte units per scanned
// byte. When the meter runs out, the images found before the bound are
// kept; the outcome depends only on the document. Within the bound the
// parse moves forward only, caps its lookaheads, and normalizes each
// distinct destination and reference definition once. It returns nil once
// ctx is done. It is a best-effort scan for the URLs to fetch; the renderer
// matches what it finds against the manifest and shows a placeholder for
// anything missing.
func extractImageURLs(ctx context.Context, markdown string, limit int) []string {
	return extractImageURLsWithBudget(ctx, markdown, limit, maxWorkPerByte)
}

// extractImageURLsWithBudget is extractImageURLs with the work allowance
// per scanned byte as a parameter, so tests can exercise the bound.
func extractImageURLsWithBudget(ctx context.Context, markdown string, limit, perByte int) []string {
	if limit <= 0 || ctx.Err() != nil {
		return nil
	}
	if len(markdown) > maxImageScanBytes {
		markdown = markdown[:maxImageScanBytes]
	}
	e := &extraction{
		ctx:      ctx,
		limit:    limit,
		budget:   perByte*len(markdown) + minWorkBudget,
		seen:     map[string]bool{},
		resolved: map[string]string{},
	}
	visible := e.visibleText(markdown)
	if e.cancelled {
		return nil
	}
	if !e.budgetReached {
		e.referenceDefinitions(visible)
	}
	for lineStart := 0; lineStart < len(visible) && !e.done(); {
		lineEnd := strings.IndexByte(visible[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(visible)
		} else {
			lineEnd += lineStart
		}
		line := visible[lineStart:lineEnd]
		if !e.spend(len(line) + 1) {
			break
		}
		hits := e.lineImages(line, lineStart, nil)
		hits = e.lineImgTags(line, lineStart, hits)
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
		for _, h := range hits {
			if len(e.out) == e.limit {
				break
			}
			e.out = append(e.out, h.url)
		}
		lineStart = lineEnd + 1
	}
	if e.cancelled || ctx.Err() != nil {
		return nil
	}
	return e.out
}

// extraction is the state of one extractImageURLs call: the work meter,
// the URLs found, and the per-document caches that keep each destination
// and definition from being normalized twice.
type extraction struct {
	ctx           context.Context
	limit         int
	budget        int
	used          int
	budgetReached bool              // the work meter ran out (keeps what was found)
	cancelled     bool              // ctx is done (returns nothing)
	seen          map[string]bool   // URLs already found
	resolved      map[string]string // raw destination -> normalized URL or ""
	defs          map[string]string // reference label -> normalized URL
	out           []string
}

// spend charges n units of work. It returns false once the meter has run
// out or ctx is done; the context is checked every ctxCheckEvery units.
func (e *extraction) spend(n int) bool {
	if e.budgetReached || e.cancelled {
		return false
	}
	before := e.used
	e.used += n
	if e.used/ctxCheckEvery != before/ctxCheckEvery && e.ctx.Err() != nil {
		e.cancelled = true
		return false
	}
	if e.used > e.budget {
		e.budgetReached = true
		return false
	}
	return true
}

// done reports whether extraction should stop: the limit is reached, the
// meter ran out, or ctx is done.
func (e *extraction) done() bool {
	return e.budgetReached || e.cancelled || len(e.seen) >= e.limit
}

// normalize returns the normalized URL for a raw destination, computing it
// once per distinct destination.
func (e *extraction) normalize(raw string) string {
	if u, ok := e.resolved[raw]; ok {
		return u
	}
	if !e.spend(len(raw)) {
		return ""
	}
	u := normalizeDestination(raw)
	e.resolved[raw] = u
	return u
}

// addURL appends a found URL (already normalized) to hits unless it was
// found before or is empty.
func (e *extraction) addURL(hits []imageHit, pos int, u string) []imageHit {
	if u == "" || e.seen[u] || len(e.seen) >= e.limit {
		return hits
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
		if !e.spend(1) {
			break
		}
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
			dest, end := parseDestination(line, after+1, nextAngle)
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
// or "".
func (e *extraction) definition(label string) string {
	if len(e.defs) == 0 || !e.spend(len(label)) {
		return ""
	}
	return e.defs[normalizeLabel(label)]
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
		if !e.spend(1) {
			break
		}
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
		if !e.spend(stop + 1 - lt) {
			break
		}
		hits = e.addURL(hits, offset+lt, e.normalize(imgSrc(line[lt:stop+1])))
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

func newNextIndex(s, chars string) *nextIndex {
	return &nextIndex{s: s, chars: chars, pos: -2}
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
	e.defs = map[string]string{}
	for lineStart := 0; lineStart < len(text) && len(e.defs) < maxRefDefinitions; {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := text[lineStart:lineEnd]
		lineStart = lineEnd + 1
		if !e.spend(len(line) + 1) {
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
		dest, _ := parseDestination(line, j+1, newNextIndex(line, ">"))
		key := normalizeLabel(label)
		if _, defined := e.defs[key]; defined {
			continue
		}
		if u := e.normalize(dest); u != "" {
			e.defs[key] = u
		}
	}
}

// imgSrc returns the src attribute of one <img> tag, with character
// references decoded.
func imgSrc(tag string) string {
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
	src     string
	end     int // position just past the closing '>'
}

// parseTag parses the HTML tag at the start of s: '<', an optional '/', a
// name, then attributes (name, optional '=' and a quoted or unquoted
// value) up to '>'. It returns the tag's name, its src attribute (decoded)
// and where it ends. It reads s once and allocates only to decode a src
// value that holds character references.
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
			t.src = html.UnescapeString(value)
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
func normalizeLabel(l string) string {
	// Fast path: a label with no uppercase letters and single spaces only
	// is already normalized and needs no copy.
	plain := len(l) > 0 && l[0] != ' ' && l[len(l)-1] != ' '
	for i := 0; plain && i < len(l); i++ {
		c := l[i]
		if c >= 'A' && c <= 'Z' || c == '\t' || c == '\n' || c == '\r' || (c == ' ' && l[i-1] == ' ') {
			plain = false
		}
	}
	if plain {
		return l
	}
	return strings.ToLower(strings.Join(strings.Fields(l), " "))
}

// normalizeDestination unwraps, unescapes and validates one image
// destination. It returns "" unless the result is an absolute http or https
// URL.
func normalizeDestination(raw string) string {
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
		s = mdEscape.ReplaceAllString(s, "$1")
	}
	s = strings.TrimSpace(html.UnescapeString(s))
	if len(s) > maxDestinationBytes-1 {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return s
	}
	return ""
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// htmlBlock is an open HTML block: its CommonMark kind (1-7), the end
// marker of kinds 1-5, whether it opened with a lone <img>, and its line
// count. Only the first line is held, until it is known whether the block
// is that one lone <img>; later lines are blanked as they come.
type htmlBlock struct {
	kind         int
	end          string
	loneImg      bool
	first        string
	firstWritten bool
	lines        int
}

// type1Tags start CommonMark HTML blocks of kind 1, which end at their
// closing tag.
var type1Tags = []string{"script", "pre", "style", "textarea"}

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
// examined once and charged to the work meter; when the meter runs out the
// rest of the document is left out.
func (e *extraction) visibleText(md string) string {
	var b strings.Builder
	b.Grow(len(md))
	var (
		fenceChar     byte
		fenceLen      int
		block         *htmlBlock
		prevParagraph bool
		inIndented    bool // inside an indented code block
		inList        bool // inside a list, where indentation is item content
		prevBlank     = true
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
		block = nil
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
		// Each line is read a bounded number of times below.
		if !e.spend(4*len(line) + 1) {
			break
		}
		isBlank := strings.TrimSpace(line) == ""
		if !first && block == nil {
			b.WriteByte('\n')
		}

		switch {
		case block != nil:
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
			if blk := startHTMLBlock(line, prevParagraph); blk != nil {
				block = blk
				block.first, block.lines = line, 1
				if blk.kind < 6 && containsFold(line[strings.IndexByte(line, '<')+1:], blk.end) {
					finish()
				}
				prevParagraph = false
				break
			}
			b.WriteString(blankCodeSpans(line))
			prevParagraph = !isBlank && !isATXHeading(line)
		}
		prevBlank = isBlank
		if last {
			break
		}
		lineStart = lineEnd + 1
	}
	if block != nil {
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
func startHTMLBlock(line string, prevParagraph bool) *htmlBlock {
	i := leadingSpaces(line, 3)
	if i < 0 || i >= len(line) || line[i] != '<' {
		return nil
	}
	rest := line[i:]
	for _, t := range type1Tags {
		if hasTagName(rest[1:], t) {
			return &htmlBlock{kind: 1, end: "</" + t + ">"}
		}
	}
	switch {
	case strings.HasPrefix(rest, "<!--"):
		return &htmlBlock{kind: 2, end: "-->"}
	case strings.HasPrefix(rest, "<?"):
		return &htmlBlock{kind: 3, end: "?>"}
	case strings.HasPrefix(rest, "<![CDATA["):
		return &htmlBlock{kind: 5, end: "]]>"}
	case len(rest) > 2 && rest[1] == '!' && isASCIILetter(rest[2]):
		return &htmlBlock{kind: 4, end: ">"}
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
			return &htmlBlock{kind: 6}
		}
	}
	if prevParagraph {
		return nil
	}
	if lone, isImg := loneTag(strings.TrimRight(rest, " \t\r")); lone {
		return &htmlBlock{kind: 7, loneImg: isImg}
	}
	return nil
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

// blankCodeSpans blanks the code spans of one line: a run of backticks up
// to the next run of the same length. Runs are paired with a precomputed
// "next run of this length" table, so the line is processed in linear time;
// the tables are sized exactly, after counting the runs.
func blankCodeSpans(line string) string {
	if strings.IndexByte(line, '`') < 0 {
		return line
	}
	count := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '`' && (i == 0 || line[i-1] != '`') {
			count++
		}
	}
	type run struct{ start, n int32 }
	runs := make([]run, 0, count)
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		runs = append(runs, run{int32(i), int32(j - i)})
		i = j
	}
	next := make([]int32, len(runs))
	lastOfLen := map[int32]int32{}
	for k := len(runs) - 1; k >= 0; k-- {
		if j, ok := lastOfLen[runs[k].n]; ok {
			next[k] = j
		} else {
			next[k] = -1
		}
		lastOfLen[runs[k].n] = int32(k)
	}
	out := []byte(line)
	for k := 0; k < len(runs); {
		j := next[k]
		if j < 0 {
			k++
			continue
		}
		for p := runs[k].start; p < runs[j].start+runs[j].n; p++ {
			out[p] = ' '
		}
		k = int(j) + 1
	}
	return string(out)
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
