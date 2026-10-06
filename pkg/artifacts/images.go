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
	mdEscape      = regexp.MustCompile(`\\([!-/:-@\[-\x60{-~])`)
)

// extractImageURLs returns the absolute http(s) image URLs a markdown
// document references, in order of first appearance and without
// duplicates: inline images, reference-style images resolved through their
// definitions, and the src of inline <img> HTML. Code blocks and code spans
// are skipped. It is a best-effort scan for the URLs to fetch; the renderer
// matches what it finds against the manifest and shows a placeholder for
// anything missing.
func extractImageURLs(markdown string) []string {
	text := stripCode(markdown)

	defs := map[string]string{}
	for _, m := range refDefinition.FindAllStringSubmatch(text, -1) {
		label := normalizeLabel(m[1])
		if _, seen := defs[label]; !seen {
			defs[label] = m[2]
		}
	}

	type hit struct {
		pos int
		raw string
	}
	var hits []hit
	for _, m := range inlineImage.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[0], text[m[2]:m[3]]})
	}
	for _, m := range refImage.FindAllStringSubmatchIndex(text, -1) {
		end := m[1]
		if end < len(text) && text[end] == '(' {
			continue // an inline image, handled above
		}
		label := text[m[2]:m[3]]
		if m[4] >= 0 && m[5] > m[4] {
			label = text[m[4]:m[5]]
		}
		if dest, ok := defs[normalizeLabel(label)]; ok {
			hits = append(hits, hit{m[0], dest})
		}
	}
	z := html.NewTokenizer(strings.NewReader(text))
	offset := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		raw := z.Raw()
		if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
			if name, hasAttr := z.TagName(); string(name) == "img" && hasAttr {
				for {
					k, v, more := z.TagAttr()
					if string(k) == "src" {
						hits = append(hits, hit{offset, string(v)})
					}
					if !more {
						break
					}
				}
			}
		}
		offset += len(raw)
	}

	// Order by position so the result follows the document.
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].pos < hits[j-1].pos; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, h := range hits {
		u := normalizeDestination(h.raw)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
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
