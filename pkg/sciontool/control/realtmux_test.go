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

//go:build unix

package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realTmux starts a private tmux server (its own TMUX_TMPDIR, TMUX unset)
// with session "scion" whose pane runs paneCmd, and returns a Handler
// whose deliver-message route uses the real LocalRunner against it.
func realTmux(t *testing.T, paneCmd string) *Handler {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		// With SCION_TEST_REQUIRE_TMUX=1 a missing tmux fails instead of
		// skipping (the repo convention, ptone/scion#3312).
		if os.Getenv("SCION_TEST_REQUIRE_TMUX") == "1" {
			t.Fatal("tmux is required (SCION_TEST_REQUIRE_TMUX=1) but is not on PATH")
		}
		t.Skip("tmux not on PATH")
	}
	// A short private socket directory; never the agent's own server.
	dir, err := os.MkdirTemp("", "ctltmux")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	tmux := func(args ...string) error {
		cmd := exec.Command("tmux", args...)
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+dir)
		cmd.Env = withoutVar(cmd.Env, "TMUX")
		return cmd.Run()
	}
	if err := tmux("-f", "/dev/null", "new-session", "-d", "-s", "scion", "-x", "200", "-y", "20", paneCmd); err != nil {
		if os.Getenv("SCION_TEST_REQUIRE_TMUX") == "1" {
			t.Fatalf("cannot start tmux: %v", err)
		}
		t.Skipf("cannot start tmux: %v", err)
	}
	t.Cleanup(func() { _ = tmux("kill-server") })
	tmuxPath, err := ResolveTool("tmux")
	if err != nil {
		t.Fatalf("tmux runs but is not in %s: %v", ToolPath, err)
	}
	shPath, err := ResolveTool("sh")
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{KickTokenRefresh: func() {}, Deliver: &DeliverOptions{Runner: LocalRunner{TmuxPath: tmuxPath, ShPath: shPath}}})
}

func withoutVar(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func postReal(t *testing.T, h *Handler, m map[string]any) deliverResponse {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, Prefix+RouteDeliverMessage, strings.NewReader(string(b))))
	var resp deliverResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusOK || resp.Outcome != outcomeDelivered {
		t.Fatalf("status %d body %q, want 200 delivered", rec.Code, rec.Body.String())
	}
	return resp
}

func realBody(id, op, text string) map[string]any {
	m := map[string]any{
		"v": 1, "delivery_id": id, "broker_id": "broker-1", "message_ids": []string{"m-1"},
		"op": op, "deadline_ms": 92000, "submit_keys": []string{"Enter"},
	}
	if text != "" {
		m["text"] = text
	}
	return m
}

// waitFile waits until path holds want bytes, and returns its content.
func waitFile(t *testing.T, path string, want int) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(path)
		if len(got) >= want {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return string(got)
}

// waitExists waits until path exists.
func waitExists(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", filepath.Base(path))
}

// TestRealTmux_Deliver512KiBByteExact: a 512 KiB body arrives byte-exact
// through the route, the local runner and a bracketed paste (design r3
// §6.3, sciontool real tmux). Lines stay under the terminal's canonical
// line limit; tmux turns each newline into CR and the pane's tty maps it
// back.
func TestRealTmux_Deliver512KiBByteExact(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	h := realTmux(t, "cat > "+out)
	line := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ-\n"
	text := strings.Repeat(line, MaxDeliverText/len(line))
	if len(text) != MaxDeliverText {
		t.Fatalf("fixture is %d bytes, want %d", len(text), MaxDeliverText)
	}
	postReal(t, h, realBody("big-1", "paste", text))
	want := text + "\n" // the submit key ends one more (empty) line
	if got := waitFile(t, out, len(want)); got != want {
		t.Fatalf("pane received %d bytes, want %d byte-exact", len(got), len(want))
	}
}

// TestRealTmux_PasteEndMarkerArrivesAsText: a literal ESC[201~ and the
// 8-bit CSI U+009B in the text arrive as inert text, so the paste cannot
// end early and be read as typed keys (design r3 §5.3).
func TestRealTmux_PasteEndMarkerArrivesAsText(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	h := realTmux(t, "cat > "+out)
	postReal(t, h, realBody("marker-1", "paste", "hello \x1b[201~world\u009b!"))
	want := "hello [201~world!\n"
	if got := waitFile(t, out, len(want)); got != want {
		t.Fatalf("pane received %q, want %q", got, want)
	}
}

// TestRealTmux_InterruptWithoutReadiness: interrupt_paste sends the
// interrupt key and pastes with no readiness wait, even though this
// session writes no readiness files (design r3 §3.1, R1b). The pane
// ignores SIGINT, so the interrupt only discards the pending input line.
func TestRealTmux_InterruptWithoutReadiness(t *testing.T) {
	dir := t.TempDir()
	out, ready := filepath.Join(dir, "out"), filepath.Join(dir, "ready")
	// The pane marks itself ready only after it ignores SIGINT, so the
	// interrupt cannot reach it before the trap is in place (a fresh pane
	// may not have run its first command yet when the test posts).
	h := realTmux(t, "trap '' INT; : > "+ready+"; exec cat > "+out)
	waitExists(t, ready)
	m := realBody("intr-1", "interrupt_paste", "after")
	m["interrupt_keys"] = []string{"C-c"}
	start := time.Now()
	postReal(t, h, m)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("interrupt delivery took %s; it must not wait for readiness", took)
	}
	want := "after\n"
	if got := waitFile(t, out, len(want)); got != want {
		t.Fatalf("pane received %q, want %q", got, want)
	}
}
