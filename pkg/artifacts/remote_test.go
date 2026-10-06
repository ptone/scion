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
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

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
	delay      time.Duration
}

func (f *fakeFetcher) Fetch(ctx context.Context, u string) (*remotefetch.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, u)
	if _, ok := ctx.Deadline(); !ok {
		f.noDeadline = true
	}
	body, ok := f.bodies[u]
	reason := f.reasons[u]
	delay := f.delay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if !ok {
		if reason == "" {
			reason = remotefetch.ReasonStatus
		}
		return nil, &remotefetch.Error{Reason: reason, Detail: "server-side detail: 10.0.0.5"}
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
		"",
		"<img src=\"https://img.example/block-1.png\">",
		"more text <img src=\"https://img.example/block-2.png\">",
		"",
		"para",
		"<span>x</span> <img src=\"https://img.example/in-para.png\">",
		"",
		"<!-- <img src=\"https://img.example/comment.png\"> -->",
		"",
		"![w](https://upload.example/File_(1).png)",
		"",
		"    ![code](https://img.example/indented.png)",
		"",
		"- item",
		"",
		"    ![continued](https://img.example/list-continuation.png)",
		"![esc](https://img.example/a\\_b.png)",
		"[link not image](https://img.example/link.png)",
	}, "\n")
	got := extractImageURLs(context.Background(), md, 100)
	want := []string{
		"https://img.example/a.png",
		"https://img.example/b c.png",
		"http://img.example/h.png",
		"https://img.example/logo.png",
		"https://img.example/short.png",
		"https://img.example/html.png?x=1&y=2",
		"https://img.example/alone.png",
		"https://img.example/in-para.png",
		"https://upload.example/File_(1).png",
		"https://img.example/list-continuation.png",
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
	got := extractImageURLs(context.Background(), md, 128)
	if elapsed := time.Since(start); elapsed > time.Second && !raceEnabled {
		t.Fatalf("extraction took %v", elapsed)
	}
	if len(got) != 128 || got[0] != "https://img.example/0.png" || got[1] != "https://img.example/ref.png" || got[2] != "https://img.example/1.png" {
		t.Fatalf("got %d URLs starting %q", len(got), got[:min(3, len(got))])
	}

	// Without an early stop every URL is found (a smaller document keeps
	// this quick under the race detector).
	var small strings.Builder
	small.WriteString("[r]: https://img.example/ref.png\n")
	for i := 0; i < 20_000; i++ {
		fmt.Fprintf(&small, "![a](https://img.example/%d.png) ![b][r] ![c](rel/%d.png)\n", i, i)
	}
	// A larger allowance than the default keeps the budget out of this
	// check: every one of the 20,001 distinct URLs is parsed.
	if all := extractImages(context.Background(), small.String(), 1_000_000, 64).urls; len(all) != 20_001 {
		t.Fatalf("full extraction found %d URLs", len(all))
	}
	if extractImageURLs(context.Background(), md, 0) != nil {
		t.Fatal("limit 0 must extract nothing")
	}
}

// TestExtractImageURLsUnusualInputs: unusual inputs (unclosed
// destinations, labels, tags and code spans, long single lines) are
// processed within a time bound.
func TestExtractImageURLsUnusualInputs(t *testing.T) {
	const size = 1 << 20
	repeat := func(unit string) string { return strings.Repeat(unit, size/len(unit)+1)[:size] }
	inputs := map[string]string{
		"unclosed angle destinations":  repeat("![](<a "),
		"unclosed reference labels":    repeat("![a][ "),
		"unclosed img tags":            repeat("<img a "),
		"definition lines":             repeat("[a]: <b \n"),
		"image openers only":           repeat("!["),
		"long label without close":     "![" + repeat("a"),
		"unclosed code spans":          repeat("`a "),
		"backtick runs of rising size": repeat("` `` ``` "),
		"angle brackets":               repeat("<"),
		"html comment never closed":    "<!--\n" + repeat("<img src=\"https://x.example/a.png\"> "),
		"div blocks":                   repeat("<div>\n"),
		"destinations at the cap":      repeat("![](" + strings.Repeat("a", 3000) + " "),
		"mixed":                        repeat("![a](<b ![c][ <img d [e]: <f `g "),
	}
	for name, md := range inputs {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			extractImageURLs(context.Background(), md, 128)
			if elapsed := time.Since(start); elapsed > time.Second && !raceEnabled {
				t.Fatalf("%s: extraction took %v", name, elapsed)
			}
		})
	}
}

// costShapes are document shapes the extraction cost tests mix: a
// preamble (a reference definition or an opening tag) and a unit repeated,
// or a generated unit that differs on each repeat.
var costShapes = []struct {
	name, preamble, unit string
	gen                  func(i int) string
}{
	{name: "img tags on one line", unit: `<img alt="x" src="https://img.example/a.png"> `},
	{name: "lone img lines", unit: "<img src=\"https://img.example/a.png\">\n\n"},
	{name: "relative images", unit: "![](a) "},
	{name: "tags without src", unit: "<img alt=x> "},
	{name: "backtick runs", unit: "` `` ``` "},
	{name: "unclosed angle destinations", unit: "![](<a "},
	{name: "unclosed reference labels", unit: "![a][ "},
	{name: "unclosed img tags", unit: "<img a "},
	{name: "definition lines", unit: "[a]: <b \n"},
	{name: "long inline destination", unit: "![a](https://img.example/" + strings.Repeat("a", 2000) + ") "},
	{name: "reference reused, one line", preamble: "[r]: https://img.example/" + strings.Repeat("a", 2000) + "\n", unit: "![a][r] "},
	{name: "reference reused, per line", preamble: "[r]: https://img.example/" + strings.Repeat("a", 2000) + "\n", unit: "![a][r]\n"},
	{name: "reference with escapes", preamble: "[r]: https://img.example/" + strings.Repeat("\\_", 1000) + "\n", unit: "![a][r] "},
	{name: "reference with character references", preamble: "[r]: https://img.example/?" + strings.Repeat("&amp;", 400) + "\n", unit: "![a][r] "},
	{name: "open comment block", preamble: "<!--\n", unit: "x <img src=\"https://img.example/a.png\">\n"},
	{name: "open div block", preamble: "<div>\n", unit: "line ![a](https://img.example/a.png)\n"},
	{name: "open pre block", preamble: "<pre>\n", unit: "code\n"},
	{name: "one destination read by three scanners", gen: func(i int) string {
		d := "http:///" + strings.Repeat("\\!", 340) + strconv.Itoa(i)
		return "[l" + strconv.Itoa(i) + "]: " + d + "<img/src=" + d + ">![](" + d + ")\n"
	}},
	{name: "distinct short relative destinations", gen: func(i int) string { return "![](" + strconv.Itoa(i) + ") " }},
	{name: "spaced labels with a non-ASCII byte", preamble: "[x]: https://img.example/x.png\n", unit: "![a\ta\ta\ta\ta\ta\ta\ta\té] "},
	{name: "lone img line whose src holds an inline image", gen: func(i int) string {
		d := "https://h/" + strings.Repeat("\\!&amp;", 60) + strings.Repeat("%25", 200) + strconv.Itoa(i)
		return "<img src=\"https://h/\\!&amp;![](" + d + ")\">\n\n"
	}},
	{name: "destination nested in three containers", gen: func(i int) string {
		d := strings.Repeat("\\!&amp;", 40) + strings.Repeat("%25", 150) + strconv.Itoa(i)
		return "[a" + strconv.Itoa(i) + "]: https://h/\\!&amp;![](https://h/\\!&amp;<img/src=https://h/" + d + ">)\n"
	}},
	{name: "fragment with repeated #", unit: "![](https:///#" + strings.Repeat("#", 2000) + ") "},
	{name: "invalid port", unit: "![](http://a:" + strings.Repeat("\xff", 64) + ") ![](http://a:b) "},
	{name: "uppercase scheme", unit: "![](HTTPS:///a) "},
	{name: "invalid UTF-8 labels defined and looked up", preamble: "[x]: https://img.example/x.png\n", unit: "[![" + strings.Repeat("\xff", 990) + "]: x\n"},
}

// costDocument builds a document of about size bytes from a shape.
func costDocument(preamble, unit string, gen func(int) string, size int) string {
	if gen == nil {
		n := (size - len(preamble)) / len(unit)
		return preamble + strings.Repeat(unit, n)
	}
	var b strings.Builder
	b.WriteString(preamble)
	for i := 0; b.Len() < size; i++ {
		b.WriteString(gen(i))
	}
	return b.String()[:size]
}

// measureExtraction runs one extraction with the given limit and
// allocation allowance and reports its result, time and bytes allocated.
func measureExtraction(md string, limit, allocPerByte int) (extractResult, time.Duration, uint64) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	res := extractImages(context.Background(), md, limit, allocPerByte)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	return res, elapsed, after.TotalAlloc - before.TotalAlloc
}

// allocSlack is the allocation the cost tests allow beyond the bytes
// charged, for the test's own measurement.
const allocSlack = 64 << 10

// checkCost asserts the bounds for one extraction of a document of size
// bytes: the time bound, at most the bytes charged (plus allocSlack), and
// a charge within the budget, which keeps allocation under 16 times the
// document.
func checkCost(t *testing.T, md string, size int, maxTime time.Duration) {
	t.Helper()
	res, elapsed, alloc := measureExtraction(md, 128, maxAllocPerByte)
	if elapsed > maxTime && !raceEnabled {
		t.Fatalf("took %v", elapsed)
	}
	if alloc > uint64(res.allocUsed)+allocSlack {
		t.Fatalf("allocated %d bytes, %d charged", alloc, res.allocUsed)
	}
	if res.allocUsed > maxAllocPerByte*size+minAllocBudget {
		t.Fatalf("charged %d bytes for a %d byte document", res.allocUsed, size)
	}
	if alloc > 16*uint64(size) {
		t.Fatalf("allocated %d bytes for a %d byte document", alloc, size)
	}
}

// TestExtractImageURLsCost: every known document shape, filling the 2 MiB
// window, is extracted within the time and allocation bounds.
func TestExtractImageURLsCost(t *testing.T) {
	const size = maxImageScanBytes
	for _, shape := range costShapes {
		t.Run(shape.name, func(t *testing.T) {
			checkCost(t, costDocument(shape.preamble, shape.unit, shape.gen, size), size, time.Second)
		})
	}
}

// TestExtractImageURLsCostMixed: random mixes of the known shapes, filling
// the window, stay within the time and allocation bounds.
func TestExtractImageURLsCostMixed(t *testing.T) {
	const size = maxImageScanBytes
	for seed := int64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			var b strings.Builder
			for b.Len() < size {
				shape := costShapes[rng.Intn(len(costShapes))]
				chunk := 1 + rng.Intn(64<<10)
				b.WriteString(costDocument(shape.preamble, shape.unit, shape.gen, len(shape.preamble)+chunk))
				if rng.Intn(4) == 0 {
					b.WriteString("\n\n")
				}
			}
			checkCost(t, b.String()[:size], size, time.Second)
		})
	}
}

// TestExtractImageURLsCostComposed: documents composed at random from
// destination contents (escapes, character references, percent escapes,
// repeated '#', invalid ports and IPv6 literals, uppercase schemes,
// missing hosts, relative paths, distinct suffixes) and labels (case,
// whitespace, non-ASCII and invalid bytes), placed in random containers
// (inline images, <img src>, definitions and their uses, all of them on
// one line, or nested inside one another) stay within the time bound and
// allocate no more than they charge.
func TestExtractImageURLsCostComposed(t *testing.T) {
	const size = maxImageScanBytes
	contents := []func(rng *rand.Rand, i int) string{
		func(rng *rand.Rand, i int) string {
			return "https://img.example/" + strings.Repeat("\\!", rng.Intn(400)) + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string {
			return "https://img.example/?" + strings.Repeat("&amp;", rng.Intn(300)) + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string {
			return "http:///" + strings.Repeat("a", rng.Intn(600)) + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string { return strconv.Itoa(i) },
		func(rng *rand.Rand, i int) string {
			return "https://img.example/" + strings.Repeat("b", rng.Intn(1500)) + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string {
			return "https://img.example/" + strings.Repeat("%25", rng.Intn(300)) + "#" + strings.Repeat("é", rng.Intn(100)) + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string {
			return "https://img.example/#" + strings.Repeat("#", rng.Intn(600)) + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string {
			return "http://a:" + strings.Repeat([]string{"b", "\xff", "\u0085"}[rng.Intn(3)], rng.Intn(300)) + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string {
			return "http://[" + strings.Repeat([]string{"\xff", "g", ":"}[rng.Intn(3)], rng.Intn(300)) + "]/" + strconv.Itoa(i)
		},
		func(rng *rand.Rand, i int) string { return "HTTPS:///" + strconv.Itoa(i) },
	}
	labelPieces := []string{"a", "B", " ", "  ", "\t", "\f", "é", "\xff", "\u023a"}
	label := func(rng *rand.Rand, i int) string {
		var b strings.Builder
		for n := rng.Intn(300); n > 0 && b.Len() < maxLabelBytes-12; n-- {
			b.WriteString(labelPieces[rng.Intn(len(labelPieces))])
		}
		b.WriteString(strconv.Itoa(i))
		return b.String()
	}
	containers := []func(d string, i int) string{
		func(d string, i int) string { return "![a](" + d + ") " },
		func(d string, i int) string { return "<img/src=" + d + "> " },
		func(d string, i int) string {
			return "[l" + strconv.Itoa(i) + "]: " + d + "\n![x][l" + strconv.Itoa(i) + "] "
		},
		func(d string, i int) string {
			return "[l" + strconv.Itoa(i) + "]: " + d + "<img/src=" + d + ">![](" + d + ")\n"
		},
		func(d string, i int) string { return "![a](" + d + "<img/src=" + d + ">![](" + d + "))\n" },
		func(d string, i int) string { return "<img src=\"" + d + "![](" + d + ")\">\n\n" },
	}
	labelled := []func(rng *rand.Rand, d string, i int) string{
		func(rng *rand.Rand, d string, i int) string {
			l := label(rng, i)
			return "[" + l + "]: " + d + "\n![x][" + l + "] ![" + strings.ToUpper(l) + "]\n"
		},
		func(rng *rand.Rand, d string, i int) string { return "[![" + label(rng, i) + "]: " + d + "\n" },
	}
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			var b strings.Builder
			for i := 0; b.Len() < size; i++ {
				d := contents[rng.Intn(len(contents))](rng, i)
				if rng.Intn(3) == 0 {
					b.WriteString(labelled[rng.Intn(len(labelled))](rng, d, i))
					continue
				}
				b.WriteString(containers[rng.Intn(len(containers))](d, i))
				if rng.Intn(8) == 0 {
					b.WriteString("\n")
				}
			}
			checkCost(t, b.String()[:size], size, time.Second)
		})
	}
}

// TestExtractImageURLsAtMaxFileSize: a document at the default file limit
// is scanned up to the 2 MiB window within the time bound.
func TestExtractImageURLsAtMaxFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("large document")
	}
	md := costDocument("[r]: https://img.example/"+strings.Repeat("a", 2000)+"\n", "![a][r] ", nil, 32<<20)
	res, elapsed, alloc := measureExtraction(md, 128, maxAllocPerByte)
	if elapsed > 2*time.Second && !raceEnabled {
		t.Fatalf("took %v", elapsed)
	}
	if alloc > uint64(res.allocUsed)+allocSlack || alloc > 16*maxImageScanBytes {
		t.Fatalf("allocated %d bytes, %d charged", alloc, res.allocUsed)
	}
}

// TestExtractImageURLsAllocBound: when the allocation budget is spent,
// the images found before it are kept, and the result is the same every
// time.
func TestExtractImageURLsAllocBound(t *testing.T) {
	head := "![a](https://img.example/1.png) ![b](https://img.example/2.png)\n![c](https://img.example/3.png)\n"
	// Distinct destinations with no host: each is parsed and rejected.
	body := costDocument("", "", func(i int) string { return "![x](http:///" + strconv.Itoa(i) + ") " }, 1<<20)
	md := head + body + "\n![late](https://img.example/late.png)\n"
	ctx := context.Background()
	res := extractImages(ctx, md, 128, maxAllocPerByte)
	if !res.budgetReached {
		t.Fatal("the allocation budget was not reached")
	}
	got := res.urls
	want := []string{"https://img.example/1.png", "https://img.example/2.png", "https://img.example/3.png"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := 0; i < 3; i++ {
		if again := extractImages(ctx, md, 128, maxAllocPerByte).urls; strings.Join(again, " ") != strings.Join(got, " ") {
			t.Fatalf("run %d: %v, want %v", i, again, got)
		}
	}
	// With a larger allowance the late image is found too.
	if all := extractImages(ctx, md, 128, 64).urls; len(all) != 4 {
		t.Fatalf("larger allowance found %v", all)
	}
}

// TestExtractImageURLsKeepsEarlyImages: an image on the first line is
// found, without reaching the budget, when the rest of the document is
// many short lines of HTML blocks or relative images.
func TestExtractImageURLsKeepsEarlyImages(t *testing.T) {
	const first = "![a](https://img.example/first.png)\n\n"
	units := []string{"<br>\n\n", "<hr>\n\n", "<div>\n\n", "</div>\n\n", "<img src=\"a.png\">\n\n", "![](a)\n", "![](a) "}
	for _, unit := range units {
		for _, size := range []int{64 << 10, 512 << 10, maxImageScanBytes} {
			t.Run(fmt.Sprintf("%q at %d", unit, size), func(t *testing.T) {
				res := extractImages(context.Background(), costDocument(first, unit, nil, size), 128, maxAllocPerByte)
				if res.budgetReached || len(res.urls) != 1 || res.urls[0] != "https://img.example/first.png" {
					t.Fatalf("got %v, budget reached %v", res.urls, res.budgetReached)
				}
			})
		}
	}
}

// TestExtractImageURLsReserve: when a pass before the image scan spends
// its share of the budget, the image on the first line is still found
// and the result says the budget was reached.
func TestExtractImageURLsReserve(t *testing.T) {
	const first = "![a](https://img.example/first.png)\n\n"
	bodies := map[string]func(int) string{
		"code spans":  func(i int) string { return strings.Repeat("`a` ", i+1) + "\n" },
		"definitions": func(i int) string { return "[" + strings.Repeat("\xff", 300) + strconv.Itoa(i) + "]: x\n" },
		"lone tags":   func(i int) string { return "<img src=\"https://img.example/" + strconv.Itoa(i) + "&amp;\">\n\n" },
	}
	for name, gen := range bodies {
		for _, size := range []int{64 << 10, 512 << 10, maxImageScanBytes} {
			t.Run(fmt.Sprintf("%s at %d", name, size), func(t *testing.T) {
				// An allowance that leaves the earlier passes little room.
				res := extractImages(context.Background(), costDocument(first, "", gen, size), 1<<20, 2)
				if !res.budgetReached || len(res.urls) == 0 || res.urls[0] != "https://img.example/first.png" {
					t.Fatalf("got %d URLs starting %v, budget reached %v", len(res.urls), res.urls[:min(1, len(res.urls))], res.budgetReached)
				}
			})
		}
	}
}

// TestExtractImageURLsChargeSites: each step that allocates charges at
// least what it allocates. Each subtest's document makes one step's
// allocation dominate, so leaving that step's charge out fails it.
func TestExtractImageURLsChargeSites(t *testing.T) {
	const size = 512 << 10
	gen := func(f func(i int) string) string { return costDocument("", "", f, size) }
	sites := []struct{ name, md string }{
		{"visible buffer", strings.Repeat("plain text\n", size/11)},
		{"html blocks", strings.Repeat("<div>\n\n<br>\n\n", size/13)},
		{"code-span tables", strings.Repeat("` ", size/2)},
		// One destination repeated: it is parsed once, so its copies are
		// what each repeat allocates.
		{"markdown escapes", gen(func(i int) string { return "![](https://h/" + strings.Repeat("\\_", 700) + ")\n" })},
		{"character references", gen(func(i int) string { return "![](https://h/?" + strings.Repeat("&amp;", 300) + ")\n" })},
		{"URL parse", gen(func(i int) string { return "![](http:///" + strconv.Itoa(i) + ") " })},
		{"URL parse with user info", gen(func(i int) string { return "![](http://u@/" + strconv.Itoa(i) + ") " })},
		{"URL parse with rewritten bytes", gen(func(i int) string { return "![](https://h/" + strings.Repeat("!", 300) + strconv.Itoa(i) + ")\n" })},
		{"found URLs", gen(func(i int) string { return "![](https://h/" + strconv.Itoa(i) + ") " })},
		{"fragment with repeated #", gen(func(i int) string { return "![](https://h/#" + strings.Repeat("#", 300) + strconv.Itoa(i) + ")\n" })},
		{"invalid port", gen(func(i int) string { return "![](http://a:" + strings.Repeat("\xff", 300) + strconv.Itoa(i) + ")\n" })},
		{"invalid port after an IPv6 literal", gen(func(i int) string { return "![](http://[::1]:" + strings.Repeat("\xff", 300) + strconv.Itoa(i) + ")\n" })},
		{"invalid IPv6 literal", gen(func(i int) string { return "![](http://[" + strings.Repeat("\xff", 300) + strconv.Itoa(i) + "])\n" })},
		{"uppercase scheme", gen(func(i int) string { return "![](HTTPS:///" + strconv.Itoa(i) + ") " })},
		{"tag src decoding", gen(func(i int) string { return "<img src=\"https://h/?" + strings.Repeat("&amp;", 300) + "\"> " })},
		{"tag src backslashes", gen(func(i int) string { return "<img src=\"https://h/" + strings.Repeat("\\", 300) + "\"> " })},
		{"definition keys", gen(func(i int) string {
			return "[" + strings.Repeat("A", 900) + strconv.Itoa(i) + "]: https://h/" + strconv.Itoa(i) + "\n"
		})},
		{"definition entries", gen(func(i int) string { return "[d" + strconv.Itoa(i) + "]: http://u@h/" + strconv.Itoa(i) + "\n" })},
		{"label buffer", "[x]: https://h/x\n" + gen(func(i int) string { return "![" + strings.Repeat("A", i%maxLabelBytes+1) + "]\n" })},
		{"label lookups", "[x]: https://h/x\n" + gen(func(i int) string { return "![a\ta\tÉ\xff] " })},
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			// A large limit and allowance, so the step runs throughout.
			res, _, alloc := measureExtraction(site.md, 1<<20, 1<<12)
			if res.budgetReached {
				t.Fatal("the budget was reached")
			}
			if alloc > uint64(res.allocUsed)+allocSlack {
				t.Fatalf("allocated %d bytes, %d charged", alloc, res.allocUsed)
			}
		})
	}
}

// growthCharges is the charge for growing a slice of elem-byte elements,
// as extraction does (16 elements, then doubling), until it holds n.
func growthCharges(n, elem int) int {
	total := 0
	for c := 0; c < n; c = max(16, 2*c) {
		total += allocSize(elem * max(16, 2*c))
	}
	return total
}

// TestExtractImageURLsChargeAccounting: conservative charge accounting for
// small documents. The bytes charged are exactly the charges of the listed
// steps (a URL parse is charged its fixed bound; a definition is charged
// when stored, and its normalization at its first use), so leaving any
// charge out fails its subtest, and the bytes allocated (averaged over many
// runs) are no more than those charged.
func TestExtractImageURLsChargeAccounting(t *testing.T) {
	var many strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&many, "![](https://h/%d) ", i)
	}
	manyCost := 0
	for i := 0; i < 40; i++ {
		manyCost += urlParseCost("https://h/" + strconv.Itoa(i))
	}
	state := func(md string) int { return extractionCharge + allocSize(len(md)) }
	cases := []struct {
		name string
		md   string
		want func(md string) int
	}{
		{"extraction state", "x", state},
		{"found-URL set", "![](https://h/a)", func(md string) int {
			return state(md) + urlParseCost("https://h/a") + mapBaseCharge + seenEntryCharge + growthCharges(1, 24) + growthCharges(1, 16)
		}},
		{"definition map", "[r]: https://h/r\n", func(md string) int {
			return state(md) + mapBaseCharge + defEntryCharge + growthCharges(1, definitionSize)
		}},
		{"hit and result slices", many.String(), func(md string) int {
			return state(md) + manyCost + mapBaseCharge + 40*seenEntryCharge + growthCharges(40, 24) + growthCharges(40, 16)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const runs = 2000
			var before, after runtime.MemStats
			var used int
			runtime.GC()
			runtime.ReadMemStats(&before)
			for i := 0; i < runs; i++ {
				used = extractImages(context.Background(), c.md, 1<<20, maxAllocPerByte).allocUsed
			}
			runtime.ReadMemStats(&after)
			if want := c.want(c.md); used != want {
				t.Fatalf("charged %d bytes, want %d", used, want)
			}
			if alloc := (after.TotalAlloc - before.TotalAlloc) / runs; alloc > uint64(used) {
				t.Fatalf("allocated %d bytes per run, %d charged", alloc, used)
			}
		})
	}
}

// TestExtractionFixedCharges: each fixed charge covers the structure it
// stands for, measured: the extraction's state, a map's first table, and
// one entry of the found-URL set and of the definition map at any size
// up to the extraction limits.
func TestExtractionFixedCharges(t *testing.T) {
	if size := allocSize(int(unsafe.Sizeof(extraction{}))); size > extractionCharge {
		t.Errorf("extraction state is %d bytes, charge %d", size, extractionCharge)
	}
	if size := int(unsafe.Sizeof(definition{})); size != definitionSize {
		t.Errorf("a definition is %d bytes, charged as %d", size, definitionSize)
	}
	keys := make([]string, maxRefDefinitions)
	for i := range keys {
		keys[i] = "https://img.example/" + strconv.Itoa(i)
	}
	measure := func(n int, build func(n int) any) int {
		const runs = 20
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		var sink any
		for r := 0; r < runs; r++ {
			sink = build(n)
		}
		runtime.ReadMemStats(&after)
		_ = sink
		return int(after.TotalAlloc-before.TotalAlloc) / runs
	}
	seen := func(n int) any {
		m := map[string]bool{}
		for _, k := range keys[:n] {
			m[k] = true
		}
		return m
	}
	defs := func(n int) any {
		m := map[string]int32{}
		for i, k := range keys[:n] {
			m[k] = int32(i)
		}
		return m
	}
	for _, n := range []int{1, 8, 9, 64, 100, 1000, 2000, maxRefDefinitions} {
		if got, charge := measure(n, seen), mapBaseCharge+n*seenEntryCharge; got > charge {
			t.Errorf("found-URL set of %d: %d bytes, charge %d", n, got, charge)
		}
		if got, charge := measure(n, defs), mapBaseCharge+n*defEntryCharge; got > charge {
			t.Errorf("definition map of %d: %d bytes, charge %d", n, got, charge)
		}
		if n >= 9 {
			if got := (measure(n, seen) - measure(1, seen)) / (n - 1); got > seenEntryCharge {
				t.Errorf("found-URL set of %d: %d bytes per entry, charge %d", n, got, seenEntryCharge)
			}
			if got := (measure(n, defs) - measure(1, defs)) / (n - 1); got > defEntryCharge {
				t.Errorf("definition map of %d: %d bytes per entry, charge %d", n, got, defEntryCharge)
			}
		}
	}
}

// TestExtractImageURLsDefinitionReuse: a long definition used many times
// is normalized once, at its first use; later uses are label lookups and
// charge nothing.
func TestExtractImageURLsDefinitionReuse(t *testing.T) {
	def := "[r]: https://h/" + strings.Repeat("\\_", 900) + "?a=&amp;\n"
	for _, unit := range []string{"![a][r] ", "![a][r]\n", "![R] "} {
		t.Run(fmt.Sprintf("%q", unit), func(t *testing.T) {
			md := costDocument(def, unit, nil, 512<<10)
			res := extractImages(context.Background(), md, 128, maxAllocPerByte)
			want := "https://h/" + strings.Repeat("_", 900) + "?a=&"
			if res.budgetReached || len(res.urls) != 1 || res.urls[0] != want {
				t.Fatalf("got %d URLs, budget reached %v", len(res.urls), res.budgetReached)
			}
			// One normalization: its copies and parse, well under one
			// charge per use.
			if perUse := res.allocUsed - extractionCharge - allocSize(len(md)); perUse > 64<<10 {
				t.Fatalf("charged %d bytes beyond the buffer", perUse)
			}
		})
	}
}

// TestExtractImageURLsScanBound: images are taken from the first 2 MiB of
// the document, and the result says the document was longer.
func TestExtractImageURLsScanBound(t *testing.T) {
	filler := strings.Repeat("text text text text text text text\n", maxImageScanBytes/35+1)
	md := "![a](https://img.example/early.png)\n" + filler + "![b](https://img.example/late.png)\n"
	res := extractImages(context.Background(), md, 128, maxAllocPerByte)
	if len(res.urls) != 1 || res.urls[0] != "https://img.example/early.png" || !res.truncated {
		t.Fatalf("got %v, truncated %v", res.urls, res.truncated)
	}
	if maxImageScanBytes != 2<<20 {
		t.Fatalf("the publish warning names 2 MiB; the window is %d bytes", maxImageScanBytes)
	}
}

// TestPublishWarnsBeyondWindow: a markdown entry larger than the scanned
// window publishes with one warning saying later images were not fetched.
func TestPublishWarnsBeyondWindow(t *testing.T) {
	f := newFixture(t, false)
	f.useFetcher(&fakeFetcher{bodies: map[string][]byte{"https://img.example/a.png": testPNG}})
	md := "![a](https://img.example/a.png)\n" + strings.Repeat("text\n", maxImageScanBytes/5+1) + "![b](https://img.example/b.png)\n"
	resp := f.publish(agentA, "doc.md", []byte(md), "")
	if len(resp.Warnings) != 1 || resp.Warnings[0] != warnBeyondWindow {
		t.Fatalf("warnings %q", resp.Warnings)
	}
	if resp.Version.FileCount != 2 {
		t.Fatalf("files %d: the image past the window must have no row", resp.Version.FileCount)
	}
}

// TestPublishWarnsWhenBudgetReached: a markdown entry whose extraction
// reaches the allocation budget publishes with one warning saying some
// images were not fetched.
func TestPublishWarnsWhenBudgetReached(t *testing.T) {
	f := newFixture(t, false)
	f.useFetcher(&fakeFetcher{bodies: map[string][]byte{"https://img.example/a.png": testPNG}})
	md := "![a](https://img.example/a.png)\n" + costDocument("", "", func(i int) string { return "![x](http:///" + strconv.Itoa(i) + ") " }, 1<<20)
	resp := f.publish(agentA, "doc.md", []byte(md), "")
	if len(resp.Warnings) != 1 || resp.Warnings[0] != warnScanStopped {
		t.Fatalf("warnings %q", resp.Warnings)
	}
	if resp.Version.FileCount != 2 {
		t.Fatalf("files %d", resp.Version.FileCount)
	}
}

// TestPublishWarnsOnceBeyondWindowAndBudget: an entry larger than the
// window whose extraction also reaches the budget gets one warning, the
// window one.
func TestPublishWarnsOnceBeyondWindowAndBudget(t *testing.T) {
	f := newFixture(t, false)
	f.useFetcher(&fakeFetcher{bodies: map[string][]byte{"https://img.example/a.png": testPNG}})
	md := "![a](https://img.example/a.png)\n" + costDocument("", "", func(i int) string { return "![x](http:///" + strconv.Itoa(i) + ") " }, maxImageScanBytes+1<<20)
	resp := f.publish(agentA, "doc.md", []byte(md), "")
	if len(resp.Warnings) != 1 || resp.Warnings[0] != warnBeyondWindow {
		t.Fatalf("warnings %q", resp.Warnings)
	}
}

// TestExtractImageURLsTagSrcAsBrowser: an <img> src yields the URL the
// browser requests: character references decoded once, no markdown
// escapes, tabs and newlines removed, and a backslash before the query or
// fragment read as '/'.
func TestExtractImageURLsTagSrcAsBrowser(t *testing.T) {
	for src, want := range map[string]string{
		`https://h/a?b=&amp;amp;c`:  "https://h/a?b=&amp;c",
		`https://h/&amp;lt;x.png`:   "https://h/&lt;x.png",
		`https://h/a\_b.png`:        "https://h/a/_b.png",
		`https://h/a\b.png?q=\x#\y`: `https://h/a/b.png?q=\x#\y`,
		"https://h/a\tb.png":        "https://h/ab.png",
		`https://h/x&#46;png`:       "https://h/x.png",
	} {
		md := "x <img src=\"" + src + "\">\n\n<img src=\"" + src + "\">\n"
		if got := extractImageURLs(context.Background(), md, 8); len(got) != 1 || got[0] != want {
			t.Errorf("src %q: got %q, want %q", src, got, want)
		}
	}
	// Markdown destinations keep their own rules: backslash escapes are
	// removed and character references are decoded once.
	if got := extractImageURLs(context.Background(), "![a](https://h/a\\_b.png?x=&amp;amp;y)", 8); len(got) != 1 || got[0] != "https://h/a_b.png?x=&amp;y" {
		t.Errorf("markdown destination: got %q", got)
	}
}

// TestRemoteExtractLimit: extraction reads exactly four times the fetch
// cap of distinct URLs, no more and no fewer.
func TestRemoteExtractLimit(t *testing.T) {
	lim := RemoteImageLimits{MaxCount: 32}
	if got := remoteExtractLimit(lim); got != 128 {
		t.Fatalf("remoteExtractLimit = %d, want 128", got)
	}
	for _, n := range []int{127, 128, 129, 200} {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "![](https://img.example/%d.png)\n", i)
		}
		got := extractImageURLs(context.Background(), b.String(), remoteExtractLimit(lim))
		if want := min(n, 128); len(got) != want {
			t.Errorf("%d distinct URLs: extracted %d, want %d", n, len(got), want)
		}
	}
}

// TestParseTag covers the hand-written tag parser used for <img src>.
func TestParseTag(t *testing.T) {
	for in, want := range map[string]string{
		`<img src="https://a.example/x.png">`:             "https://a.example/x.png",
		`<IMG alt='a b' SRC='https://a.example/y.png' />`: "https://a.example/y.png",
		`<img src=https://a.example/z.png>`:               "https://a.example/z.png",
		`<img src="https://a.example/q?a=1&amp;b=2">`:     "https://a.example/q?a=1&b=2",
		`<img data-src="x" src = "s">`:                    "s",
		`<img alt="x">`:                                   "",
		`<img src="unterminated>`:                         "",
		`<imgx src="a">`:                                  "",
	} {
		if got := imgSrc(in); got != want {
			t.Errorf("imgSrc(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string][2]bool{
		`<img src="x">`:      {true, true},
		`</div>`:             {true, false},
		`<span>`:             {true, false},
		`<img src="x"> tail`: {false, false},
		`<script>`:           {false, false},
	} {
		lone, isImg := loneTag(in)
		if lone != want[0] || isImg != want[1] {
			t.Errorf("loneTag(%q) = %v, %v, want %v", in, lone, isImg, want)
		}
	}
}

// TestExtractImageURLsStopsWhenCancelled: a cancelled context ends
// extraction with no result.
func TestExtractImageURLsStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := extractImageURLs(ctx, "![a](https://img.example/a.png)", 10); got != nil {
		t.Fatalf("got %v from a cancelled context", got)
	}
}

// TestPublishExtendsWriteDeadline: fetching runs inside the publish
// request, so the handler extends its write deadline past the fetch budget;
// a server write timeout shorter than the fetch does not cut the response.
func TestPublishExtendsWriteDeadline(t *testing.T) {
	f := newFixture(t, false)
	f.useFetcher(&fakeFetcher{bodies: map[string][]byte{"https://img.example/a.png": testPNG}, delay: 600 * time.Millisecond})
	f.svc.SetLimits(func(context.Context) Limits {
		return Limits{MaxFileBytes: 1 << 20, RemoteImages: RemoteImageLimits{
			Enabled: true, MaxCount: 4, MaxBytes: 1 << 20, FetchTimeout: 2 * time.Second, TotalBudget: 2 * time.Second,
		}}
	})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.svc.ServeHTTP(w, withPrincipal(r, agentA))
	}))
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/v1/artifacts?name=doc.md", "text/markdown", strings.NewReader("![a](https://img.example/a.png)"))
	if err != nil {
		t.Fatalf("publish: %v (the response was cut off)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestFetchFloor: refused and unresolvable image fetches complete no
// sooner than the fetch floor, once per publish however many there are;
// a publish whose fetches all succeed is not held.
func TestFetchFloor(t *testing.T) {
	const floor = 300 * time.Millisecond
	good := "https://img.example/good.png"
	publishWith := func(t *testing.T, md string, reasons map[string]remotefetch.Reason) time.Duration {
		t.Helper()
		f := newFixture(t, false)
		f.svc.fetchFloor = floor
		f.useFetcher(&fakeFetcher{bodies: map[string][]byte{good: testPNG}, reasons: reasons})
		start := time.Now()
		f.publish(agentA, "doc.md", []byte(md), "")
		return time.Since(start)
	}
	t.Run("refused", func(t *testing.T) {
		u := "https://refused.example/a.png"
		if d := publishWith(t, "![a]("+u+")", map[string]remotefetch.Reason{u: remotefetch.ReasonDeniedAddress}); d < floor {
			t.Fatalf("took %v, want at least %v", d, floor)
		}
	})
	t.Run("unresolvable", func(t *testing.T) {
		u := "https://unresolvable.example/a.png"
		if d := publishWith(t, "![a]("+u+")", map[string]remotefetch.Reason{u: remotefetch.ReasonResolve}); d < floor {
			t.Fatalf("took %v, want at least %v", d, floor)
		}
	})
	t.Run("many refused cost one floor", func(t *testing.T) {
		var md strings.Builder
		reasons := map[string]remotefetch.Reason{}
		for i := 0; i < 50; i++ {
			u := fmt.Sprintf("https://refused.example/%d.png", i)
			reasons[u] = remotefetch.ReasonDeniedAddress
			fmt.Fprintf(&md, "![a](%s)\n", u)
		}
		if d := publishWith(t, md.String(), reasons); d < floor || d > 3*floor {
			t.Fatalf("took %v, want about one floor (%v)", d, floor)
		}
	})
	t.Run("all fetched", func(t *testing.T) {
		if d := publishWith(t, "![a]("+good+")", nil); d >= floor {
			t.Fatalf("took %v, want well under the floor %v", d, floor)
		}
	})
	t.Run("floor stays within the fetch budget", func(t *testing.T) {
		u := "https://refused.example/a.png"
		f := newFixture(t, false)
		f.svc.fetchFloor = 5 * time.Second
		f.useFetcher(&fakeFetcher{reasons: map[string]remotefetch.Reason{u: remotefetch.ReasonDeniedAddress}})
		f.svc.SetLimits(func(context.Context) Limits {
			return Limits{MaxFileBytes: 1 << 20, RemoteImages: RemoteImageLimits{
				Enabled: true, MaxCount: 4, MaxBytes: 1 << 20, FetchTimeout: 200 * time.Millisecond, TotalBudget: 200 * time.Millisecond,
			}}
		})
		start := time.Now()
		f.publish(agentA, "doc.md", []byte("![a]("+u+")"), "")
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %v, want the floor cut short by the 200ms budget", d)
		}
	})
	t.Run("refused by the production fetcher", func(t *testing.T) {
		// The real fetcher with its production address rules: an IP
		// literal in a denied range is refused before any connection (no
		// DNS involved), and the publish is held for the floor.
		f := newFixture(t, false)
		f.svc.fetchFloor = floor
		f.svc.setImageFetcherFactory(defaultFetcherFactory)
		start := time.Now()
		resp := f.publish(agentA, "doc.md", []byte("![a](https://169.254.169.254/latest/meta-data/)"), "")
		if d := time.Since(start); d < floor {
			t.Fatalf("took %v, want at least %v", d, floor)
		}
		if len(resp.Warnings) != 1 {
			t.Fatalf("warnings %q", resp.Warnings)
		}
	})
	t.Run("missing image is not held", func(t *testing.T) {
		u := "https://img.example/missing.png"
		if d := publishWith(t, "![a]("+u+")", map[string]remotefetch.Reason{u: remotefetch.ReasonStatus}); d >= floor {
			t.Fatalf("took %v; only refused and unresolvable fetches are held", d)
		}
	})
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
	for _, internal := range []string{"denied", "10.0.0.5", "bad_status", "detail"} {
		if strings.Contains(rec.Body.String(), internal) {
			t.Fatalf("metadata shows server-side text %q: %s", internal, rec.Body.String())
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
	// On the current-version route the same image is not cached as
	// immutable: that route can point at another version later.
	current := "/api/v1/artifacts/" + resp.Artifact.ID + "/files/" + RemotePath(good) + "?stream=1"
	if rec := f.do(&agentA, http.MethodGet, current, nil, nil); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("current route: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
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
// plain 404 even for a failed image, so the status header says nothing
// about an artifact the caller cannot read.
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
