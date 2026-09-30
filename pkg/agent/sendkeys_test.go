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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/agentkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// runningAgent returns a minimal, running, identity-bound fixture for
// SendKeys tests: slug "test-agent", project "proj-1", agent_id "agent-abc".
func runningAgent() api.AgentInfo {
	return api.AgentInfo{
		ContainerID: "container-1",
		Name:        "test-agent",
		ProjectID:   "proj-1",
		Phase:       string(state.PhaseRunning),
		Labels: map[string]string{
			"scion.name": "test-agent",
			"agent_id":   "agent-abc",
		},
	}
}

// execRecord captures one call into a mock Runtime, whichever method made
// it: argv is always populated (joined by spaces); stdin is only non-empty
// for an ExecWithStdin call.
type execRecord struct {
	argv  string
	stdin string
}

// newSendKeysMock builds a MockRuntime that answers List with agents and
// records every Exec/ExecWithStdin call into captured, succeeding every call
// unless execErr is non-nil.
func newSendKeysMock(agents []api.AgentInfo, captured *[]execRecord, execErr error) *runtime.MockRuntime {
	var mu sync.Mutex
	record := func(cmd []string, stdin string) {
		mu.Lock()
		*captured = append(*captured, execRecord{argv: strings.Join(cmd, " "), stdin: stdin})
		mu.Unlock()
	}
	return &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return agents, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			record(cmd, "")
			if execErr != nil {
				return "", execErr
			}
			return "", nil
		},
		ExecWithStdinFunc: func(ctx context.Context, id string, cmd []string, stdin io.Reader) (string, error) {
			data, _ := io.ReadAll(stdin)
			record(cmd, string(data))
			if execErr != nil {
				return "", execErr
			}
			return "", nil
		},
	}
}

// TestSendKeys_ArgvExactness covers AC "Enter, Escape, C-c, Unicode, spaces
// and literal @text reach the exact expected argv with no added Enter", and
// the adapter argv/stdin spy for the stdin-based delivery mechanism: SendKeys
// must call "tmux has-session -t scion:0" (the readiness probe, a plain
// Exec) followed by ExecWithStdin with the fixed argv "tmux source-file -"
// and a stdin payload built by sendKeysScript — never trimmed, split,
// tokenized, or followed by an implicit Enter (contrast deliverImmediate's
// message path, which does add one).
func TestSendKeys_ArgvExactness(t *testing.T) {
	cases := []struct {
		name string
		keys string
	}{
		{"Enter", "Enter"},
		{"Escape", "Escape"},
		{"C-c", "C-c"},
		{"spaces_not_a_sequence", "Up Up Enter"},
		{"literal_at_text", "@builder do the thing"},
		{"unicode", "héllo 世界 🎉"},
		{"whitespace_only", "   "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured []execRecord
			mock := newSendKeysMock([]api.AgentInfo{runningAgent()}, &captured, nil)
			mgr := &AgentManager{Runtime: mock}

			err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", tc.keys)
			if err != nil {
				t.Fatalf("SendKeys failed: %v", err)
			}

			if len(captured) == 0 {
				t.Fatalf("expected at least one call, got none")
			}
			last := captured[len(captured)-1]
			wantArgv := "tmux source-file -"
			if last.argv != wantArgv {
				t.Errorf("argv = %q, want %q", last.argv, wantArgv)
			}
			wantStdin := sendKeysScript(keysTarget, tc.keys)
			if last.stdin != wantStdin {
				t.Errorf("stdin = %q, want %q", last.stdin, wantStdin)
			}
			// No step must add a trailing Enter (unlike deliverImmediate's
			// message path), and the payload must never appear as a process
			// argument.
			for _, c := range captured {
				if c.argv == "tmux send-keys -t scion:0 Enter" {
					t.Errorf("SendKeys must never send an implicit Enter, but got: %v", captured)
				}
				if strings.Contains(c.argv, tc.keys) && tc.keys != "" {
					t.Errorf("payload must not appear in argv, got: %q", c.argv)
				}
			}
		})
	}
}

// TestSendKeys_InvalidKeysShapeNeverExecutes covers the AC "empty/NUL/
// oversize/invalid shapes never execute" at the primitive level itself
// (review round 2, finding #4; renamed in round 3, finding #6, from
// TestSendKeys_EmptyKeysPassedThroughVerbatim, which asserted the opposite
// of what the test now does): SendKeys calls agentkeys.ValidateKeys before
// any resolution or Exec attempt, so a local-mode caller invoking this
// primitive directly — without going through the runtimebroker's own
// ValidateKeys enforcement (handlers_keys_test.go's
// TestSendKeys_HTTP_InvalidKeysShape table) — still gets the same
// guarantee, never a silent "empty becomes Enter" (contrast
// deliverImmediate's message path, which does have that special case for
// its own, unrelated reasons). Includes an invalid-UTF-8 case that the JSON
// broker route can never actually reach (encoding/json repairs invalid
// UTF-8 to U+FFFD on decode — see TestSendKeys_HTTP_InvalidKeysShape's doc
// comment) but a direct primitive caller can.
func TestSendKeys_InvalidKeysShapeNeverExecutes(t *testing.T) {
	cases := []struct {
		name string
		keys string
	}{
		{"empty", ""},
		{"nul_byte", "abc\x00def"},
		{"oversize", strings.Repeat("a", agentkeys.MaxBytes+1)},
		{"invalid_utf8", "abc\xff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured []execRecord
			mock := newSendKeysMock([]api.AgentInfo{runningAgent()}, &captured, nil)
			mgr := &AgentManager{Runtime: mock}

			err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", tc.keys)
			if err == nil {
				t.Fatal("expected an error")
			}
			if _, ok := agentkeys.AsValidationError(err); !ok {
				t.Errorf("SendKeys error = %v, want an *agentkeys.ValidationError", err)
			}
			if len(captured) != 0 {
				t.Errorf("expected zero Exec calls for invalid keys shape %q, got %v", tc.name, captured)
			}
		})
	}
}

// TestSendKeys_TargetNotFound_NoMatch covers the "missing" case of AK-45/46:
// no container matches (projectID, agentSlug) at all.
func TestSendKeys_TargetNotFound_NoMatch(t *testing.T) {
	var captured []execRecord
	mock := newSendKeysMock(nil, &captured, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if len(captured) != 0 {
		t.Fatalf("expected no Exec calls when target is not found, got %v", captured)
	}
}

// TestSendKeys_TargetNotFound_AgentIDMismatch covers AK-45: a container
// resolves by slug/project, but its "agent_id" label does not match the
// caller's expected ID — e.g. a same-slug agent recreated inside the
// execute-before window. Must fail closed to not_found, never fall back to
// the slug-only match.
func TestSendKeys_TargetNotFound_AgentIDMismatch(t *testing.T) {
	agent := runningAgent()
	agent.Labels["agent_id"] = "some-other-agent"

	var captured []execRecord
	mock := newSendKeysMock([]api.AgentInfo{agent}, &captured, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if len(captured) != 0 {
		t.Fatalf("expected no Exec calls on an agent_id mismatch, got %v", captured)
	}
}

// TestSendKeys_TargetNotFound_MissingLabel covers AK-46: a container started
// outside the Hub's dispatch path (or before SCION_AGENT_ID injection
// existed) has no "agent_id" label at all. Must fail closed, never a
// slug-only match.
func TestSendKeys_TargetNotFound_MissingLabel(t *testing.T) {
	agent := runningAgent()
	delete(agent.Labels, "agent_id")

	var captured []execRecord
	mock := newSendKeysMock([]api.AgentInfo{agent}, &captured, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if len(captured) != 0 {
		t.Fatalf("expected no Exec calls on a missing agent_id label, got %v", captured)
	}
}

// TestSendKeys_CrossProjectSameSlugNeverConfused covers "Two same-name
// agents in different projects cannot be confused": a container matching
// the requested slug exists, but only in a different project than the one
// requested. The other project's container must never be selected.
func TestSendKeys_CrossProjectSameSlugNeverConfused(t *testing.T) {
	other := runningAgent()
	other.ProjectID = "proj-2"
	other.ContainerID = "container-2"
	other.Labels = map[string]string{"scion.name": "test-agent", "agent_id": "agent-abc", "scion.project_id": "proj-2"}

	var capturedCmd []string
	// The runtime's List is expected to be called with a "scion.project_id"
	// filter; a real runtime backend would exclude proj-2's container from
	// the result for a proj-1 query, so the mock replicates that filtering
	// rather than returning both indiscriminately (which would hide a bug
	// where SendKeys forgot to pass the filter at all).
	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			wantProject, ok := filter["scion.project_id"]
			if !ok {
				t.Errorf("List called without a scion.project_id filter: %v", filter)
				return []api.AgentInfo{other}, nil
			}
			if other.ProjectID == wantProject {
				return []api.AgentInfo{other}, nil
			}
			return nil, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			capturedCmd = append(capturedCmd, strings.Join(cmd, " "))
			return "", nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound (proj-2's agent must not be selected for a proj-1 request)", err)
	}
	if len(capturedCmd) != 0 {
		t.Fatalf("expected no Exec calls against the wrong project's container, got %v", capturedCmd)
	}
}

// TestSendKeys_AgentNotRunning covers AK-26: a resolved, identity-confirmed
// target that is not running must fail with ErrAgentNotRunning, with no
// Exec attempt (no wake/start).
func TestSendKeys_AgentNotRunning(t *testing.T) {
	agent := runningAgent()
	agent.Phase = string(state.PhaseStopped)

	var captured []execRecord
	mock := newSendKeysMock([]api.AgentInfo{agent}, &captured, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrAgentNotRunning) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrAgentNotRunning", err)
	}
	if len(captured) != 0 {
		t.Fatalf("expected no Exec calls when agent is not running, got %v", captured)
	}
}

// TestSendKeys_TerminalNotReady covers AK-27: a running, identity-confirmed
// target whose tmux session is not accepting input (has-session fails) must
// fail with ErrTerminalNotReady, and the send-keys call itself must never
// run.
func TestSendKeys_TerminalNotReady(t *testing.T) {
	agent := runningAgent()

	var capturedCmd []string
	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			capturedCmd = append(capturedCmd, strings.Join(cmd, " "))
			if len(cmd) >= 2 && cmd[1] == "has-session" {
				return "", errors.New("no such session")
			}
			return "", nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTerminalNotReady) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTerminalNotReady", err)
	}
	for _, c := range capturedCmd {
		if strings.Contains(c, "send-keys") {
			t.Fatalf("send-keys must not run when the terminal is not ready, but got: %v", capturedCmd)
		}
	}
}

// TestSendKeys_PlainErrorOnAmbiguousExecFailure covers "Any execution error
// with uncertain terminal effect is represented as unknown, not definitely
// undelivered": once resolution/identity/readiness have all passed, a
// failure from the send-keys Exec call itself (which may have partially
// run) must be a plain error, never one of the three sentinels.
func TestSendKeys_PlainErrorOnAmbiguousExecFailure(t *testing.T) {
	agent := runningAgent()
	execErr := errors.New("tmux: some ambiguous failure")

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			if len(cmd) >= 2 && cmd[1] == "has-session" {
				return "", nil
			}
			return "", execErr
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, agentkeys.ErrTargetNotFound) || errors.Is(err, agentkeys.ErrAgentNotRunning) || errors.Is(err, agentkeys.ErrTerminalNotReady) || errors.Is(err, ErrKeysNotStarted) {
		t.Fatalf("an ambiguous Exec failure must never be reported as one of the proven-before-execution sentinels, got: %v", err)
	}
	// The underlying error's *text* must still be discoverable (for
	// diagnostics), but not via errors.Is/errors.As — see
	// TestSendKeys_PostExecFailureWrappingCtxErrorIsNotErrKeysNotStarted for
	// why the chain is deliberately broken.
	if !strings.Contains(err.Error(), execErr.Error()) {
		t.Fatalf("expected the underlying Exec error's text to be present, got: %v", err)
	}
	if errors.Is(err, execErr) {
		t.Fatalf("the underlying Exec error must not be reachable via errors.Is (chain must be broken), got: %v", err)
	}
}

// TestSendKeys_StdinTransportFailureIsAmbiguous covers a transport failure
// on the delivery mechanism itself (ExecWithStdin, not Exec's fallback to
// it): once the readiness probe has passed, a failure from the stdin-based
// call must be a plain error — never one of the proven-before-execution
// sentinels — matching TestSendKeys_PlainErrorOnAmbiguousExecFailure's
// requirement, exercised against the actual method SendKeys calls for
// delivery rather than MockRuntime's Exec-fallback behavior.
func TestSendKeys_StdinTransportFailureIsAmbiguous(t *testing.T) {
	agent := runningAgent()
	transportErr := errors.New("transport: connection reset")
	var gotStdin string

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			return "", nil // the has-session probe
		},
		ExecWithStdinFunc: func(ctx context.Context, id string, cmd []string, stdin io.Reader) (string, error) {
			data, _ := io.ReadAll(stdin)
			gotStdin = string(data)
			return "", transportErr
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, agentkeys.ErrTargetNotFound) || errors.Is(err, agentkeys.ErrAgentNotRunning) || errors.Is(err, agentkeys.ErrTerminalNotReady) || errors.Is(err, ErrKeysNotStarted) {
		t.Fatalf("a stdin-transport failure must never be reported as one of the proven-before-execution sentinels, got: %v", err)
	}
	if gotStdin != sendKeysScript(keysTarget, "C-c") {
		t.Fatalf("ExecWithStdin received stdin %q, want the generated script", gotStdin)
	}
}

// TestSendKeys_PostExecFailureWrappingCtxErrorIsNotErrKeysNotStarted is
// review round 2 finding #1's core regression test: a send-keys Exec
// failure that itself wraps a context error (e.g. a Kubernetes exec stream
// cancelled mid-call, or os/exec's Wait returning ctx.Err() after a
// successful cancel) must not be classified as "proven not to have
// started" — only SendKeys's own pre-Exec checks may produce that
// classification. Reproduces the exact repro the reviewer used: the mock
// records that send-keys ran, then returns an error wrapping
// context.DeadlineExceeded.
func TestSendKeys_PostExecFailureWrappingCtxErrorIsNotErrKeysNotStarted(t *testing.T) {
	agent := runningAgent()
	sendKeysRan := false

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			if len(cmd) >= 2 && cmd[1] == "has-session" {
				return "", nil
			}
			sendKeysRan = true
			// Simulates a backend whose Exec observed the call's own
			// context expiring *during* a genuinely-started call, and
			// wrapped that into its returned error — this must not be
			// mistaken for SendKeys's own pre-Exec expiry signal.
			return "", fmt.Errorf("exec stream: %w", context.DeadlineExceeded)
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")

	if !sendKeysRan {
		t.Fatal("test setup error: send-keys Exec never ran")
	}
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrKeysNotStarted) {
		t.Fatalf("a post-Exec failure that wraps a context error must never match ErrKeysNotStarted (it is not proven to have failed before Exec began), got: %v", err)
	}
	// Finding #1 part (a): SendKeys must not join the send-keys Exec error
	// with %w, so the mock's own context.DeadlineExceeded is not reachable
	// via errors.Is on SendKeys's returned value either — the handler must
	// be unable to reach this by accident even if it (incorrectly) checked
	// for context.DeadlineExceeded/Canceled directly instead of matching
	// ErrKeysNotStarted's identity.
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendKeys's returned error must not wrap context.DeadlineExceeded via errors.Is, got: %v", err)
	}
	// SendKeys deliberately breaks the error chain here (%v, not %w — see
	// ErrKeysNotStarted's doc comment), so errors.Is(err,
	// context.DeadlineExceeded) is correctly false on the returned error
	// itself; that is the fix, not a gap. What must still hold is that the
	// mock's own crafted error really did wrap context.DeadlineExceeded
	// (confirming this test reproduces the reviewer's repro shape) and that
	// its text survives into SendKeys's returned error for diagnostics.
	mockErr := fmt.Errorf("exec stream: %w", context.DeadlineExceeded)
	if !errors.Is(mockErr, context.DeadlineExceeded) {
		t.Fatal("test setup error: mock's own error does not wrap context.DeadlineExceeded")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected the underlying error's text to survive for diagnostics, got: %v", err)
	}
}

// TestSendKeys_ProbeFailureDuringExpiredDeadlineIsNotStarted covers review
// round 2 finding #3: if the admission deadline fires during the terminal-
// readiness probe itself, the outcome must be "proven not to have started"
// (ErrKeysNotStarted), not ErrTerminalNotReady — the session's actual
// readiness was never established either way, but only one of these two
// outcomes is honest about why the probe failed.
func TestSendKeys_ProbeFailureDuringExpiredDeadlineIsNotStarted(t *testing.T) {
	agent := runningAgent()
	ctx, cancel := context.WithCancel(context.Background())

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(execCtx context.Context, id string, cmd []string) (string, error) {
			if len(cmd) >= 2 && cmd[1] == "has-session" {
				// Simulates the deadline firing while the probe itself was
				// in flight: cancel the caller's ctx before the probe
				// returns its own (unrelated) failure.
				cancel()
				return "", errors.New("no such session")
			}
			return "", nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(ctx, "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, ErrKeysNotStarted) {
		t.Fatalf("SendKeys error = %v, want an error wrapping ErrKeysNotStarted", err)
	}
	if errors.Is(err, agentkeys.ErrTerminalNotReady) {
		t.Fatalf("a probe failure coinciding with ctx expiry must not be reported as ErrTerminalNotReady, got: %v", err)
	}
}

// TestSendKeys_DeadlineExpiredWhileWaitingForLock is the deadline-expiry-
// after-wait test: SendKeys must not inject once its ctx's deadline has
// passed while it was waiting for the per-target injection lock (modeling
// the contract's "after control-channel semaphore/target-lock wait"
// enforcement point) — even though the lock does eventually become free.
func TestSendKeys_DeadlineExpiredWhileWaitingForLock(t *testing.T) {
	agent := runningAgent()

	var capturedCmd []string
	var mu sync.Mutex
	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			mu.Lock()
			capturedCmd = append(capturedCmd, strings.Join(cmd, " "))
			mu.Unlock()
			return "", nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	// Hold the injection lock for this target for longer than the deadline
	// SendKeys will be given below, simulating a saturated critical section
	// (e.g. a slow concurrent message delivery). Keyed by the resolved
	// container ID (see injectionLock's doc comment), matching what
	// SendKeys itself will look up once it resolves the fixture above.
	lock := mgr.injectionLock(agent.ContainerID)
	if err := lock.Lock(context.Background()); err != nil {
		t.Fatalf("failed to seed the lock: %v", err)
	}
	release := make(chan struct{})
	go func() {
		<-release
		lock.Unlock()
	}()
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := mgr.SendKeys(ctx, "proj-1", "test-agent", "agent-abc", "C-c")
	if err == nil {
		t.Fatal("expected SendKeys to fail once its deadline expired while waiting for the lock")
	}
	if !errors.Is(err, ErrKeysNotStarted) {
		t.Fatalf("SendKeys error = %v, want an error wrapping ErrKeysNotStarted", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(capturedCmd) != 0 {
		t.Fatalf("expected no Exec calls once the admission deadline expired before the lock was acquired, got %v", capturedCmd)
	}
}

// TestSendKeys_ConcurrentWithInterruptMessage_NoInterleave covers "Concurrent
// keys and buffered/interrupt messages cannot interleave manager injection
// sequences": SendKeys and an interrupt Message call for the same target,
// launched concurrently, must never have their tmux Exec calls interleaved —
// one call's full sequence of Exec invocations must complete before the
// other's begins.
//
// This is checked by recording the ordered sequence of caller tags across
// every Exec call (not just whether two calls were ever simultaneously
// "active"): a lock-free A,B,A ordering with no time overlap at all would
// pass an overlap-only check but still prove the two callers' Exec sequences
// were not each contiguous, which is what the AC actually requires. A
// correctly serialized run produces exactly one transition in the recorded
// sequence (all of one caller's Execs, then all of the other's, in either
// order) — this test asserts exactly that, and was confirmed to fail 3/3
// runs with injectionLock's Lock call removed from SendKeys and
// deliverImmediate during review.
func TestSendKeys_ConcurrentWithInterruptMessage_NoInterleave(t *testing.T) {
	agent := runningAgent()

	var mu sync.Mutex
	var sequence []string
	record := func(who string) {
		mu.Lock()
		sequence = append(sequence, who)
		mu.Unlock()
	}
	leaveDelay := 3 * time.Millisecond

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		// Tags which logical call (SendKeys vs. the interrupt message) is
		// currently inside an Exec invocation via a ctx value set by each
		// goroutine below, recording the tag on every call.
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			record(currentCaller(ctx))
			time.Sleep(leaveDelay)
			return "", nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx := withCaller(context.Background(), "keys")
		if err := mgr.SendKeys(ctx, "proj-1", "test-agent", "agent-abc", "C-c"); err != nil {
			t.Errorf("SendKeys failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		ctx := withCaller(context.Background(), "interrupt")
		// projectID is irrelevant to which lock the two calls share now
		// (injectionLock keys on the resolved container ID, not caller
		// strings), but both resolve the same fixture regardless.
		if err := mgr.deliverImmediate(ctx, "test-agent", "proj-1", "", true); err != nil {
			t.Errorf("deliverImmediate failed: %v", err)
		}
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(sequence) < 2 {
		t.Fatalf("expected at least 2 recorded Exec calls (one per caller), got %v", sequence)
	}
	transitions := 0
	for i := 1; i < len(sequence); i++ {
		if sequence[i] != sequence[i-1] {
			transitions++
		}
	}
	if transitions != 1 {
		t.Fatalf("SendKeys and an interrupt message interleaved their tmux Exec calls for the same target: sequence = %v (want exactly one transition between callers, got %d)", sequence, transitions)
	}
}

// TestMessageRaw_ConcurrentWithSendKeys_NoInterleave covers review round 2
// finding #5: MessageRaw (the legacy raw-keys primitive, pending Phase 4
// removal) previously did not take injectionLock at all, so raw keys
// delivered through it could interleave with a concurrent SendKeys call (or
// a buffered/interrupt message) for the same target. Same
// sequence-contiguity technique as
// TestSendKeys_ConcurrentWithInterruptMessage_NoInterleave.
// TestMessageRaw_ConcurrentWithSendKeys_NoInterleave deterministically
// forces the race the injection lock exists to prevent (review round 3,
// finding #2: the previous version of this test passed 30/30 runs with the
// lock removed from MessageRaw, because MessageRaw's single Exec call
// happened to always run to completion before SendKeys's two calls in
// practice, never actually landing between them).
//
// SendKeys's mocked Exec blocks on its first call — the "has-session"
// readiness probe, which the lock must be held across — after signaling
// that it has entered its critical section. Only once that signal arrives
// is the MessageRaw goroutine started, and only after giving it a fixed
// window to reach (and, with the lock present, block on) its own Exec
// attempt is the probe released to let SendKeys continue to its second
// (send-keys) call. With the lock, MessageRaw's Lock call cannot succeed
// until SendKeys's Unlock — after both of its calls — so the sequence must
// be keys, keys, raw. Without it, MessageRaw's unblocked Exec call lands
// inside that window, producing keys, raw, keys instead. Confirmed (during
// review) to fail with the lock removed from MessageRaw, and confirmed
// again here via a temporary revert-and-retest before restoring the fix.
func TestMessageRaw_ConcurrentWithSendKeys_NoInterleave(t *testing.T) {
	agent := runningAgent()

	var mu sync.Mutex
	var sequence []string
	record := func(who string) {
		mu.Lock()
		sequence = append(sequence, who)
		mu.Unlock()
	}

	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			record(currentCaller(ctx))
			if currentCaller(ctx) == "keys" && len(cmd) >= 2 && cmd[1] == "has-session" {
				// SendKeys's readiness probe — its first of two Exec calls,
				// made while (with the lock present) still holding the
				// injection lock. Signal entry, then hold here until the
				// test decides MessageRaw has had its chance to race in.
				close(probeEntered)
				<-releaseProbe
			}
			return "", nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := withCaller(context.Background(), "keys")
		if err := mgr.SendKeys(ctx, "proj-1", "test-agent", "agent-abc", "C-c"); err != nil {
			t.Errorf("SendKeys failed: %v", err)
		}
	}()

	<-probeEntered

	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := withCaller(context.Background(), "raw")
		if err := mgr.MessageRaw(ctx, "test-agent", "", "Escape"); err != nil {
			t.Errorf("MessageRaw failed: %v", err)
		}
	}()

	// Give MessageRaw time to reach its own Exec attempt — with the lock
	// present it blocks there; without it, it completes within this window,
	// landing between SendKeys's two calls.
	time.Sleep(50 * time.Millisecond)
	close(releaseProbe)

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(sequence) != 3 {
		t.Fatalf("expected exactly 3 recorded Exec calls (2 from SendKeys, 1 from MessageRaw), got %v", sequence)
	}
	want := []string{"keys", "keys", "raw"}
	for i := range want {
		if sequence[i] != want[i] {
			t.Fatalf("SendKeys and MessageRaw interleaved their tmux Exec calls for the same target: sequence = %v, want %v", sequence, want)
		}
	}
}

// callerCtxKey/withCaller/currentCaller let the concurrency test above tag
// which logical call (SendKeys vs. the interrupt message) is currently
// inside an Exec invocation, without depending on argv shape (SendKeys's
// has-session probe and the interrupt path's send-keys call have different
// argv anyway, but tagging via ctx keeps the test's intent explicit).
type callerCtxKey struct{}

func withCaller(ctx context.Context, who string) context.Context {
	return context.WithValue(ctx, callerCtxKey{}, who)
}

func currentCaller(ctx context.Context) string {
	who, _ := ctx.Value(callerCtxKey{}).(string)
	return who
}

// TestTmuxOctalEscape covers the byte-for-byte octal-escape encoding used to
// embed keys inside the generated tmux command: every byte becomes a
// three-digit octal escape, so no byte — including one that would otherwise
// need individual handling, such as a quote, a backslash, or a trailing
// separator character — is ever written unescaped.
func TestTmuxOctalEscape(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"A", `\101`},
		{";", `\073`},
		{`"`, `\042`},
		{`\`, `\134`},
		{"a;b", `\141\073\142`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := tmuxOctalEscape(tc.in)
			if got != tc.want {
				t.Errorf("tmuxOctalEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSendKeysScript covers the exact generated command line SendKeys
// supplies on stdin.
func TestSendKeysScript(t *testing.T) {
	got := sendKeysScript("scion:0", "Enter")
	want := "send-keys -t scion:0 -- \"\\105\\156\\164\\145\\162\"\n"
	if got != want {
		t.Errorf("sendKeysScript(...) = %q, want %q", got, want)
	}
}

// TestSendKeys_StdinScriptForSpecialCharacters is the argv/stdin spy for
// inputs that would have needed special-casing under a process-argument
// invocation (a trailing ';') and others that exercise the same encoding
// path (quotes, backslashes, embedded semicolons, newlines) — now handled
// uniformly by tmuxOctalEscape. TestRealTmuxSendKeys separately proves the
// round trip against a real tmux server.
func TestSendKeys_StdinScriptForSpecialCharacters(t *testing.T) {
	cases := []string{";", "abc;", `a"b`, `a\b`, "a;b;c", "line1\nline2"}
	for _, keys := range cases {
		t.Run(keys, func(t *testing.T) {
			var captured []execRecord
			mock := newSendKeysMock([]api.AgentInfo{runningAgent()}, &captured, nil)
			mgr := &AgentManager{Runtime: mock}

			if err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", keys); err != nil {
				t.Fatalf("SendKeys failed: %v", err)
			}

			last := captured[len(captured)-1]
			if last.argv != "tmux source-file -" {
				t.Errorf("argv = %q, want the fixed argv carrying no payload", last.argv)
			}
			want := sendKeysScript(keysTarget, keys)
			if last.stdin != want {
				t.Errorf("stdin = %q, want %q", last.stdin, want)
			}
		})
	}
}

// TestSendKeys_UnscopedProjectRejected covers #2193's "reject unscoped
// target fallback": SendKeys must fail closed on an empty projectID rather
// than listing across every project.
func TestSendKeys_UnscopedProjectRejected(t *testing.T) {
	var listCalled bool
	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			listCalled = true
			return []api.AgentInfo{runningAgent()}, nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if listCalled {
		t.Error("SendKeys must not list agents at all for an empty (unscoped) projectID")
	}
}

// TestSendKeys_UnscopedAgentIDRejected is TestSendKeys_UnscopedProjectRejected's
// counterpart for an empty expectedAgentID: SendKeys must fail closed rather
// than binding to whatever container happens to match the slug/project.
func TestSendKeys_UnscopedAgentIDRejected(t *testing.T) {
	var listCalled bool
	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			listCalled = true
			return []api.AgentInfo{runningAgent()}, nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if listCalled {
		t.Error("SendKeys must not list agents at all for an empty expectedAgentID")
	}
}

// TestSendKeys_UnsupportedBackend covers review finding #2: a manager whose
// runtime backend does not support keys delivery must fail with
// ErrKeysUnsupported before any resolution or Exec attempt.
func TestSendKeys_UnsupportedBackend(t *testing.T) {
	var listCalled bool
	mock := &runtime.MockRuntime{
		NameFunc: func() string { return "cloudrun" },
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			listCalled = true
			return []api.AgentInfo{runningAgent()}, nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, ErrKeysUnsupported) {
		t.Fatalf("SendKeys error = %v, want ErrKeysUnsupported", err)
	}
	if listCalled {
		t.Error("SendKeys must not resolve a target at all for a backend that does not support keys delivery")
	}
}

// TestSendKeys_MarksExecCallsSensitive covers review finding #7's
// SendKeys-level redaction check, adapted to what SendKeys itself is
// responsible for: it performs no logging of its own (there is nothing to
// capture here), and it cannot scrub arbitrary content out of a real
// backend's Exec error without violating "any other failure ... must be a
// plain error" (contract §4.3) — generic string-scrubbing at this layer
// would fight that requirement rather than serve it. What SendKeys does
// control, and what the actual source-level suppression
// (pkg/runtime/common.go's debug logging, the Kubernetes backend's
// stderr-embedding error — see TestRunSimpleCommand_SensitiveExec_
// SuppressesOutputInDebugLog and TestWrapExecStreamError_SensitiveOmitsStderr
// in pkg/runtime) depends on, is marking every Exec call — the readiness
// probe and the send-keys call itself — via runtime.WithSensitiveExec. This
// test proves that marking happens for both calls.
func TestSendKeys_MarksExecCallsSensitive(t *testing.T) {
	agent := runningAgent()
	var sawSensitive []bool

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			sawSensitive = append(sawSensitive, runtime.IsSensitiveExec(ctx))
			return "", nil
		},
	}
	mgr := &AgentManager{Runtime: mock}

	if err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c"); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}

	if len(sawSensitive) < 2 {
		t.Fatalf("expected at least 2 Exec calls (readiness probe + send-keys), got %d", len(sawSensitive))
	}
	for i, sensitive := range sawSensitive {
		if !sensitive {
			t.Errorf("Exec call %d was not marked sensitive via runtime.WithSensitiveExec", i)
		}
	}
}
