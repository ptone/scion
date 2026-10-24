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
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// safariReportToBody is a single report as Safari sends it to a report-to
// endpoint: one object with "type", "url" and "body" (no array), sent
// with Content-Type application/csp-report. The body members and their
// order are WebKit's (CSPViolationReportBody); a Trusted Types sink report
// has an empty sample and no source file.
const safariReportToBody = `{"type":"csp-violation","url":"https://hub.example/projects/p1/chat/c1",` +
	`"body":{"documentURL":"https://hub.example/projects/p1/chat/c1","disposition":"report","referrer":"",` +
	`"effectiveDirective":"require-trusted-types-for","blockedURL":"trusted-types-sink",` +
	`"originalPolicy":"require-trusted-types-for 'script'; report-to csp","statusCode":200,"sample":""}}`

// safariReportURIBody is the older form as Safari sends it to a report-uri
// endpoint (WebKit's member names and order).
const safariReportURIBody = `{"type":"csp-violation","url":"https://hub.example/projects/p1/chat/c1",` +
	`"csp-report":{"document-uri":"https://hub.example/projects/p1/chat/c1","referrer":"",` +
	`"violated-directive":"img-src","effective-directive":"img-src","original-policy":"img-src 'self'",` +
	`"blocked-uri":"https://cdn.example/a.png","status-code":200,` +
	`"source-file":"https://hub.example/assets/main.js","line-number":3,"column-number":9}}`

// reportingAPIArrayBody is a Reporting API array as Chromium sends it.
const reportingAPIArrayBody = `[{"type":"csp-violation","age":10,"url":"https://hub.example/projects/p1/chat/c1",` +
	`"user_agent":"UA","body":{"documentURL":"https://hub.example/projects/p1/chat/c1",` +
	`"effectiveDirective":"require-trusted-types-for","blockedURL":"trusted-types-sink",` +
	`"sourceFile":"https://hub.example/assets/main.js","lineNumber":42,"columnNumber":7,` +
	`"sample":"Element innerHTML|<b>x</b>","disposition":"report","statusCode":200}}]`

// TestEveryReportShapeIsAccepted: each body shape is logged with either
// report content type, since browsers do not agree on the pairing.
func TestEveryReportShapeIsAccepted(t *testing.T) {
	shapes := []struct {
		name, body, directive string
	}{
		{"single report-to object", safariReportToBody, "require-trusted-types-for"},
		{"older form", safariReportURIBody, "img-src"},
		{"older form without type", legacyBody, "script-src-elem"},
		{"Reporting API array", reportingAPIArrayBody, "require-trusted-types-for"},
	}
	for _, shape := range shapes {
		for _, contentType := range []string{"application/csp-report", "application/reports+json"} {
			t.Run(shape.name+"/"+contentType, func(t *testing.T) {
				var buf bytes.Buffer
				h := &Handler{Log: recordingLog(&buf)}
				if rec := post(h, contentType, shape.body); rec.Code != http.StatusNoContent {
					t.Fatalf("status = %d, want 204; body %q", rec.Code, rec.Body.String())
				}
				got := records(t, &buf)
				if len(got) != 1 {
					t.Fatalf("%d records, want 1", len(got))
				}
				if got[0]["msg"] != LogMessage || got[0]["directive"] != shape.directive {
					t.Fatalf("record = %v, want %s with directive %q", got[0], LogMessage, shape.directive)
				}
			})
		}
	}
}

// TestSafariReportToFields: the fields of Safari's single report-to object
// reach the record.
func TestSafariReportToFields(t *testing.T) {
	var buf bytes.Buffer
	h := &Handler{Log: recordingLog(&buf)}
	if rec := post(h, "application/csp-report", safariReportToBody); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	got := records(t, &buf)
	if len(got) != 1 {
		t.Fatalf("%d records, want 1", len(got))
	}
	want := map[string]any{
		"directive":     "require-trusted-types-for",
		"blocked_uri":   "trusted-types-sink",
		"source_file":   "",
		"line":          float64(0),
		"document_path": "/projects/p1/chat/c1",
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("%s = %v, want %v", k, got[0][k], v)
		}
	}
	if _, ok := got[0]["repeats"]; ok {
		t.Errorf("repeats on a first record: %v", got[0])
	}
}

// TestDocumentPathIsPathOnly: the page is logged as its path; the host,
// user information, query and fragment are not logged.
func TestDocumentPathIsPathOnly(t *testing.T) {
	const marker = "Secret7c1d"
	bodies := map[string]string{
		"single report-to object": `{"type":"csp-violation","body":{"effectiveDirective":"img-src",` +
			`"documentURL":"https://user:` + marker + `@hub.example/projects/p1/chat/c1?token=` + marker + `#` + marker + `"}}`,
		"older form": `{"csp-report":{"effective-directive":"img-src",` +
			`"document-uri":"https://hub.example/projects/p1/chat/c1?token=` + marker + `#` + marker + `"}}`,
		"report url when the body has none": `[{"type":"csp-violation","url":"https://hub.example/projects/p1/chat/c1?q=` + marker + `",` +
			`"body":{"effectiveDirective":"img-src"}}]`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			h := &Handler{Log: recordingLog(&buf)}
			if rec := post(h, "application/csp-report", body); rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			out := buf.String()
			got := records(t, bytes.NewBufferString(out))
			if len(got) != 1 || got[0]["document_path"] != "/projects/p1/chat/c1" {
				t.Fatalf("records = %v, want one with document_path /projects/p1/chat/c1", got)
			}
			if strings.Contains(out, marker) || strings.Contains(out, "hub.example") {
				t.Fatalf("log holds the host or the query: %s", out)
			}
		})
	}
}

func TestDocumentPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://hub.example/projects/p1/chat/c1", "/projects/p1/chat/c1"},
		{"https://hub.example", "/"},
		{"https://hub.example/", "/"},
		{"https://hub.example/a?b=c#d", "/a"},
		{"https://u:p@hub.example/a", "/a"},
		{"//hub.example/a?b", "/a"},
		{"about:blank", "about:"},
		{"data:text/html,<b>x</b>", "data:"},
		{"blob:https://hub.example/1234", "blob:"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := documentPath(tc.in); got != tc.want {
			t.Errorf("documentPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestURLOrigin(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://cdn.example/a.png", "https://cdn.example"},
		{"https://cdn.example", "https://cdn.example"},
		{"wss://hub.example:8443/ws", "wss://hub.example:8443"},
		{"//cdn.example/a", "//cdn.example"},
		{"trusted-types-sink", "trusted-types-sink"},
		{"inline", "inline"},
		{"data:", "data:"},
		{"blob:", "blob:"},
		{"/relative/path", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := urlOrigin(tc.in); got != tc.want {
			t.Errorf("urlOrigin(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// fakeClock is a settable clock for a Dedupe.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// dedupeHandler returns a Handler with a Dedupe on clock, logging to buf,
// and a pointer to the number of times the record limiter was asked.
func dedupeHandler(buf *bytes.Buffer, clock *fakeClock, interval time.Duration, maxKeys int) (*Handler, *int) {
	d := NewDedupe(interval, maxKeys)
	d.now = clock.now
	asked := 0
	return &Handler{
		Log:    recordingLog(buf),
		Dedupe: d,
		AllowRecord: func() (bool, int) {
			asked++
			return true, 0
		},
	}, &asked
}

// TestRepeatsAreCollapsed: repeats of one violation within the interval
// answer 204 and are not logged or charged to the record limiter; the
// first report after the interval is logged with the count of repeats.
func TestRepeatsAreCollapsed(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	h, asked := dedupeHandler(&buf, clock, 10*time.Minute, 16)

	for i := 0; i < 50; i++ {
		if rec := post(h, "application/csp-report", safariReportToBody); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d: status = %d, want 204", i+1, rec.Code)
		}
	}
	if got := records(t, &buf); len(got) != 1 {
		t.Fatalf("%d records for 50 identical reports, want 1", len(got))
	}
	if *asked != 1 {
		t.Fatalf("record limiter asked %d times, want 1", *asked)
	}

	// The same violation in a Reporting API array is a repeat too.
	if rec := post(h, "application/reports+json", reportingAPIArrayBody); rec.Code != http.StatusNoContent {
		t.Fatalf("array: status = %d, want 204", rec.Code)
	}
	if got := records(t, &buf); len(got) != 0 {
		t.Fatalf("%d records for a repeat in an array, want 0", len(got))
	}

	// Positive control: another page is another violation.
	other := strings.ReplaceAll(safariReportToBody, "/chat/c1", "/chat/c2")
	if rec := post(h, "application/csp-report", other); rec.Code != http.StatusNoContent {
		t.Fatalf("other page: status = %d, want 204", rec.Code)
	}
	if got := records(t, &buf); len(got) != 1 || got[0]["document_path"] != "/projects/p1/chat/c2" {
		t.Fatalf("records = %v, want one for /projects/p1/chat/c2", got)
	}

	clock.t = clock.t.Add(10 * time.Minute)
	if rec := post(h, "application/csp-report", safariReportToBody); rec.Code != http.StatusNoContent {
		t.Fatalf("after the interval: status = %d, want 204", rec.Code)
	}
	got := records(t, &buf)
	if len(got) != 1 || got[0]["repeats"] != float64(50) {
		t.Fatalf("records after the interval = %v, want one with repeats=50", got)
	}
}

// TestRepeatsKeyOnBlockedOrigin: blocked URIs on one origin are the same
// violation; another origin, or another directive, is not.
func TestRepeatsKeyOnBlockedOrigin(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	h, _ := dedupeHandler(&buf, clock, 10*time.Minute, 16)
	report := func(directive, blocked string) string {
		return fmt.Sprintf(`{"type":"csp-violation","body":{"documentURL":"https://hub.example/",`+
			`"effectiveDirective":%q,"blockedURL":%q}}`, directive, blocked)
	}
	bodies := []string{
		report("img-src", "https://cdn.example/a.png"),
		report("img-src", "https://cdn.example/b.png"),
		report("img-src", "https://other.example/a.png"),
		report("connect-src", "https://cdn.example/a.png"),
	}
	for _, body := range bodies {
		if rec := post(h, "application/csp-report", body); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
	}
	got := records(t, &buf)
	if len(got) != 3 {
		t.Fatalf("%d records, want 3 (b.png repeats a.png)", len(got))
	}
}

// TestRepeatsInOneRequestAreCollapsed: identical reports in one array give
// one record and 204.
func TestRepeatsInOneRequestAreCollapsed(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	h, asked := dedupeHandler(&buf, clock, 10*time.Minute, 16)
	if rec := post(h, "application/reports+json", manyReports(5)); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := records(t, &buf); len(got) != 1 || *asked != 1 {
		t.Fatalf("%d records and %d limiter calls, want 1 and 1", len(got), *asked)
	}
}

// TestDedupeIsBounded: however many violations arrive, at most maxKeys
// are tracked, and each new one is still logged.
func TestDedupeIsBounded(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	const maxKeys = 4
	h, _ := dedupeHandler(&buf, clock, 10*time.Minute, maxKeys)
	const n = 100
	for i := 0; i < n; i++ {
		body := strings.ReplaceAll(safariReportToBody, "/chat/c1", fmt.Sprintf("/chat/c%d", i))
		if rec := post(h, "application/csp-report", body); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d: status = %d, want 204", i, rec.Code)
		}
		if size := h.Dedupe.size(); size > maxKeys {
			t.Fatalf("after %d violations %d are tracked, want at most %d", i+1, size, maxKeys)
		}
	}
	if got := records(t, &buf); len(got) != n {
		t.Fatalf("%d records for %d distinct violations, want %d", len(got), n, n)
	}

	// Entries whose interval is over are removed first: after the
	// interval, a full table is emptied down to the new entry.
	clock.t = clock.t.Add(10 * time.Minute)
	if rec := post(h, "application/csp-report", safariReportToBody); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if size := h.Dedupe.size(); size != 1 {
		t.Fatalf("after the interval %d entries are tracked, want 1", size)
	}
}

// TestRefusedRecordIsNotCountedAsSeen: a report the record limiter refuses
// is not logged, and the same violation is logged once the limiter
// allows it.
func TestRefusedRecordIsNotCountedAsSeen(t *testing.T) {
	var buf bytes.Buffer
	d := NewDedupe(10*time.Minute, 16)
	allow := false
	h := &Handler{
		Log:         recordingLog(&buf),
		Dedupe:      d,
		AllowRecord: func() (bool, int) { return allow, 3 },
	}
	rec := post(h, "application/csp-report", safariReportToBody)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("refused: status = %d, Retry-After %q, want 429 and 3", rec.Code, rec.Header().Get("Retry-After"))
	}
	if d.size() != 0 {
		t.Fatalf("a refused report is tracked")
	}
	allow = true
	if rec := post(h, "application/csp-report", safariReportToBody); rec.Code != http.StatusNoContent {
		t.Fatalf("allowed: status = %d, want 204", rec.Code)
	}
	if got := records(t, &buf); len(got) != 1 {
		t.Fatalf("%d records, want 1", len(got))
	}
}

// TestCollapsedRecordsLeaveSamplesOut: with Dedupe on, records still hold
// no sample, query or fragment by default, in every report shape.
func TestCollapsedRecordsLeaveSamplesOut(t *testing.T) {
	const marker = "Secret9b2e"
	bodies := []string{
		`{"type":"csp-violation","body":{"documentURL":"https://hub.example/a?x=` + marker + `",` +
			`"effectiveDirective":"require-trusted-types-for","blockedURL":"trusted-types-sink",` +
			`"sample":"Element innerHTML|` + marker + `"}}`,
		`{"csp-report":{"document-uri":"https://hub.example/b#` + marker + `","effective-directive":"script-src-elem",` +
			`"blocked-uri":"https://cdn.example/s.js?k=` + marker + `","script-sample":"` + marker + `"}}`,
		`[{"type":"csp-violation","body":{"documentURL":"https://hub.example/c","effectiveDirective":"script-src",` +
			`"blockedURL":"inline","sourceFile":"https://hub.example/m.js?` + marker + `","sample":"` + marker + `"}}]`,
	}
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	h, _ := dedupeHandler(&buf, clock, 10*time.Minute, 16)
	for _, body := range bodies {
		for i := 0; i < 2; i++ {
			if rec := post(h, "application/csp-report", body); rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
		}
	}
	out := buf.String()
	if strings.Contains(out, marker) {
		t.Fatalf("log holds a sample, query or fragment: %s", out)
	}
	// Positive control: one record per violation, with the sink kept.
	got := records(t, bytes.NewBufferString(out))
	if len(got) != len(bodies) || got[0]["sink"] != "Element innerHTML" {
		t.Fatalf("records = %v, want %d with the first naming its sink", got, len(bodies))
	}
}

// TestNilDedupeLogsEveryReport: without a Dedupe, identical reports are
// all logged.
func TestNilDedupeLogsEveryReport(t *testing.T) {
	var buf bytes.Buffer
	h := &Handler{Log: recordingLog(&buf)}
	for i := 0; i < 3; i++ {
		post(h, "application/csp-report", safariReportToBody)
	}
	if got := records(t, &buf); len(got) != 3 {
		t.Fatalf("%d records, want 3", len(got))
	}
}
