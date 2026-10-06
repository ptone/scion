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

var (
	fenceLine     = regexp.MustCompile("^ {0,3}(\x60{3,}|~{3,})")
	codeSpan      = regexp.MustCompile("(\x60+)[^\x60]*?\x60+")
	refDefinition = regexp.MustCompile(`(?m)^ {0,3}\[([^\]]+)\]:[ \t]*(<[^>\n]*>|\S+)`)
	inlineImage   = regexp.MustCompile(`!\[[^\]]*\]\([ \t]*(<[^>\n]*>|[^\s)]+)`)
	refImage      = regexp.MustCompile(`!\[([^\]]*)\](?:\[([^\]]*)\])?`)
	imgTag        = regexp.MustCompile(`(?i)<img\b[^<>]*>`)
	htmlBlockLine = regexp.MustCompile(`^ {0,3}</?([A-Za-z][A-Za-z0-9-]*)`)
	mdEscape      = regexp.MustCompile(`\\([!-/:-@\[-\x60{-~])`)
)

// maxRefDefinitions bounds the reference definitions remembered from one
// document.
const maxRefDefinitions = 4096

// imageHit is one candidate image URL and where it first appears.
type imageHit struct {
	pos int
	url string
}

// extractImageURLs returns at most limit absolute http(s) image URLs a
// markdown document references, in order of first appearance and without
// duplicates: inline images, reference-style images resolved through their
// definitions, and <img> tags where the web renderer shows them (inline in
// text or alone on a line, not inside an HTML block). Code blocks and code
// spans are skipped.
//
// The work is linear in the document and bounded by limit: each kind of
// reference is scanned once and stops after limit distinct URLs or past the
// position where the result is already complete, so the result holds the
// first limit URLs of the document. It is a best-effort
// scan for the URLs to fetch; the renderer matches what it finds against
// the manifest and shows a placeholder for anything missing.
func extractImageURLs(markdown string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	text := stripCode(markdown)

	defs := map[string]string{}
	scanMatches(refDefinition, text, func(m []int) bool {
		label := normalizeLabel(text[m[2]:m[3]])
		if _, seen := defs[label]; !seen {
			defs[label] = text[m[4]:m[5]]
		}
		return len(defs) < maxRefDefinitions
	})

	var hits []imageHit
	// cutoff is the position of the limit-th distinct URL found so far; a
	// later scan stops there, because nothing after it can make the result.
	cutoff := len(text)
	updateCutoff := func() {
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
		seen := map[string]bool{}
		for _, h := range hits {
			if !seen[h.url] {
				seen[h.url] = true
				if len(seen) == limit {
					cutoff = h.pos
					return
				}
			}
		}
	}
	collectIn := func(src string, re *regexp.Regexp, dest func(m []int) string) {
		seen := map[string]bool{}
		scanMatches(re, src, func(m []int) bool {
			if m[0] > cutoff {
				return false
			}
			if u := normalizeDestination(dest(m)); u != "" && !seen[u] {
				seen[u] = true
				hits = append(hits, imageHit{m[0], u})
			}
			return len(seen) < limit
		})
		updateCutoff()
	}
	collect := func(re *regexp.Regexp, dest func(m []int) string) { collectIn(text, re, dest) }
	collect(inlineImage, func(m []int) string { return text[m[2]:m[3]] })
	collect(refImage, func(m []int) string {
		if m[1] < len(text) && text[m[1]] == '(' {
			return "" // an inline image, handled above
		}
		label := text[m[2]:m[3]]
		if m[4] >= 0 && m[5] > m[4] {
			label = text[m[4]:m[5]]
		}
		return defs[normalizeLabel(label)]
	})
	shown := blankHTMLBlocks(text)
	collectIn(shown, imgTag, func(m []int) string { return imgSrc(shown[m[0]:m[1]]) })

	var out []string
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h.url] {
			continue
		}
		seen[h.url] = true
		out = append(out, h.url)
		if len(out) == limit {
			break
		}
	}
	return out
}

// scanMatches calls fn for each successive match of re in text, left to
// right, until fn returns false. Each call searches only the rest of the
// text, so a full scan is linear.
func scanMatches(re *regexp.Regexp, text string, fn func(m []int) bool) {
	for off := 0; off < len(text); {
		m := re.FindStringSubmatchIndex(text[off:])
		if m == nil {
			return
		}
		for i := range m {
			if m[i] >= 0 {
				m[i] += off
			}
		}
		if !fn(m) {
			return
		}
		next := m[1]
		if next == m[0] {
			next++
		}
		off = next
	}
}

// blankHTMLBlocks returns text with every HTML block blanked to spaces, so
// only <img> tags the renderer would show remain: a block starts at a line
// that begins with a tag other than <img> and runs to the next blank line,
// and the renderer shows such a block as text. Offsets are preserved.
func blankHTMLBlocks(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inBlock := false
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			inBlock = false
		} else if !inBlock {
			if m := htmlBlockLine.FindStringSubmatch(line); m != nil && !strings.EqualFold(m[1], "img") {
				inBlock = true
			}
		}
		if inBlock {
			b.WriteString(strings.Map(func(r rune) rune {
				if r == '\n' {
					return r
				}
				return ' '
			}, line))
			continue
		}
		b.WriteString(line)
	}
	return b.String()
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

// stripCode blanks fenced code blocks and code spans so images inside them
// are not fetched.
func stripCode(md string) string {
	var b strings.Builder
	inFence := ""
	for _, line := range strings.SplitAfter(md, "\n") {
		if m := fenceLine.FindStringSubmatch(line); m != nil {
			marker := m[1][:3]
			if inFence == "" {
				inFence = marker
				b.WriteString("\n")
				continue
			}
			if strings.HasPrefix(m[1], inFence) {
				inFence = ""
				b.WriteString("\n")
				continue
			}
		}
		if inFence != "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(codeSpan.ReplaceAllStringFunc(line, func(s string) string { return strings.Repeat(" ", len(s)) }))
	}
	return b.String()
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
