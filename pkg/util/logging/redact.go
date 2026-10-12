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

package logging

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// redactedValue replaces the value of a credential-bearing query parameter.
const redactedValue = "REDACTED"

// credentialQueryParams are query parameters whose values are bearer
// credentials and must never be written to logs. "sig" carries the Hub's
// skill file capability signature: anyone holding it can replay the download
// until it expires.
var credentialQueryParams = map[string]bool{
	"sig": true,
}

// RedactQuery returns rawQuery with the values of credential-bearing
// parameters replaced by REDACTED. Other parameters, and their order, are
// preserved. A query that cannot be parsed is dropped entirely rather than
// risk logging a credential.
func RedactQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	for i, p := range parts {
		key, _, hasValue := strings.Cut(p, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			return redactedValue
		}
		if credentialQueryParams[strings.ToLower(name)] && hasValue {
			parts[i] = key + "=" + redactedValue
		}
	}
	return strings.Join(parts, "&")
}

// RedactedArtifactPath is how every artifact request path is logged.
const RedactedArtifactPath = "/api/v1/artifacts/" + redactedValue

// maxUnescapeRounds bounds how many times artifactPath percent-decodes a
// path while looking for the artifact routes.
const maxUnescapeRounds = 4

// artifactPath reports whether the URL path p (decoded or escaped) may
// belong to the artifact routes, some of which carry bearer credentials in
// their path (share-link tokens, view capabilities). It does not try to
// recognise the credential routes: any path that, lowercased and
// percent-decoded up to maxUnescapeRounds times, contains "artifacts"
// anywhere counts, so no spelling of a path (dot segments, doubled or
// escaped slashes, escaped letters) reaches a log or a trace whole.
// Paths that merely contain the word are redacted too; that is the
// intended trade-off.
func artifactPath(p string) bool {
	s := strings.ToLower(p)
	for range maxUnescapeRounds {
		if strings.Contains(s, "artifacts") {
			return true
		}
		u, err := url.PathUnescape(s)
		if err != nil || u == s {
			break
		}
		s = strings.ToLower(u)
	}
	return strings.Contains(s, "artifacts")
}

// isArtifactURL applies artifactPath to both forms of u's path: the
// decoded path and the escaped path the router may see.
func isArtifactURL(u *url.URL) bool {
	return u != nil && (artifactPath(u.Path) || artifactPath(u.EscapedPath()))
}

// IsCredentialURL reports whether a request for u must not be recorded
// with its path, for example in a trace. It shares its predicate with
// RequestPath and RedactURL.
func IsCredentialURL(u *url.URL) bool { return isArtifactURL(u) }

// RequestPath returns r's URL path for a log line: RedactedArtifactPath
// for any request under the artifact routes (see artifactPath, which
// checks the decoded and the escaped path), the decoded path otherwise.
// Every log attribute that records a request path must use it.
func RequestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	if isArtifactURL(r.URL) {
		return RedactedArtifactPath
	}
	return r.URL.Path
}

// RedactURL returns u as a string with credential-bearing query parameter
// values redacted (see RedactQuery) and, for an artifact request (see
// RequestPath), the path replaced by RedactedArtifactPath. u is not
// modified.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.RawQuery = RedactQuery(u.RawQuery)
	if isArtifactURL(u) {
		c.Path, c.RawPath = RedactedArtifactPath, ""
	}
	return c.String()
}

// urlInText matches a URL with an authority (scheme://...) in free text. It
// stops at whitespace, double quotes and angle brackets, so a URL quoted
// with %q (as in *url.Error) is matched without its quotes. A single quote
// is legal inside userinfo and query values, so it does not end a match;
// a trailing one (git quotes URLs as 'https://...') is trimmed instead.
var urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"<>]+`)

// urlTrailingPunct is punctuation trimmed from the end of a matched URL
// because it more likely belongs to the surrounding text.
const urlTrailingPunct = ".,;:)]}'`"

// redactURLsInText returns s with every scheme://... URL in it redacted for
// a log line. It is meant for free text such as error messages, where the
// URL's origin is unknown and any part of it may carry a credential:
// userinfo (user:password or a username-only token) becomes REDACTED,
// every query parameter value (or bare query part) becomes REDACTED with
// keys kept, and a fragment becomes REDACTED. A URL with none of these is
// left byte for byte as it was. A URL that does not parse is replaced by
// its scheme and "://REDACTED"; if it ends in a backslash (a double quote
// escaped by %q, so the credential may continue past the match), the text
// up to the next whitespace is dropped too.
func redactURLsInText(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range urlInText.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if start < last {
			continue // inside text already dropped after an escaped quote
		}
		b.WriteString(s[last:start])
		red, ok := redactURL(s[start:end])
		b.WriteString(red)
		if !ok && strings.HasSuffix(strings.TrimRight(s[start:end], urlTrailingPunct), `\`) {
			if i := strings.IndexFunc(s[end:], unicode.IsSpace); i >= 0 {
				end += i
			} else {
				end = len(s)
			}
		}
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactURL redacts one URL matched by urlInText, keeping trailing
// punctuation (urlTrailingPunct) outside it unless the URL only parses
// with it (as for "http://[::1]"). ok is false when the URL does not parse.
func redactURL(raw string) (redacted string, ok bool) {
	trimmed := strings.TrimRight(raw, urlTrailingPunct)
	tail := raw[len(trimmed):]
	u, err := url.Parse(trimmed)
	if err != nil {
		if u, err = url.Parse(raw); err == nil {
			trimmed, tail = raw, ""
		}
	}
	if err != nil {
		scheme, _, _ := strings.Cut(trimmed, "://")
		return scheme + "://" + redactedValue + tail, false
	}
	if u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		return raw, true
	}
	if u.User != nil {
		u.User = url.User(redactedValue)
	}
	u.RawQuery = redactQueryValues(u.RawQuery)
	if u.Fragment != "" {
		u.Fragment, u.RawFragment = redactedValue, ""
	}
	return u.String() + tail, true
}

// redactQueryValues replaces the value of every parameter in rawQuery with
// REDACTED, keeping keys and order. A part without "=" may itself be a bare
// token, so it is replaced whole.
func redactQueryValues(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	for i, p := range parts {
		if key, _, ok := strings.Cut(p, "="); ok {
			parts[i] = key + "=" + redactedValue
		} else if p != "" {
			parts[i] = redactedValue
		}
	}
	return strings.Join(parts, "&")
}
