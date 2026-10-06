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
	// ctxCheckEvery is how many scan steps run between context checks.
	ctxCheckEvery = 1024
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
// a paragraph, or a lone <img> forming its own HTML block). Fenced code,
// code spans, and HTML blocks other than a lone <img> are skipped, as the
// renderer shows them as text.
//
// The parse is linear in the document: lines are processed once, scans
// move forward only, lookaheads are capped (labels, destinations) or
// consumed, and the "next ]" and "next >" positions are cached so they are
// never searched twice. It stops at limit URLs, and returns nil once ctx is
// done. It is a best-effort scan for the URLs to fetch; the renderer
// matches what it finds against the manifest and shows a placeholder for
// anything missing.
func extractImageURLs(ctx context.Context, markdown string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	visible, ok := visibleText(ctx, markdown)
	if !ok {
		return nil
	}
	defs, ok := referenceDefinitions(ctx, visible)
	if !ok {
		return nil
	}

	var out []string
	seen := map[string]bool{}
	steps := 0
	for lineStart := 0; lineStart < len(visible); {
		lineEnd := strings.IndexByte(visible[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(visible)
		} else {
			lineEnd += lineStart
		}
		if ctx.Err() != nil {
			return nil
		}
		line := visible[lineStart:lineEnd]
		hits, ok := lineImages(ctx, line, lineStart, defs, &steps)
		if !ok {
			return nil
		}
		hits = append(hits, lineImgTags(line, lineStart)...)
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
		for _, h := range hits {
			u := normalizeDestination(h.url)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
			if len(out) == limit {
				return out
			}
		}
		lineStart = lineEnd + 1
	}
	return out
}

// lineImages finds the ![alt](dest), ![alt][ref], ![ref][] and ![ref]
// images on one line. offset is the line's position in the document.
func lineImages(ctx context.Context, line string, offset int, defs map[string]string, steps *int) ([]imageHit, bool) {
	var hits []imageHit
	nextClose := newNextIndex(line, "]")
	nextAngle := newNextIndex(line, ">")
	for pos := 0; pos < len(line); {
		*steps++
		if *steps%ctxCheckEvery == 0 && ctx.Err() != nil {
			return nil, false
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
			if dest != "" {
				hits = append(hits, imageHit{offset + start, dest})
			}
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
			if dest, ok := defs[normalizeLabel(ref)]; ok {
				hits = append(hits, imageHit{offset + start, dest})
			}
			pos = refEnd + 1
		default:
			if dest, ok := defs[normalizeLabel(label)]; ok {
				hits = append(hits, imageHit{offset + start, dest})
			}
			pos = after
		}
	}
	return hits, true
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
	end := i
	for end < len(line) && end-i < maxDestinationBytes {
		c := line[end]
		if c == ' ' || c == '\t' || c == ')' {
			break
		}
		end++
	}
	if end == i {
		return "", i + 1
	}
	return line[i:end], end
}

// lineImgTags finds the complete <img ...> tags on one line and returns
// their src attributes. A tag runs to the next '>'; an unclosed one ends
// at the next '<', where the scan resumes.
func lineImgTags(line string, offset int) []imageHit {
	var hits []imageHit
	nextStop := newNextIndex(line, "<>")
	for pos := 0; pos < len(line); {
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
		if src := imgSrc(line[lt : stop+1]); src != "" {
			hits = append(hits, imageHit{offset + lt, src})
		}
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
// maxRefDefinitions. Each line is read once.
func referenceDefinitions(ctx context.Context, text string) (map[string]string, bool) {
	defs := map[string]string{}
	for lineStart := 0; lineStart < len(text) && len(defs) < maxRefDefinitions; {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		if ctx.Err() != nil {
			return nil, false
		}
		line := text[lineStart:lineEnd]
		lineStart = lineEnd + 1

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
		j++
		dest, _ := parseDestination(line, j, newNextIndex(line, ">"))
		if dest == "" {
			continue
		}
		if key := normalizeLabel(label); defs[key] == "" {
			defs[key] = dest
		}
	}
	return defs, true
}

// imgSrc returns the src attribute of one <img> tag.
func imgSrc(tag string) string {
	z := html.NewTokenizer(strings.NewReader(tag))
	if tt := z.Next(); tt != html.StartTagToken && tt != html.SelfClosingTagToken {
		return ""
	}
	for {
		k, v, more := z.TagAttr()
		if string(k) == "src" {
			return string(v)
		}
		if !more {
			return ""
		}
	}
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
	return strings.ToLower(strings.Join(strings.Fields(l), " "))
}

// normalizeDestination unwraps, unescapes and validates one image
// destination. It returns "" unless the result is an absolute http or https
// URL.
func normalizeDestination(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
	s = mdEscape.ReplaceAllString(s, "$1")
	s = strings.TrimSpace(html.UnescapeString(s))
	if s == "" || len(s) > 2048 {
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

// htmlBlock is an open HTML block: its CommonMark kind (1-7), the end
// marker of kinds 1-5, and its lines so far.
type htmlBlock struct {
	kind    int
	end     string
	lines   []string
	loneImg bool
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
// code, code spans, and HTML blocks (CommonMark kinds 1-7) other than a
// block that is exactly one lone <img> tag. Each line is examined once;
// the result is false once ctx is done.
func visibleText(ctx context.Context, md string) (string, bool) {
	var b strings.Builder
	b.Grow(len(md))
	var (
		fenceChar     byte
		fenceLen      int
		block         *htmlBlock
		prevParagraph bool
	)
	blank := func(line string) {
		b.WriteString(strings.Repeat(" ", len(line)))
	}
	flush := func() {
		if block.kind == 7 && block.loneImg && len(block.lines) == 1 {
			b.WriteString(block.lines[0])
		} else {
			for i, l := range block.lines {
				if i > 0 {
					b.WriteByte('\n')
				}
				blank(l)
			}
		}
		block = nil
	}
	for lineStart, first := 0, true; lineStart <= len(md); first = false {
		if ctx.Err() != nil {
			return "", false
		}
		lineEnd := strings.IndexByte(md[lineStart:], '\n')
		last := lineEnd < 0
		if last {
			lineEnd = len(md)
		} else {
			lineEnd += lineStart
		}
		line := md[lineStart:lineEnd]
		isBlank := strings.TrimSpace(line) == ""
		if !first && block == nil {
			b.WriteByte('\n')
		}

		switch {
		case block != nil:
			if block.kind >= 6 && isBlank {
				flush()
				b.WriteByte('\n')
				b.WriteString(line)
				prevParagraph = false
			} else {
				block.lines = append(block.lines, line)
				if block.kind < 6 && containsFold(line, block.end) {
					flush()
				}
			}
		case fenceChar != 0:
			if c, n := fenceRun(line); c == fenceChar && n >= fenceLen {
				fenceChar = 0
			}
			blank(line)
		default:
			if c, n := fenceRun(line); n >= 3 {
				fenceChar, fenceLen = c, n
				blank(line)
				prevParagraph = false
				break
			}
			if blk := startHTMLBlock(line, prevParagraph); blk != nil {
				block = blk
				block.lines = []string{line}
				if blk.kind < 6 && containsFold(line[strings.IndexByte(line, '<')+1:], blk.end) {
					flush()
				}
				prevParagraph = false
				break
			}
			b.WriteString(blankCodeSpans(line))
			prevParagraph = !isBlank && !isATXHeading(line)
		}
		if last {
			break
		}
		lineStart = lineEnd + 1
	}
	if block != nil {
		flush()
	}
	return b.String(), true
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
	if n > 0 && type6Tags[strings.ToLower(name[:n])] && hasTagName(name, name[:n]) {
		return &htmlBlock{kind: 6}
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
	z := html.NewTokenizer(strings.NewReader(s))
	tt := z.Next()
	if tt != html.StartTagToken && tt != html.SelfClosingTagToken && tt != html.EndTagToken {
		return false, false
	}
	if len(z.Raw()) != len(s) {
		return false, false
	}
	name, _ := z.TagName()
	for _, t := range type1Tags {
		if string(name) == t {
			return false, false
		}
	}
	return true, tt != html.EndTagToken && string(name) == "img"
}

// blankCodeSpans blanks the code spans of one line: a run of backticks up
// to the next run of the same length. Runs are paired with a precomputed
// "next run of this length" table, so the line is processed in linear time.
func blankCodeSpans(line string) string {
	if strings.IndexByte(line, '`') < 0 {
		return line
	}
	type run struct{ start, n int }
	var runs []run
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		runs = append(runs, run{i, j - i})
		i = j
	}
	next := make([]int, len(runs))
	lastOfLen := map[int]int{}
	for k := len(runs) - 1; k >= 0; k-- {
		if j, ok := lastOfLen[runs[k].n]; ok {
			next[k] = j
		} else {
			next[k] = -1
		}
		lastOfLen[runs[k].n] = k
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
		k = j + 1
	}
	return string(out)
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

// containsFold reports whether s contains substr, ignoring ASCII case.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
