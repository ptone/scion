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

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/agent/tmuxdeliver"
)

// fakeClock is a manual clock; Sleep advances it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) bool {
	c.Advance(d)
	return ctx.Err() == nil
}

// runCall is one runner call: argv and the stdin it carried.
type runCall struct {
	argv  []string
	stdin string
}

// fakeRunner records calls. The readiness probe answers probe(); a
// tmux verb in fail fails; hook, when set, runs before each call.
type fakeRunner struct {
	mu    sync.Mutex
	calls []runCall
	fail  map[string]error
	probe func() string
	hook  func(argv []string)
}

func (f *fakeRunner) Run(_ context.Context, argv []string, stdin io.Reader) (string, error) {
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	f.mu.Lock()
	f.calls = append(f.calls, runCall{argv: slices.Clone(argv), stdin: in})
	hook, fail, probe := f.hook, f.fail, f.probe
	f.mu.Unlock()
	if hook != nil {
		hook(argv)
	}
	v := verb(argv)
	if err := fail[v]; err != nil {
		return "", err
	}
	if v == "probe" {
		if probe == nil {
			return "", errors.New("no probe")
		}
		return probe(), nil
	}
	return "", nil
}

func verb(argv []string) string {
	if slices.Equal(argv, tmuxdeliver.ProbeArgv()) {
		return "probe"
	}
	if len(argv) >= 2 && argv[0] == "tmux" {
		return argv[1]
	}
	return "?"
}

func (f *fakeRunner) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, verb(c.argv))
	}
	return out
}

func (f *fakeRunner) count(v string) int {
	n := 0
	for _, got := range f.verbs() {
		if got == v {
			n++
		}
	}
	return n
}

func (f *fakeRunner) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// capturedLog records the route's log lines by level.
type capturedLog struct {
	mu                sync.Mutex
	info, warn, debug []string
}

func (c *capturedLog) logger() deliverLogger {
	rec := func(dst *[]string) func(string, ...any) {
		return func(format string, args ...any) {
			c.mu.Lock()
			*dst = append(*dst, fmt.Sprintf(format, args...))
			c.mu.Unlock()
		}
	}
	return deliverLogger{info: rec(&c.info), warn: rec(&c.warn), debug: rec(&c.debug)}
}

type harness struct {
	d     *deliverer
	h     *Handler
	run   *fakeRunner
	clock *fakeClock
	logs  *capturedLog
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := newFakeClock()
	run := &fakeRunner{}
	h := New(Options{KickTokenRefresh: func() {}, Deliver: &DeliverOptions{Runner: run, Now: clock.Now, Sleep: clock.Sleep}})
	d := h.deliver
	logs := &capturedLog{}
	d.logf = logs.logger()
	return &harness{d: d, h: h, run: run, clock: clock, logs: logs}
}

// body builds a request; mutate edits the map before it is encoded.
func body(mutate func(m map[string]any)) string {
	m := map[string]any{
		"v":           1,
		"delivery_id": "d-1",
		"broker_id":   "broker-1",
		"message_ids": []string{"m-1"},
		"op":          "paste",
		"text":        "hello",
		"deadline_ms": 92000,
		"submit_keys": []string{"Enter"},
	}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

type reply struct {
	status int
	resp   deliverResponse
	bad    badRequestResponse
	raw    string
}

func (h *harness) post(t *testing.T, b string) reply {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, Prefix+RouteDeliverMessage, strings.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	h.h.ServeHTTP(rec, req)
	r := reply{status: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &r.resp)
	_ = json.Unmarshal(rec.Body.Bytes(), &r.bad)
	return r
}

const testNonce = "0123456789abcdef0123456789abcdef"

func probeOut(session, now, initrun, ready, readymtime string) string {
	return fmt.Sprintf("session=%s\nnow=%s\ninitrun=%s\ninitspec=\nready=%s\nreadymtime=%s\n",
		session, now, initrun, ready, readymtime)
}

var (
	hookRun       = "nonce=" + testNonce + " mode=" + state.ReadyModeHook
	probeHookIdle = probeOut("900", "1000", hookRun, "", "")
	probeHookDone = probeOut("900", "1000", hookRun, "nonce="+testNonce, "990")
)

// TestDeliverValidation: every schema or bounds violation answers 400
// with the field name and runs nothing (design r3 §1.2, §1.7, §5.3).
func TestDeliverValidation(t *testing.T) {
	long := strings.Repeat("a", MaxDeliverText+1)
	ids65 := make([]string, 65)
	for i := range ids65 {
		ids65[i] = fmt.Sprintf("m-%d", i)
	}
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		field  string
	}{
		{"unknown field", func(m map[string]any) { m["extra"] = 1 }, ""},
		{"unknown nested field", func(m map[string]any) { m["extra_submits"] = map[string]any{"count": 1, "delay_ms": 1, "x": 1} }, ""},
		{"v missing", func(m map[string]any) { delete(m, "v") }, "v"},
		{"v 2", func(m map[string]any) { m["v"] = 2 }, "v"},
		{"delivery_id bad char", func(m map[string]any) { m["delivery_id"] = "a_b" }, "delivery_id"},
		{"delivery_id 65", func(m map[string]any) { m["delivery_id"] = strings.Repeat("a", 65) }, "delivery_id"},
		{"broker_id missing", func(m map[string]any) { delete(m, "broker_id") }, "broker_id"},
		{"message_ids empty", func(m map[string]any) { m["message_ids"] = []string{} }, "message_ids"},
		{"message_ids 65", func(m map[string]any) { m["message_ids"] = ids65 }, "message_ids"},
		{"message_id bad", func(m map[string]any) { m["message_ids"] = []string{"m 1"} }, "message_ids"},
		{"op unknown", func(m map[string]any) { m["op"] = "raw" }, "op"},
		{"paste without text", func(m map[string]any) { delete(m, "text") }, "text"},
		{"paste with empty text", func(m map[string]any) { m["text"] = "" }, "text"},
		{"submit_only with text", func(m map[string]any) { m["op"] = "submit_only" }, "text"},
		{"text over 512 KiB", func(m map[string]any) { m["text"] = long }, "text"},
		{"interrupt op without keys", func(m map[string]any) { m["op"] = "interrupt_paste" }, "interrupt_keys"},
		{"interrupt keys on paste", func(m map[string]any) { m["interrupt_keys"] = []string{"C-c"} }, "interrupt_keys"},
		{"5 interrupt keys", func(m map[string]any) {
			m["op"] = "interrupt_paste"
			m["interrupt_keys"] = []string{"C-c", "C-c", "C-c", "C-c", "C-c"}
		}, "interrupt_keys"},
		{"interrupt key -l", func(m map[string]any) { m["op"] = "interrupt_paste"; m["interrupt_keys"] = []string{"-l"} }, "interrupt_keys"},
		{"submit key literal string", func(m map[string]any) { m["submit_keys"] = []string{"echo hi"} }, "submit_keys"},
		{"submit key -l", func(m map[string]any) { m["submit_keys"] = []string{"-l"} }, "submit_keys"},
		{"no submit keys", func(m map[string]any) { m["submit_keys"] = []string{} }, "submit_keys"},
		{"5 submit keys", func(m map[string]any) { m["submit_keys"] = []string{"Enter", "Enter", "Enter", "Enter", "Enter"} }, "submit_keys"},
		{"deadline missing", func(m map[string]any) { delete(m, "deadline_ms") }, "deadline_ms"},
		{"deadline 0", func(m map[string]any) { m["deadline_ms"] = 0 }, "deadline_ms"},
		{"deadline 92001", func(m map[string]any) { m["deadline_ms"] = 92001 }, "deadline_ms"},
		{"not_after negative", func(m map[string]any) { m["not_after_unix_ms"] = -1 }, "not_after_unix_ms"},
		{"extra submits 4", func(m map[string]any) { m["extra_submits"] = map[string]any{"count": 4, "delay_ms": 300} }, "extra_submits.count"},
		{"extra delay 2001", func(m map[string]any) { m["extra_submits"] = map[string]any{"count": 1, "delay_ms": 2001} }, "extra_submits.delay_ms"},
		{"ready strategy", func(m map[string]any) { m["ready"] = map[string]any{"strategy": "poll"} }, "ready.strategy"},
		{"ready settle 10001", func(m map[string]any) { m["ready"] = map[string]any{"strategy": "hook", "settle_ms": 10001} }, "ready.settle_ms"},
		{"ready timeout 90001", func(m map[string]any) { m["ready"] = map[string]any{"strategy": "hook", "timeout_ms": 90001} }, "ready.timeout_ms"},
		{"ready nonce bad", func(m map[string]any) { m["ready"] = map[string]any{"strategy": "hook", "nonce": "a/b"} }, "ready.nonce"},
		{"ready on interrupt op", func(m map[string]any) {
			m["op"] = "interrupt_paste"
			m["interrupt_keys"] = []string{"C-c"}
			m["ready"] = map[string]any{"strategy": "hook"}
		}, "ready"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			r := h.post(t, body(tt.mutate))
			if r.status != http.StatusBadRequest {
				t.Fatalf("status %d (%s), want 400", r.status, r.raw)
			}
			if r.bad.Field != tt.field {
				t.Fatalf("field %q, want %q", r.bad.Field, tt.field)
			}
			if n := len(h.run.verbs()); n != 0 {
				t.Fatalf("runner called %d times: %v", n, h.run.verbs())
			}
		})
	}
}

// TestDeliverValidation_Controls: the in-bounds edge of each bound is
// accepted, so the refusals above are about the bound.
func TestDeliverValidation_Controls(t *testing.T) {
	h := newHarness(t)
	ids64 := make([]string, 64)
	for i := range ids64 {
		ids64[i] = fmt.Sprintf("m-%d", i)
	}
	r := h.post(t, body(func(m map[string]any) {
		m["delivery_id"] = strings.Repeat("a", 64)
		m["message_ids"] = ids64
		m["text"] = strings.Repeat("a", MaxDeliverText)
		m["submit_keys"] = []string{"Enter", "Enter", "Enter", "Enter"}
		m["extra_submits"] = map[string]any{"count": 3, "delay_ms": 2000}
		m["ready"] = map[string]any{"strategy": "delay", "settle_ms": 10000, "timeout_ms": 90000, "nonce": testNonce}
	}))
	if r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered {
		t.Fatalf("status %d %s, want 200 delivered", r.status, r.raw)
	}
}

// TestDeliverMalformedAndOversize: a body that is not one JSON object is
// 400; a body over the session's RPC cap is 413.
func TestDeliverMalformedAndOversize(t *testing.T) {
	h := newHarness(t)
	for _, b := range []string{"", "{", "[]", body(nil) + body(nil), body(nil) + " x"} {
		if r := h.post(t, b); r.status != http.StatusBadRequest {
			t.Fatalf("body %.40q: status %d, want 400", b, r.status)
		}
	}
	big := `{"text":"` + strings.Repeat("a", 800<<10) + `"}`
	if r := h.post(t, big); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body: status %d, want 413", r.status)
	}
}

// TestDeliverPaste_StepsAndStdin: a paste checks the target, loads the
// text through stdin into the delivery's buffer (never argv), pastes it
// bracketed into the fixed target, submits, then runs the extra submits.
func TestDeliverPaste_StepsAndStdin(t *testing.T) {
	h := newHarness(t)
	const text = "SENTINEL-TEXT-7f3a"
	r := h.post(t, body(func(m map[string]any) {
		m["text"] = text
		m["extra_submits"] = map[string]any{"count": 2, "delay_ms": 300}
	}))
	if r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered || r.resp.Replayed == nil || *r.resp.Replayed {
		t.Fatalf("got %d %s, want 200 delivered replayed:false", r.status, r.raw)
	}
	want := [][]string{
		{"tmux", "has-session", "-t", tmuxdeliver.Target},
		tmuxdeliver.LoadBufferArgv("scion-msg-d-1-1"),
		tmuxdeliver.PasteBufferArgv("scion-msg-d-1-1"),
		tmuxdeliver.SendKeyArgv("Enter"),
		tmuxdeliver.SendKeyArgv("Enter"),
		tmuxdeliver.SendKeyArgv("Enter"),
	}
	calls := h.run.calls
	if len(calls) != len(want) {
		t.Fatalf("calls %v, want %v", h.run.verbs(), want)
	}
	for i := range want {
		if !slices.Equal(calls[i].argv, want[i]) {
			t.Fatalf("call %d argv %q, want %q", i, calls[i].argv, want[i])
		}
		if strings.Contains(strings.Join(calls[i].argv, " "), text) {
			t.Fatalf("call %d argv carries the text", i)
		}
	}
	if calls[1].stdin != text {
		t.Fatalf("load-buffer stdin %q, want the text", calls[1].stdin)
	}
	for i, c := range calls {
		if i != 1 && c.stdin != "" {
			t.Fatalf("call %d got stdin", i)
		}
	}
}

// TestDeliverOps: the other three ops run the step list deliverImmediate
// does today: submit_only submits once with no extras, interrupt_submit
// sends the keys then submits once, interrupt_paste sends the keys then
// pastes and submits with extras; none waits for readiness.
func TestDeliverOps(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		want   []string
	}{
		{"submit_only", func(m map[string]any) {
			m["op"] = "submit_only"
			delete(m, "text")
		}, []string{"has-session", "send-keys"}},
		{"interrupt_submit", func(m map[string]any) {
			m["op"] = "interrupt_submit"
			delete(m, "text")
			m["interrupt_keys"] = []string{"Escape", "C-c"}
		}, []string{"has-session", "send-keys", "send-keys", "send-keys"}},
		{"interrupt_paste", func(m map[string]any) {
			m["op"] = "interrupt_paste"
			m["interrupt_keys"] = []string{"C-c"}
		}, []string{"has-session", "send-keys", "load-buffer", "paste-buffer", "send-keys", "send-keys"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			r := h.post(t, body(func(m map[string]any) {
				m["extra_submits"] = map[string]any{"count": 1, "delay_ms": 300}
				tt.mutate(m)
			}))
			if r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered {
				t.Fatalf("got %d %s", r.status, r.raw)
			}
			if got := h.run.verbs(); !slices.Equal(got, tt.want) {
				t.Fatalf("steps %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDeliverIdempotency: a replay of a finished key answers the stored
// outcome with replayed:true and runs nothing; the same broker with
// another delivery id, or another broker with the same id, is a new key
// (design r3 §5.5).
func TestDeliverIdempotency(t *testing.T) {
	h := newHarness(t)
	if r := h.post(t, body(nil)); r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered {
		t.Fatalf("first: %d %s", r.status, r.raw)
	}
	h.run.reset()
	r := h.post(t, body(nil))
	if r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered || r.resp.Replayed == nil || !*r.resp.Replayed {
		t.Fatalf("replay: %d %s, want 200 delivered replayed:true", r.status, r.raw)
	}
	if n := len(h.run.verbs()); n != 0 {
		t.Fatalf("replay ran %d steps", n)
	}
	if r := h.post(t, body(func(m map[string]any) { m["broker_id"] = "broker-2" })); r.resp.Replayed == nil || *r.resp.Replayed {
		t.Fatalf("another broker, same id: %s, want a fresh delivery", r.raw)
	}
	if r := h.post(t, body(func(m map[string]any) { m["delivery_id"] = "d-2" })); r.resp.Replayed == nil || *r.resp.Replayed {
		t.Fatalf("same broker, another id: %s, want a fresh delivery", r.raw)
	}
}

// TestDeliverConcurrency_BusyAndInProgress: while a flush runs, the same
// key gets 409 in_progress and another key gets 409 busy, and neither
// runs a step (design r3 §1.3, R3).
func TestDeliverConcurrency_BusyAndInProgress(t *testing.T) {
	h := newHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	h.run.hook = func(argv []string) {
		if verb(argv) == "load-buffer" {
			close(entered)
			<-release
		}
	}
	first := make(chan reply, 1)
	go func() { first <- h.post(t, body(nil)) }()
	<-entered
	steps := len(h.run.verbs())

	if r := h.post(t, body(nil)); r.status != http.StatusConflict || r.resp.Outcome != outcomeInProgress {
		t.Fatalf("same key: %d %s, want 409 in_progress", r.status, r.raw)
	}
	if r := h.post(t, body(func(m map[string]any) { m["delivery_id"] = "d-2" })); r.status != http.StatusConflict || r.resp.Outcome != outcomeBusy {
		t.Fatalf("other key: %d %s, want 409 busy", r.status, r.raw)
	}
	if got := len(h.run.verbs()); got != steps {
		t.Fatalf("409s ran %d steps", got-steps)
	}
	h.run.mu.Lock()
	h.run.hook = nil
	h.run.mu.Unlock()
	close(release)
	if r := <-first; r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered {
		t.Fatalf("first: %d %s", r.status, r.raw)
	}
	if r := h.post(t, body(func(m map[string]any) { m["delivery_id"] = "d-2" })); r.status != http.StatusOK {
		t.Fatalf("after the flush, other key: %d %s, want 200", r.status, r.raw)
	}
}

// TestDeliverPartial: a failure after the paste answers 200 partial with
// the stage, and is stored, so a replay never pastes again.
func TestDeliverPartial(t *testing.T) {
	t.Run("submit", func(t *testing.T) {
		h := newHarness(t)
		h.run.fail = map[string]error{"send-keys": errors.New("boom")}
		r := h.post(t, body(nil))
		if r.status != http.StatusOK || r.resp.Outcome != outcomePartial || r.resp.Stage != tmuxdeliver.StageSubmit {
			t.Fatalf("got %d %s, want 200 partial submit", r.status, r.raw)
		}
		h.run.fail = nil
		h.run.reset()
		r = h.post(t, body(nil))
		if r.resp.Outcome != outcomePartial || r.resp.Replayed == nil || !*r.resp.Replayed || h.run.count("paste-buffer") != 0 {
			t.Fatalf("replay: %s, steps %v; want the stored partial and no paste", r.raw, h.run.verbs())
		}
	})
	t.Run("extra_submit", func(t *testing.T) {
		h := newHarness(t)
		n := 0
		h.run.hook = func(argv []string) {
			if verb(argv) == "send-keys" {
				n++
				if n == 2 {
					h.run.fail = map[string]error{"send-keys": errors.New("boom")}
				}
			}
		}
		r := h.post(t, body(func(m map[string]any) { m["extra_submits"] = map[string]any{"count": 2, "delay_ms": 1} }))
		if r.status != http.StatusOK || r.resp.Outcome != outcomePartial || r.resp.Stage != tmuxdeliver.StageExtraSubmit {
			t.Fatalf("got %d %s, want 200 partial extra_submit", r.status, r.raw)
		}
	})
}

// TestDeliverFailedBeforePaste: a load-buffer failure is a 500 with a
// fixed body; it is stored (a step was attempted), so a replay does not
// run again.
func TestDeliverFailedBeforePaste(t *testing.T) {
	h := newHarness(t)
	h.run.fail = map[string]error{"load-buffer": errors.New("boom SENTINEL-ERR")}
	r := h.post(t, body(nil))
	if r.status != http.StatusInternalServerError || r.resp.Outcome != outcomeFailed || strings.Contains(r.raw, "SENTINEL") {
		t.Fatalf("got %d %s", r.status, r.raw)
	}
	h.run.fail = nil
	h.run.reset()
	if r := h.post(t, body(nil)); r.resp.Replayed == nil || !*r.resp.Replayed || len(h.run.verbs()) != 0 {
		t.Fatalf("replay: %s steps %v", r.raw, h.run.verbs())
	}
}

// TestDeliverTargetMissing: no tmux target answers 422 stage target,
// runs no acting step, and is not stored, so a retry runs (r3 §1.3).
func TestDeliverTargetMissing(t *testing.T) {
	h := newHarness(t)
	h.run.fail = map[string]error{"has-session": errors.New("no session")}
	r := h.post(t, body(nil))
	if r.status != http.StatusUnprocessableEntity || r.resp.Stage != stageTarget {
		t.Fatalf("got %d %s, want 422 target", r.status, r.raw)
	}
	if got := h.run.verbs(); !slices.Equal(got, []string{"has-session"}) {
		t.Fatalf("steps %v", got)
	}
	h.run.fail = nil
	if r := h.post(t, body(nil)); r.status != http.StatusOK || r.resp.Replayed == nil || *r.resp.Replayed {
		t.Fatalf("retry: %d %s, want a fresh 200", r.status, r.raw)
	}
}

// TestDeliverReadiness: op=paste waits with the harness ready block.
// Hook mode settles once the marker appears; a harness never ready is
// pasted anyway after the readiness timeout (r3 §3.1, §6.4); the
// interrupt ops never probe (covered by TestDeliverOps).
func TestDeliverReadiness(t *testing.T) {
	t.Run("hook settles then pastes", func(t *testing.T) {
		h := newHarness(t)
		probes := 0
		h.run.probe = func() string {
			probes++
			if probes < 3 {
				return probeHookIdle
			}
			return probeHookDone
		}
		start := h.clock.Now()
		r := h.post(t, body(func(m map[string]any) {
			m["ready"] = map[string]any{"strategy": "hook", "settle_ms": 1500, "timeout_ms": 90000}
		}))
		if r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered {
			t.Fatalf("got %d %s", r.status, r.raw)
		}
		if probes != 3 || h.run.count("paste-buffer") != 1 {
			t.Fatalf("probes %d, steps %v", probes, h.run.verbs())
		}
		if waited := h.clock.Now().Sub(start); waited < 1500*time.Millisecond {
			t.Fatalf("waited %s, want at least the settle", waited)
		}
	})
	t.Run("delay strategy settle mode", func(t *testing.T) {
		h := newHarness(t)
		h.run.probe = func() string {
			return probeOut("900", "1000", "nonce="+testNonce+" mode="+state.ReadyModeSettle, "", "")
		}
		r := h.post(t, body(func(m map[string]any) { m["ready"] = map[string]any{"strategy": "delay", "settle_ms": 8000} }))
		if r.status != http.StatusOK || h.run.count("probe") != 1 || h.run.count("paste-buffer") != 1 {
			t.Fatalf("got %d %s steps %v", r.status, r.raw, h.run.verbs())
		}
	})
	t.Run("deadline then paste", func(t *testing.T) {
		h := newHarness(t)
		h.run.probe = func() string { return probeHookIdle }
		start := h.clock.Now()
		r := h.post(t, body(func(m map[string]any) { m["ready"] = map[string]any{"strategy": "hook", "timeout_ms": 90000} }))
		if r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered || h.run.count("paste-buffer") != 1 {
			t.Fatalf("got %d %s steps %v", r.status, r.raw, h.run.verbs())
		}
		if waited := h.clock.Now().Sub(start); waited < 90*time.Second || waited >= 92*time.Second {
			t.Fatalf("waited %s, want the 90 s readiness timeout, before the 92 s hard stop", waited)
		}
	})
	t.Run("no ready block, no probe", func(t *testing.T) {
		h := newHarness(t)
		if r := h.post(t, body(nil)); r.status != http.StatusOK || h.run.count("probe") != 0 {
			t.Fatalf("got %d steps %v", r.status, h.run.verbs())
		}
	})
}

// notAfter returns the hub's not_after for a hub clock offset from the
// agent's: hub send time + 92 s, with send at the agent's receipt.
func notAfter(agentNow time.Time, agentAhead time.Duration, plus time.Duration) int64 {
	return agentNow.Add(-agentAhead).Add(plus).UnixMilli()
}

// TestHardStop_EarlierOf: the hard stop is the earlier of receipt +
// deadline_ms and not_after_unix_ms (r3 §1.2 line 45, §6.3 line 350).
func TestHardStop_EarlierOf(t *testing.T) {
	receipt := newFakeClock().Now()
	ms := func(d time.Duration) *int64 { v := receipt.Add(d).UnixMilli(); return &v }
	cases := []struct {
		name   string
		na     *int64
		want   time.Duration
		skewed bool
	}{
		{"no not_after", nil, 92 * time.Second, false},
		{"not_after later than receipt bound", ms(100 * time.Second), 92 * time.Second, false},
		{"not_after at receipt + 70 s is used (r3 §6.3 line 351)", ms(70 * time.Second), 70 * time.Second, false},
		{"not_after at receipt + 62 s is used (the r3 §2.2 line 159 cut-off)", ms(62 * time.Second), 62 * time.Second, false},
		{"not_after at receipt + 61 s is skew (below receipt + 62 s, r3 §6.3 line 351)", ms(61 * time.Second), 92 * time.Second, true},
		{"not_after at receipt is skew (already past, r3 §2.2 line 159)", ms(0), 92 * time.Second, true},
		{"not_after in the past is skew", ms(-time.Second), 92 * time.Second, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			stop, skewed := hardStop(receipt, 92*time.Second, tt.na)
			if got := stop.Sub(receipt); got != tt.want || skewed != tt.skewed {
				t.Fatalf("stop receipt+%s skewed %v, want receipt+%s skewed %v", got, skewed, tt.want, tt.skewed)
			}
		})
	}
}

// TestSkewGuard_AgentClock120sAhead: with the agent clock 120 s ahead of
// the hub's, the hub's not_after is already past at receipt; the guard
// ignores it, the delivery pastes on the receipt bound, and the skew is
// counted at warn without times or ids (r3 §2.2 lines 158-160, §6.3
// line 351, §6.4 line 401).
func TestSkewGuard_AgentClock120sAhead(t *testing.T) {
	h := newHarness(t)
	r := h.post(t, body(func(m map[string]any) {
		m["not_after_unix_ms"] = notAfter(h.clock.Now(), 120*time.Second, 92*time.Second)
	}))
	if r.status != http.StatusOK || r.resp.Outcome != outcomeDelivered || h.run.count("paste-buffer") != 1 {
		t.Fatalf("got %d %s steps %v, want a paste", r.status, r.raw, h.run.verbs())
	}
	if n := h.d.skewIgnored.Load(); n != 1 {
		t.Fatalf("skew count %d, want 1", n)
	}
	if len(h.logs.warn) != 1 || strings.Contains(h.logs.warn[0], "d-1") || strings.Contains(h.logs.warn[0], "broker-1") {
		t.Fatalf("warn lines %q, want one count-only line", h.logs.warn)
	}
}

// TestSkewGuard_NotAfterBelow62sIgnored: a not_after earlier than
// receipt + 62 s (92 s - the 30 s tolerance, r3 §2.2 line 159) is
// ignored; a readiness wait past it still pastes before the receipt
// bound.
func TestSkewGuard_NotAfterBelow62sIgnored(t *testing.T) {
	h := newHarness(t)
	h.run.probe = func() string { return probeHookIdle }
	r := h.post(t, body(func(m map[string]any) {
		m["not_after_unix_ms"] = h.clock.Now().Add(61 * time.Second).UnixMilli()
		m["ready"] = map[string]any{"strategy": "hook", "timeout_ms": 90000}
	}))
	if r.status != http.StatusOK || h.run.count("paste-buffer") != 1 || h.d.skewIgnored.Load() != 1 {
		t.Fatalf("got %d %s steps %v skew %d", r.status, r.raw, h.run.verbs(), h.d.skewIgnored.Load())
	}
}

// TestHardStop_NotAfter70sReadinessPastIt408: a not_after at receipt +
// 70 s is inside the tolerance and used; a readiness wait that runs past
// it answers 408 expired_before_paste and never calls load-buffer or
// paste-buffer (r3 §6.3 lines 351 and 359). It is not stored: nothing
// was done, so a retry runs.
func TestHardStop_NotAfter70sReadinessPastIt408(t *testing.T) {
	h := newHarness(t)
	h.run.probe = func() string { return probeHookIdle }
	r := h.post(t, body(func(m map[string]any) {
		m["not_after_unix_ms"] = h.clock.Now().Add(70 * time.Second).UnixMilli()
		m["ready"] = map[string]any{"strategy": "hook", "timeout_ms": 90000}
	}))
	if r.status != http.StatusRequestTimeout || r.resp.Outcome != outcomeExpired {
		t.Fatalf("got %d %s, want 408 expired_before_paste", r.status, r.raw)
	}
	if h.run.count("load-buffer") != 0 || h.run.count("paste-buffer") != 0 || h.run.count("send-keys") != 0 {
		t.Fatalf("acting steps ran: %v", h.run.verbs())
	}
	if h.d.skewIgnored.Load() != 0 {
		t.Fatal("not_after at receipt + 70 s was treated as skew")
	}
	h.run.probe = func() string { return probeHookDone }
	if r := h.post(t, body(nil)); r.status != http.StatusOK || r.resp.Replayed == nil || *r.resp.Replayed {
		t.Fatalf("retry: %d %s, want a fresh 200", r.status, r.raw)
	}
}

// TestHardStop_SubmitOpFirstKeyRefused: for the *_submit ops the hard
// stop gates the first key (r3 §1.2 line 45). The clock passes the stop
// during the target check, so no key is sent.
func TestHardStop_SubmitOpFirstKeyRefused(t *testing.T) {
	h := newHarness(t)
	h.run.hook = func(argv []string) {
		if verb(argv) == "has-session" {
			h.clock.Advance(93 * time.Second)
		}
	}
	r := h.post(t, body(func(m map[string]any) {
		m["op"] = "interrupt_submit"
		delete(m, "text")
		m["interrupt_keys"] = []string{"C-c"}
	}))
	if r.status != http.StatusRequestTimeout || h.run.count("send-keys") != 0 {
		t.Fatalf("got %d %s steps %v", r.status, r.raw, h.run.verbs())
	}
}

// TestDeliverLogging_NoTextOrKeysAtInfo: the message text and key names
// never reach info or warn; info carries the delivery id, size, op and
// outcome (r3 §5.6 line 304).
func TestDeliverLogging_NoTextOrKeysAtInfo(t *testing.T) {
	h := newHarness(t)
	const text = "SENTINEL-TEXT-91c2"
	h.post(t, body(func(m map[string]any) {
		m["op"] = "interrupt_paste"
		m["text"] = text
		m["interrupt_keys"] = []string{"Escape"}
	}))
	h.run.fail = map[string]error{"send-keys": errors.New("boom")}
	h.post(t, body(func(m map[string]any) { m["delivery_id"] = "d-2"; m["text"] = text }))
	h.post(t, body(func(m map[string]any) { m["text"] = text; m["submit_keys"] = []string{"Escape", "bogus"} }))
	lines := append(append([]string(nil), h.logs.info...), h.logs.warn...)
	if len(h.logs.info) < 3 {
		t.Fatalf("info lines %q, want one per request", h.logs.info)
	}
	for _, l := range lines {
		if strings.Contains(l, text) || strings.Contains(l, "Escape") || strings.Contains(l, "Enter") || strings.Contains(l, "bogus") {
			t.Fatalf("info/warn line carries text or a key name: %q", l)
		}
	}
	for _, l := range h.logs.debug {
		if strings.Contains(l, text) {
			t.Fatalf("debug line carries the text: %q", l)
		}
	}
	if !strings.Contains(h.logs.info[0], "delivery_id=d-1") || !strings.Contains(h.logs.info[0], "op=interrupt_paste") ||
		!strings.Contains(h.logs.info[0], fmt.Sprintf("bytes=%d", len(text))) || !strings.Contains(h.logs.info[0], "outcome=delivered") {
		t.Fatalf("info line %q lacks the delivery fields", h.logs.info[0])
	}
}

// TestDeliverRoute_Exposure: the route is served and listed in Routes()
// only when Deliver is set (design r3 §1.5, §6.3 route exposure).
func TestDeliverRoute_Exposure(t *testing.T) {
	off := New(Options{KickTokenRefresh: func() {}})
	if slices.Contains(off.Routes(), RouteDeliverMessage) {
		t.Fatalf("Routes() = %v without Deliver", off.Routes())
	}
	rec := httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, Prefix+RouteDeliverMessage, strings.NewReader(body(nil))))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("route without Deliver: %d, want 404", rec.Code)
	}
	on := New(Options{KickTokenRefresh: func() {}, Deliver: &DeliverOptions{Runner: &fakeRunner{}}})
	if got := on.Routes(); !slices.Equal(got, []string{RouteRotateToken, RouteDeliverMessage}) {
		t.Fatalf("Routes() = %v", got)
	}
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, Prefix+RouteDeliverMessage, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d, want 405", rec.Code)
	}
}

// TestNewPanicsWithoutRunner: Deliver without a Runner is a programming
// error.
func TestNewPanicsWithoutRunner(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	New(Options{KickTokenRefresh: func() {}, Deliver: &DeliverOptions{}})
}

// TestAllowedArgv: the local runner runs tmux and the exact probe only.
func TestAllowedArgv(t *testing.T) {
	for _, argv := range [][]string{{"tmux", "send-keys"}, {"tmux", "has-session"}, {"tmux", "load-buffer"},
		{"tmux", "paste-buffer"}, {"tmux", "delete-buffer"}, tmuxdeliver.ProbeArgv()} {
		if err := allowedArgv(argv); err != nil {
			t.Fatalf("%q refused", argv)
		}
	}
	for _, argv := range [][]string{nil, {"tmux"}, {"sh", "-c", "id"}, {"/usr/bin/tmux", "ls"}, {"bash"},
		{"tmux", "run-shell", "id"}, {"tmux", "if-shell", "true", "x"}, {"tmux", "kill-server"}} {
		if err := allowedArgv(argv); err == nil {
			t.Fatalf("%q allowed", argv)
		}
	}
}
