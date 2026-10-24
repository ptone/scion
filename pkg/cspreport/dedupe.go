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

package cspreport

import (
	"strings"
	"sync"
	"time"
)

// Dedupe collapses repeats of the same violation. A page can report the
// same violation many times in one load (once per rendered element, and
// again on each re-render), and some browsers send every report as its own
// request. Two reports are the same violation when their directive, the
// origin of their blocked URI and their page path match.
//
// The first report of a violation is logged; the repeats within the next
// interval are counted and not logged. The first report after the interval
// is logged again, with the count of the repeats since the previous record
// as "repeats". At most maxKeys violations are tracked: when a new one
// arrives with the table full, the entries whose interval is over are
// removed, and if none is, one other entry is removed. So memory stays
// bounded whatever the reports hold, and the record limiter still bounds
// what is logged.
//
// A Dedupe is safe for concurrent use. A nil *Dedupe collapses nothing.
type Dedupe struct {
	interval time.Duration
	maxKeys  int
	now      func() time.Time

	mu      sync.Mutex
	entries map[string]*dedupeEntry
}

// dedupeEntry is the state of one violation: when its record was written,
// and the repeats counted since.
type dedupeEntry struct {
	logged  time.Time
	repeats int
}

// NewDedupe returns a Dedupe that logs each violation at most once per
// interval and tracks at most maxKeys violations (at least 1).
func NewDedupe(interval time.Duration, maxKeys int) *Dedupe {
	if maxKeys < 1 {
		maxKeys = 1
	}
	return &Dedupe{
		interval: interval,
		maxKeys:  maxKeys,
		now:      time.Now,
		entries:  make(map[string]*dedupeEntry),
	}
}

// verdict is what admit decided for one report.
type verdict int

const (
	// verdictWrite: write the record.
	verdictWrite verdict = iota
	// verdictRepeat: a repeat within the interval; counted, not logged.
	verdictRepeat
	// verdictRefused: the record limiter refused; nothing is recorded, so
	// a later report of the same violation is still logged.
	verdictRefused
)

// admit decides whether the report with key is written. allowRecord is
// asked only for a report that is not a repeat. On verdictWrite, repeats
// is the count of repeats collapsed since the previous record of key; on
// verdictRefused, retry is the limiter's Retry-After.
func (d *Dedupe) admit(key string, allowRecord func() (bool, int)) (v verdict, repeats, retry int) {
	if d == nil {
		if ok, retry := allowRecord(); !ok {
			return verdictRefused, 0, retry
		}
		return verdictWrite, 0, 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	e, seen := d.entries[key]
	if seen && now.Sub(e.logged) < d.interval {
		e.repeats++
		return verdictRepeat, 0, 0
	}
	if ok, retry := allowRecord(); !ok {
		return verdictRefused, 0, retry
	}
	if seen {
		repeats = e.repeats
		e.logged, e.repeats = now, 0
		return verdictWrite, repeats, 0
	}
	if len(d.entries) >= d.maxKeys {
		d.evict(now)
	}
	d.entries[key] = &dedupeEntry{logged: now}
	return verdictWrite, 0, 0
}

// evict makes room for one entry: it removes every entry whose interval
// is over, and when none is, one other entry. d.mu is held.
func (d *Dedupe) evict(now time.Time) {
	for k, e := range d.entries {
		if now.Sub(e.logged) >= d.interval {
			delete(d.entries, k)
		}
	}
	if len(d.entries) < d.maxKeys {
		return
	}
	for k := range d.entries {
		delete(d.entries, k)
		return
	}
}

// size returns the number of tracked violations.
func (d *Dedupe) size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}

// splitWebURL splits a URL reduced by reduceURL that has an authority
// ("scheme://host/path" or "//host/path") into its origin
// ("scheme://host" or "//host") and its path. ok is false for any other
// value.
func splitWebURL(reduced string) (origin, path string, ok bool) {
	prefix, rest := "", ""
	switch scheme, afterScheme, hasScheme := splitScheme(reduced); {
	case hasScheme && strings.HasPrefix(afterScheme, "//"):
		prefix, rest = scheme+"://", afterScheme[2:]
	case !hasScheme && strings.HasPrefix(reduced, "//"):
		prefix, rest = "//", reduced[2:]
	default:
		return "", "", false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return prefix + rest[:i], rest[i:], true
	}
	return prefix + rest, "", true
}

// urlOrigin returns the origin of a URL reduced by reduceURL: scheme and
// host for a URL with an authority, and the value itself for a keyword
// (such as "inline" or "trusted-types-sink") or a bare scheme (such as
// "data:"). A path without a host gives "".
func urlOrigin(reduced string) string {
	if origin, _, ok := splitWebURL(reduced); ok {
		return origin
	}
	if strings.Contains(reduced, "/") {
		return ""
	}
	return reduced
}

// documentPath returns the path of the page a report is about: the URL is
// reduced by reduceURL (no user information, query or fragment), then the
// scheme and host are dropped. A URL with an authority and no path gives
// "/"; a bare scheme (such as "about:" or "data:") is kept as is.
func documentPath(documentURL string) string {
	reduced := reduceURL(documentURL)
	if _, path, ok := splitWebURL(reduced); ok {
		if path == "" {
			return "/"
		}
		return path
	}
	return reduced
}
