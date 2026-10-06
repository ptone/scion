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

//go:build !no_sqlite

package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/artifacts/remotefetch"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

// fakeFetcher serves fixed bodies by URL and fails everything else with
// the given reasons, recording the calls and their deadlines.
type fakeFetcher struct {
	mu         sync.Mutex
	bodies     map[string][]byte
	reasons    map[string]remotefetch.Reason
	calls      []string
	noDeadline bool
}

func (f *fakeFetcher) Fetch(ctx context.Context, u string) (*remotefetch.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, u)
	if _, ok := ctx.Deadline(); !ok {
		f.noDeadline = true
	}
	body, ok := f.bodies[u]
	reason := f.reasons[u]
	f.mu.Unlock()
	if !ok {
		if reason == "" {
			reason = remotefetch.ReasonStatus
		}
		return nil, &remotefetch.Error{Reason: reason, Detail: "detail that must not leak: 10.0.0.5"}
	}
	sum := sha256.Sum256(body)
	return &remotefetch.Result{Body: body, ContentType: remotefetch.SniffImage(body), SHA256: hex.EncodeToString(sum[:])}, nil
}

func (f *fixture) useFetcher(ff *fakeFetcher) {
	f.svc.setImageFetcherFactory(func(RemoteImageLimits) ImageFetcher { return ff })
}

func TestExtractImageURLs(t *testing.T) {
	md := strings.Join([]string{
		"# Title",
		"![a](https://img.example/a.png)",
		"![b](<https://img.example/b c.png> \"title\")",
		"![dup](https://img.example/a.png)",
		"![rel](images/local.png)",
		"![data](data:image/png;base64,AAAA)",
		"![js](javascript:alert(1))",
		"![plain http](http://img.example/h.png)",
		"![ref][logo] and ![Shortcut]",
		"[logo]: https://img.example/logo.png",
		"[shortcut]: <https://img.example/short.png>",
		"Inline \x60![code](https://img.example/code-span.png)\x60 is skipped.",
		"\x60\x60\x60",
		"![fenced](https://img.example/fenced.png)",
		"\x60\x60\x60",
		"<p><img alt=x src=\"https://img.example/in-block.png\"></p>",
		"",
		"Text with <img alt=x src=\"https://img.example/html.png?x=1&amp;y=2\"> inline.",
		"",
		"<img src=\"https://img.example/alone.png\">",
		"![esc](https://img.example/a\\_b.png)",
		"[link not image](https://img.example/link.png)",
	}, "\n")
	got := extractImageURLs(md, 100)
	want := []string{
		"https://img.example/a.png",
		"https://img.example/b c.png",
		"http://img.example/h.png",
		"https://img.example/logo.png",
		"https://img.example/short.png",
		"https://img.example/html.png?x=1&y=2",
		"https://img.example/alone.png",
		"https://img.example/a_b.png",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestExtractImageURLsLinearAndBounded: a large document of interleaved
// inline and reference images is scanned in linear time, and extraction
// stops at the limit.
func TestExtractImageURLsLinearAndBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("[r]: https://img.example/ref.png\n")
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(&b, "![a](https://img.example/%d.png) ![b][r] ![c](rel/%d.png)\n", i, i)
	}
	md := b.String()

	start := time.Now()
	got := extractImageURLs(md, 128)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("extraction took %v", elapsed)
	}
	if len(got) != 128 || got[0] != "https://img.example/0.png" || got[1] != "https://img.example/ref.png" || got[2] != "https://img.example/1.png" {
		t.Fatalf("got %d URLs starting %q", len(got), got[:min(3, len(got))])
	}

	// Without an early stop the full document still scans quickly.
	start = time.Now()
	all := extractImageURLs(md, 1_000_000)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("full extraction took %v", elapsed)
	}
	if len(all) != 100_001 {
		t.Fatalf("full extraction found %d URLs", len(all))
	}
	if extractImageURLs(md, 0) != nil {
		t.Fatal("limit 0 must extract nothing")
	}
}

// TestRemoteImageLimitsFailClosed: limits a host supplies that are
// incomplete or invalid turn remote images off; the defaults apply only
// when the host gives no limits at all.
func TestRemoteImageLimitsFailClosed(t *testing.T) {
	ctx := context.Background()
	if got := (backend{}).remoteImageLimits(ctx); got != DefaultRemoteImageLimits() {
		t.Fatalf("no limits getter: %+v", got)
	}
	for name, l := range map[string]RemoteImageLimits{
		"zero":            {},
		"enabled, no cap": {Enabled: true, MaxBytes: 1, FetchTimeout: time.Second, TotalBudget: time.Second},
		"negative bytes":  {Enabled: true, MaxCount: 1, MaxBytes: -1, FetchTimeout: time.Second, TotalBudget: time.Second},
	} {
		b := backend{limits: func(context.Context) Limits { return Limits{RemoteImages: l} }}
		if got := b.remoteImageLimits(ctx); got.Enabled {
			t.Fatalf("%s: remote images enabled: %+v", name, got)
		}
	}

	f := newFixture(t, false)
	ff := &fakeFetcher{bodies: map[string][]byte{"https://img.example/a.png": testPNG}}
	f.useFetcher(ff)
	f.svc.SetLimits(func(context.Context) Limits { return Limits{MaxFileBytes: 1 << 20} })
	resp := f.publish(agentA, "doc.md", []byte("![a](https://img.example/a.png)"), "")
	if len(ff.calls) != 0 || resp.Version.FileCount != 1 {
		t.Fatalf("host limits without remote image limits must not fetch: calls %v files %d", ff.calls, resp.Version.FileCount)
	}
}

func TestRemotePath(t *testing.T) {
	sum := sha256.Sum256([]byte("https://img.example/a.png"))
	if got := RemotePath("https://img.example/a.png"); got != "_remote/"+hex.EncodeToString(sum[:]) {
		t.Fatalf("RemotePath = %q", got)
	}
}

// TestPublishMarkdownFetchesRemoteImages: one image fetches, one is refused
// and one is missing; the publish succeeds, the good image is a manifest
// file, the others are failed rows, and the warnings are identical and
// generic (a refused address and a missing image look the same).
func TestPublishMarkdownFetchesRemoteImages(t *testing.T) {
	f := newFixture(t, false)
	good, refused, missing := "https://img.example/good.png", "https://169.254.169.254/latest", "https://img.example/missing.png"
	ff := &fakeFetcher{bodies: map[string][]byte{good: testPNG}, reasons: map[string]remotefetch.Reason{refused: remotefetch.ReasonDeniedAddress, missing: remotefetch.ReasonStatus}}
	f.useFetcher(ff)
	md := "![g](" + good + ")\n![r](" + refused + ")\n![m](" + missing + ")\n"
	resp := f.publish(agentA, "doc.md", []byte(md), "")

	if len(ff.calls) != 3 || ff.noDeadline {
		t.Fatalf("fetch calls %v, every call must carry the budget deadline (missing: %v)", ff.calls, ff.noDeadline)
	}
	files := map[string]FileInfo{}
	for _, fi := range resp.Version.Files {
		files[fi.Path] = fi
	}
	if len(files) != 4 || resp.Version.FileCount != 4 {
		t.Fatalf("manifest %+v", resp.Version.Files)
	}
	g := files[RemotePath(good)]
	if g.Origin != FileOriginRemote || g.FetchStatus != FetchStatusOK || g.SourceURL != good || g.SHA256 != sha(testPNG) || g.MediaType != "image/png" {
		t.Fatalf("good row %+v", g)
	}
	if resp.Version.TotalBytes != int64(len(md)+len(testPNG)) {
		t.Fatalf("total bytes %d", resp.Version.TotalBytes)
	}
	for _, u := range []string{refused, missing} {
		r := files[RemotePath(u)]
		if r.FetchStatus != FetchStatusFailed || r.SHA256 != "" || r.SourceURL != u {
			t.Fatalf("failed row %+v", r)
		}
	}
	wantWarnings := []string{remoteFetchFailed + ": " + refused, remoteFetchFailed + ": " + missing}
	if strings.Join(resp.Warnings, "|") != strings.Join(wantWarnings, "|") {
		t.Fatalf("warnings %q, want %q", resp.Warnings, wantWarnings)
	}
	rec := f.do(&agentA, http.MethodGet, "/api/v1/artifacts/"+resp.Artifact.ID, nil, nil)
	for _, leak := range []string{"denied", "10.0.0.5", "bad_status", "detail"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("metadata leaks %q: %s", leak, rec.Body.String())
		}
	}
	// The stored error text is the generic one for both failures.
	v, err := f.store.GetVersion(context.Background(), resp.Artifact.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{refused, missing} {
		row, err := f.store.GetFile(context.Background(), v.ID, RemotePath(u))
		if err != nil || row.FetchError != remoteFetchFailed {
			t.Fatalf("stored row for %s: %+v %v", u, row, err)
		}
	}
}

func TestServeRemoteImages(t *testing.T) {
	f := newFixture(t, true)
	good, bad := "https://img.example/good.png", "https://img.example/bad.png"
	f.useFetcher(&fakeFetcher{bodies: map[string][]byte{good: testPNG}})
	resp := f.publish(agentA, "doc.md", []byte("![g]("+good+") ![b]("+bad+")"), "")
	base := "/api/v1/artifacts/" + resp.Artifact.ID + "/versions/1/files/"

	rec := f.do(&agentA, http.MethodGet, base+RemotePath(good)+"?stream=1", nil, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), testPNG) {
		t.Fatalf("good image: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Fatalf("cache control %q", cc)
	}
	if rec.Header().Get(HeaderRemoteStatus) != "" {
		t.Fatal("status header on a good image")
	}

	rec = f.do(&agentA, http.MethodGet, base+RemotePath(bad)+"?stream=1", nil, nil)
	if rec.Code != http.StatusNotFound || rec.Header().Get(HeaderRemoteStatus) != "failed" {
		t.Fatalf("failed image: %d %q", rec.Code, rec.Header().Get(HeaderRemoteStatus))
	}

	// A path nobody fetched is a plain 404, without the header.
	rec = f.do(&agentA, http.MethodGet, base+RemotePath("https://img.example/other.png")+"?stream=1", nil, nil)
	if rec.Code != http.StatusNotFound || rec.Header().Get(HeaderRemoteStatus) != "" {
		t.Fatalf("unknown remote path: %d %q", rec.Code, rec.Header().Get(HeaderRemoteStatus))
	}
}

// TestRemoteImageAuthorization: remote files go through the same chain as
// any other file, credential check first; an unreadable artifact answers a
// plain 404 even for a failed image, so the status header is no oracle.
func TestRemoteImageAuthorization(t *testing.T) {
	f := newFixture(t, false)
	good, bad := "https://img.example/good.png", "https://img.example/bad.png"
	f.useFetcher(&fakeFetcher{bodies: map[string][]byte{good: testPNG}})
	resp := f.publish(agentA, "doc.md", []byte("![g]("+good+") ![b]("+bad+")"), "")
	base := "/api/v1/artifacts/" + resp.Artifact.ID + "/files/"

	for _, p := range []*principal{nil, &agentX, &outside} {
		for _, u := range []string{good, bad} {
			rec := f.do(p, http.MethodGet, base+RemotePath(u), nil, nil)
			if rec.Code != http.StatusNotFound || rec.Header().Get(HeaderRemoteStatus) != "" {
				t.Fatalf("%v %s: %d %q", p, u, rec.Code, rec.Header().Get(HeaderRemoteStatus))
			}
		}
	}
	f.host.deny(agentB, "project-1", PermissionRead)
	f.host.calls = nil
	rec := f.do(&agentB, http.MethodGet, base+RemotePath(good), nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("credential denial: %d", rec.Code)
	}
	if len(f.host.calls) == 0 || !strings.HasPrefix(f.host.calls[0], "permits ") {
		t.Fatalf("Permits not asked first: %v", f.host.calls)
	}
	if rec := f.do(&agentA, http.MethodGet, base+RemotePath(good), nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("owner: %d", rec.Code)
	}
}

func TestPublishRejectsReservedRemoteNames(t *testing.T) {
	f := newFixture(t, false)
	for _, name := range []string{"_remote"} {
		rec := f.do(&agentA, http.MethodPost, "/api/v1/artifacts?name="+name, []byte("x"), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d", name, rec.Code)
		}
	}
	if !isReservedPath("_remote/abc") || !isReservedPath("_remote") || isReservedPath("_remotes") || isReservedPath("a/_remote/b") {
		t.Fatal("isReservedPath")
	}
}

func TestPublishRemoteImageLimits(t *testing.T) {
	f := newFixture(t, false)
	ff := &fakeFetcher{bodies: map[string][]byte{}}
	var md strings.Builder
	for i := 0; i < 5; i++ {
		u := "https://img.example/" + string(rune('a'+i)) + ".png"
		ff.bodies[u] = testPNG
		md.WriteString("![x](" + u + ")\n")
	}
	f.useFetcher(ff)
	f.svc.SetLimits(func(context.Context) Limits {
		return Limits{RemoteImages: RemoteImageLimits{Enabled: true, MaxCount: 2, MaxBytes: 1 << 20, FetchTimeout: time.Second, TotalBudget: time.Second}}
	})
	resp := f.publish(agentA, "doc.md", []byte(md.String()), "")
	if len(ff.calls) != 2 || resp.Version.FileCount != 3 {
		t.Fatalf("calls %v, files %d", ff.calls, resp.Version.FileCount)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "3 more remote images") {
		t.Fatalf("warnings %q", resp.Warnings)
	}

	// Disabled: nothing is fetched and no rows are written.
	ff.calls = nil
	f.svc.SetLimits(func(context.Context) Limits {
		return Limits{RemoteImages: RemoteImageLimits{Enabled: false, MaxCount: 2, MaxBytes: 1 << 20, FetchTimeout: time.Second, TotalBudget: time.Second}}
	})
	resp = f.publish(agentA, "doc2.md", []byte(md.String()), "")
	if len(ff.calls) != 0 || resp.Version.FileCount != 1 || len(resp.Warnings) != 0 {
		t.Fatalf("disabled: calls %v files %d warnings %q", ff.calls, resp.Version.FileCount, resp.Warnings)
	}
}

func TestPublishNonMarkdownFetchesNothing(t *testing.T) {
	f := newFixture(t, false)
	ff := &fakeFetcher{bodies: map[string][]byte{"https://img.example/a.png": testPNG}}
	f.useFetcher(ff)
	resp := f.publish(agentA, "notes.txt", []byte("![a](https://img.example/a.png)"), "")
	if len(ff.calls) != 0 || resp.Version.FileCount != 1 {
		t.Fatalf("calls %v files %d", ff.calls, resp.Version.FileCount)
	}
}
