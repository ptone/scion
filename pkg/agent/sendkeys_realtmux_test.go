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

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// TestRealTmuxSendKeys drives AgentManager.SendKeys's actual argv against a
// private, disposable tmux server — never an active agent terminal, per the
// campaign's hard constraint — verifying named keys (Enter), Unicode, spaces,
// literal "@text" and trailing-semicolon content all reach the target
// exactly, with no automatic Enter added by SendKeys itself.
//
// The trailing-semicolon cases (review round 1, finding #1) exist because
// tmux's own command-line parser treats an unescaped trailing ';' as a
// command separator rather than literal input, dropping it, even when it
// arrives as part of a single argv element after "--". SendKeys's
// escapeTrailingSemicolon compensates for this before building the argv;
// these cases prove the round trip against a real tmux server rather than
// just against escapeTrailingSemicolon's own unit test.
func TestRealTmuxSendKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-tmux integration test in short mode")
	}

	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed; skipping real-tmux integration test")
	}

	dir, err := os.MkdirTemp("", "tmxk")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "sock")
	outFile := filepath.Join(dir, "out")

	runTmux := func(args ...string) (string, error) {
		cmd := exec.Command(tmuxPath, append([]string{"-S", sock}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("tmux %v failed: %w (%s)", args, err, out)
		}
		return string(out), nil
	}
	mustTmux := func(args ...string) string {
		t.Helper()
		out, err := runTmux(args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Cleanup(func() {
		_, _ = runTmux("kill-server")
	})

	// A pane running "cat" redirected to a file stands in for the agent's
	// terminal input. Session named "scion" so it matches SendKeys's
	// hardcoded "-t scion:0" target.
	mustTmux("-f", "/dev/null", "new-session", "-d", "-s", "scion", "-x", "220", "-y", "50", "cat > "+outFile)

	shim := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{{
				ContainerID: "local",
				Name:        "test-agent",
				Phase:       string(state.PhaseRunning),
				Labels: map[string]string{
					"scion.name": "test-agent",
					"agent_id":   "agent-abc",
				},
			}}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			return runTmux(cmd[1:]...)
		},
	}
	mgr := &AgentManager{Runtime: shim}

	send := func(keys string) {
		t.Helper()
		if err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", keys); err != nil {
			t.Fatalf("SendKeys(%q) failed: %v", keys, err)
		}
	}

	// Literal text containing Unicode, spaces and a literal "@name" — must
	// reach the pane byte-for-byte, never parsed or tokenized.
	//
	// cat's stdout is block-buffered here (not a tty), so intermediate
	// writes below this size are not guaranteed to reach outFile until EOF
	// flushes the buffer — unlike TestRealTmuxLoadBufferDeliversLargePayload's
	// 200 KB payload, which exceeds the buffer and can be polled
	// incrementally. So every send below happens before a single
	// end-of-sequence poll rather than being checked one at a time.
	text := "héllo @builder 世界 do the thing"
	send(text)

	// "Enter" is a recognized tmux key name and must be interpreted as an
	// actual keypress (arriving as a bare LF via the pty's ICRNL
	// translation), not typed as the four literal characters "Enter".
	send("Enter")

	// "Escape" is also a recognized tmux key name; cat has no ESC-driven
	// behavior of its own, so it should just pass the literal ESC (0x1b)
	// byte through, not the six literal characters "Escape".
	send("Escape")

	// Trailing-semicolon content, each a distinct literal string (raw string
	// literals below so every backslash is exactly what it looks like, with
	// no Go string-escape reinterpretation): a bare ';', a semicolon after
	// other text, and content that itself already ends in a literal
	// backslash followed by a semicolon (0, 0, and 1 "extra" backslash
	// before the trailing ';' respectively, exercising distinct cases of the
	// escapeTrailingSemicolon rule). Each must reach the pane as exactly
	// itself, not truncated and not with an extra backslash left over.
	semicolonCases := []string{
		`;`,
		`a;`,
		`a\;`,
		`a\\;`,
	}
	for _, sc := range semicolonCases {
		send(sc)
	}

	mustTmux("send-keys", "-t", "scion:0", "C-d")

	// Wait for the pane's output to settle before reading it.
	const quietFor = 100 * time.Millisecond
	deadline := time.Now().Add(5 * time.Second)
	lastSize := int64(-1)
	quietSince := time.Now()
	for {
		info, statErr := os.Stat(outFile)
		if statErr == nil {
			if info.Size() != lastSize {
				lastSize = info.Size()
				quietSince = time.Now()
			} else if time.Since(quietSince) >= quietFor {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pane output to settle (last size %d)", lastSize)
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading pane output: %v", err)
	}

	want := text + "\n" + "\x1b" + strings.Join(semicolonCases, "")
	if string(got) != want {
		t.Fatalf("pane output = %q, want %q", got, want)
	}
}
