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

// newSendKeysMock builds a MockRuntime that answers List with agents and
// records every Exec call's argv (joined by spaces) into capturedCmd,
// succeeding every Exec call unless execErr is non-nil.
func newSendKeysMock(agents []api.AgentInfo, capturedCmd *[]string, execErr error) *runtime.MockRuntime {
	var mu sync.Mutex
	return &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return agents, nil
		},
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			mu.Lock()
			*capturedCmd = append(*capturedCmd, strings.Join(cmd, " "))
			mu.Unlock()
			if execErr != nil {
				return "", execErr
			}
			return "", nil
		},
	}
}

// TestSendKeys_ArgvExactness covers AC "Enter, Escape, C-c, Unicode, spaces
// and literal @text reach the exact expected argv with no added Enter":
// SendKeys must call exactly "tmux has-session -t scion:0" (the readiness
// probe) followed by "tmux send-keys -t scion:0 -- <keys>" with keys as a
// single, untouched argv element — never trimmed, split, tokenized, or
// followed by an implicit Enter (contrast deliverImmediate's message path,
// which does add one).
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
			var capturedCmd []string
			mock := newSendKeysMock([]api.AgentInfo{runningAgent()}, &capturedCmd, nil)
			mgr := &AgentManager{Runtime: mock}

			err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", tc.keys)
			if err != nil {
				t.Fatalf("SendKeys failed: %v", err)
			}

			wantLast := "tmux send-keys -t scion:0 -- " + tc.keys
			if len(capturedCmd) == 0 {
				t.Fatalf("expected at least one Exec call, got none")
			}
			got := capturedCmd[len(capturedCmd)-1]
			if got != wantLast {
				t.Errorf("send-keys argv = %q, want %q", got, wantLast)
			}
			// No step must add a trailing Enter (unlike deliverImmediate's
			// message path).
			for _, c := range capturedCmd {
				if c == "tmux send-keys -t scion:0 Enter" {
					t.Errorf("SendKeys must never send an implicit Enter, but got: %v", capturedCmd)
				}
			}
		})
	}
}

// TestSendKeys_EmptyKeysPassedThroughVerbatim documents that SendKeys itself
// applies no "empty becomes Enter" special case — unlike deliverImmediate's
// message path. Rejecting an empty keys value is the Hub-level validation's
// job (agentkeys.ValidateBody, before a BrokerRequest is ever built); by the
// time SendKeys is called, per the frozen contract, Keys is "already
// validated" and must be passed through byte-for-byte with no
// reinterpretation at this layer.
func TestSendKeys_EmptyKeysPassedThroughVerbatim(t *testing.T) {
	var capturedCmd []string
	mock := newSendKeysMock([]api.AgentInfo{runningAgent()}, &capturedCmd, nil)
	mgr := &AgentManager{Runtime: mock}

	if err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", ""); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}

	want := "tmux send-keys -t scion:0 --"
	got := capturedCmd[len(capturedCmd)-1]
	if strings.TrimRight(got, " ") != want {
		t.Errorf("send-keys argv = %q, want an empty final element (%q)", got, want)
	}
}

// TestSendKeys_TargetNotFound_NoMatch covers the "missing" case of AK-45/46:
// no container matches (projectID, agentSlug) at all.
func TestSendKeys_TargetNotFound_NoMatch(t *testing.T) {
	var capturedCmd []string
	mock := newSendKeysMock(nil, &capturedCmd, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if len(capturedCmd) != 0 {
		t.Fatalf("expected no Exec calls when target is not found, got %v", capturedCmd)
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

	var capturedCmd []string
	mock := newSendKeysMock([]api.AgentInfo{agent}, &capturedCmd, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if len(capturedCmd) != 0 {
		t.Fatalf("expected no Exec calls on an agent_id mismatch, got %v", capturedCmd)
	}
}

// TestSendKeys_TargetNotFound_MissingLabel covers AK-46: a container started
// outside the Hub's dispatch path (or before SCION_AGENT_ID injection
// existed) has no "agent_id" label at all. Must fail closed, never a
// slug-only match.
func TestSendKeys_TargetNotFound_MissingLabel(t *testing.T) {
	agent := runningAgent()
	delete(agent.Labels, "agent_id")

	var capturedCmd []string
	mock := newSendKeysMock([]api.AgentInfo{agent}, &capturedCmd, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrTargetNotFound) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrTargetNotFound", err)
	}
	if len(capturedCmd) != 0 {
		t.Fatalf("expected no Exec calls on a missing agent_id label, got %v", capturedCmd)
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

	var capturedCmd []string
	mock := newSendKeysMock([]api.AgentInfo{agent}, &capturedCmd, nil)
	mgr := &AgentManager{Runtime: mock}

	err := mgr.SendKeys(context.Background(), "proj-1", "test-agent", "agent-abc", "C-c")
	if !errors.Is(err, agentkeys.ErrAgentNotRunning) {
		t.Fatalf("SendKeys error = %v, want agentkeys.ErrAgentNotRunning", err)
	}
	if len(capturedCmd) != 0 {
		t.Fatalf("expected no Exec calls when agent is not running, got %v", capturedCmd)
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
	if errors.Is(err, agentkeys.ErrTargetNotFound) || errors.Is(err, agentkeys.ErrAgentNotRunning) || errors.Is(err, agentkeys.ErrTerminalNotReady) {
		t.Fatalf("an ambiguous Exec failure must never be reported as one of the three proven-before-execution sentinels, got: %v", err)
	}
	if !errors.Is(err, execErr) {
		t.Fatalf("expected the underlying Exec error to be wrapped, got: %v", err)
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
	// (e.g. a slow concurrent message delivery).
	lock := mgr.injectionLock("test-agent", "proj-1")
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendKeys error = %v, want context.DeadlineExceeded", err)
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
func TestSendKeys_ConcurrentWithInterruptMessage_NoInterleave(t *testing.T) {
	agent := runningAgent()

	var mu sync.Mutex
	var active string // "" when nobody is mid-injection
	var violated bool
	enter := func(who string) {
		mu.Lock()
		defer mu.Unlock()
		if active != "" && active != who {
			violated = true
		}
		active = who
	}
	leaveDelay := 3 * time.Millisecond

	mock := &runtime.MockRuntime{
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{agent}, nil
		},
		// Tags which logical call (SendKeys vs. the interrupt message) is
		// currently inside an Exec invocation via a ctx value set by each
		// goroutine below, and fails the test if the two ever overlap.
		ExecFunc: func(ctx context.Context, id string, cmd []string) (string, error) {
			enter(currentCaller(ctx))
			time.Sleep(leaveDelay)
			mu.Lock()
			active = ""
			mu.Unlock()
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
		// Same projectID as the SendKeys call above ("proj-1") so both calls
		// contend on the same injectionLock key — a different projectID
		// would silently prove nothing, since the two calls would never
		// share a lock at all.
		if err := mgr.deliverImmediate(ctx, "test-agent", "proj-1", "", true); err != nil {
			t.Errorf("deliverImmediate failed: %v", err)
		}
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if violated {
		t.Fatal("SendKeys and an interrupt message interleaved their tmux Exec calls for the same target")
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
