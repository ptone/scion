// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// Tests for the control path after a broker restart: Exec and
// ExecWithStdin (the transport for Message and reset-auth) read the control
// token and exec secrets through from the durable agent state store when
// the in-memory cache is empty, use only committed state, and treat a 401
// from the actor as an explicit credential rejection.

// stateTestExecSecrets is every exec secret value stateTestRunConfig
// carries.
var stateTestExecSecrets = []string{stateTestSecretValue, stateTestHarnessSecret}

// runPersistedAgent runs name through h and returns its id and the
// committed state object, after pinning that the token the actor received
// at bootstrap is exactly the persisted one.
func runPersistedAgent(t *testing.T, h *stateHarness, name string) (string, *substrateAgentState) {
	t.Helper()
	ctx := context.Background()
	id, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, name))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	persisted, err := h.store.Get(ctx, id)
	if err != nil {
		t.Fatalf("state after Run: Get error = %v", err)
	}
	if persisted.Phase != substrateStateCommitted || persisted.ControlToken == "" {
		t.Fatalf("state after Run = {Phase:%q token set:%v}, want committed with a token", persisted.Phase, persisted.ControlToken != "")
	}
	h.fa.mu.Lock()
	boot := h.fa.lastBootstrap
	h.fa.mu.Unlock()
	if boot == nil {
		t.Fatal("actor received no bootstrap")
	}
	if boot.ControlToken != persisted.ControlToken {
		t.Fatal("control token sent in the bootstrap body differs from the persisted control_token")
	}
	return id, persisted
}

// echoSecretsOutput is control-server output that repeats every exec
// secret, as a command printing its own environment would.
func echoSecretsOutput() string {
	return "API_KEY=" + stateTestSecretValue + " HARNESS_TOKEN=" + stateTestHarnessSecret
}

func assertRedacted(t *testing.T, what, got string) {
	t.Helper()
	for _, s := range stateTestExecSecrets {
		if strings.Contains(got, s) {
			t.Errorf("%s leaks an exec secret value: %s", what, got)
		}
	}
	if !strings.Contains(got, "redacted]") {
		t.Errorf("%s = %q, want the redaction marker in place of the secrets", what, got)
	}
}

func (h *stateHarness) lastExec() (*execRequest, string) {
	h.fa.mu.Lock()
	defer h.fa.mu.Unlock()
	return h.fa.lastExec, h.fa.lastExecAuth
}

// TestSubstrateRestart_ExecAfterCacheWipeUsesPersistedCredentials is the
// exec-after-restart contract, from the same runtime with its cache wiped
// and from a second runtime instance: Exec authenticates with the persisted
// control token, and both its output and its error are redacted with the
// exec secrets restored from the state object.
func TestSubstrateRestart_ExecAfterCacheWipeUsesPersistedCredentials(t *testing.T) {
	for _, newProcess := range []bool{false, true} {
		name := "same runtime, cache wiped"
		if newProcess {
			name = "new runtime instance"
		}
		t.Run(name, func(t *testing.T) {
			h := newStateHarness(t)
			ctx := context.Background()
			id, persisted := runPersistedAgent(t, h, "exec-after-restart")
			rt := h.rt
			if newProcess {
				rt = h.secondRuntime()
			}

			// Success: stdout echoes the secrets.
			t.Cleanup(WipeSubstrateAgentStateForTest())
			h.fa.execResp = execResponse{Stdout: echoSecretsOutput(), ExitCode: 0}
			out, err := rt.Exec(ctx, id, []string{"env"})
			if err != nil {
				t.Fatalf("Exec() after restart error = %v, want success via read-through", err)
			}
			if _, auth := h.lastExec(); auth != "Bearer "+persisted.ControlToken {
				t.Error("Exec() after restart did not authenticate with the persisted control token")
			}
			assertRedacted(t, "Exec() stdout after restart", out)

			// The read-through filled the cache.
			substrateAgentStateMu.Lock()
			cachedToken, cachedSecrets := substrateControlTokens[id], substrateExecSecrets[id]
			cachedRecord := substrateAgentRecords[persisted.ActorUID]
			substrateAgentStateMu.Unlock()
			if cachedToken != persisted.ControlToken || fmt.Sprint(cachedSecrets) != fmt.Sprint(persisted.ExecSecrets) || cachedRecord == nil {
				t.Error("read-through did not fill the cache with the persisted token, exec secrets and record")
			}

			// Error: a failing command's stderr echoes the secrets, after
			// a second wipe so this path reads through too.
			t.Cleanup(WipeSubstrateAgentStateForTest())
			h.fa.execResp = execResponse{Stderr: echoSecretsOutput(), ExitCode: 3}
			_, err = rt.Exec(ctx, id, []string{"false"})
			if err == nil {
				t.Fatal("Exec() of a failing command error = nil, want the exit error")
			}
			assertRedacted(t, "Exec() error after restart", err.Error())
		})
	}
}

// TestSubstrateRestart_ExecWithStdinAfterCacheWipe covers the reset-auth
// and message transport: ExecWithStdin reads the token through, delivers
// stdin, and redacts its output.
func TestSubstrateRestart_ExecWithStdinAfterCacheWipe(t *testing.T) {
	h := newStateHarness(t)
	id, persisted := runPersistedAgent(t, h, "stdin-after-restart")
	t.Cleanup(WipeSubstrateAgentStateForTest())

	h.fa.execResp = execResponse{Stdout: echoSecretsOutput(), StdinSupported: true}
	out, err := h.secondRuntime().ExecWithStdin(context.Background(), id, []string{"sh", "-c", "cat > f"}, strings.NewReader("fresh-hub-token"))
	if err != nil {
		t.Fatalf("ExecWithStdin() after restart error = %v, want success via read-through", err)
	}
	req, auth := h.lastExec()
	if auth != "Bearer "+persisted.ControlToken {
		t.Error("ExecWithStdin() after restart did not authenticate with the persisted control token")
	}
	if req == nil || string(req.Stdin) != "fresh-hub-token" {
		t.Errorf("ExecWithStdin() after restart did not deliver stdin: %+v", req)
	}
	assertRedacted(t, "ExecWithStdin() stdout after restart", out)
}

// TestSubstrateRestart_ExecUnusableStateIsNoControlToken: a state object
// that is pending (start not committed) or deleting gives the existing
// no-control-token error and never reaches the actor.
func TestSubstrateRestart_ExecUnusableStateIsNoControlToken(t *testing.T) {
	for _, phase := range []substrateStatePhase{substrateStatePending, substrateStateDeleting} {
		t.Run(string(phase), func(t *testing.T) {
			h := newStateHarness(t)
			ctx := context.Background()
			atespace := substrateAtespaceName(stateTestProjectID)
			st := testState(atespace, "half")
			st.ActorUID = "uid-half"
			if err := h.store.Create(ctx, st); err != nil {
				t.Fatal(err)
			}
			if phase != substrateStatePending {
				st.Phase = phase
				if err := h.store.Update(ctx, st); err != nil {
					t.Fatal(err)
				}
			}

			_, err := h.rt.Exec(ctx, st.ID, []string{"true"})
			if err == nil || !strings.Contains(err.Error(), "no control token cached") {
				t.Errorf("Exec() of a %s agent error = %v, want the no-control-token error", phase, err)
			}
			_, err = h.rt.ExecWithStdin(ctx, st.ID, []string{"cat"}, strings.NewReader("x"))
			if err == nil || !strings.Contains(err.Error(), "no control token cached") {
				t.Errorf("ExecWithStdin() of a %s agent error = %v, want the no-control-token error", phase, err)
			}
			if h.calls("exec") != 0 {
				t.Errorf("exec reached the actor %d time(s) for a %s agent, want 0", h.calls("exec"), phase)
			}
			substrateAgentStateMu.Lock()
			_, cached := substrateControlTokens[st.ID]
			substrateAgentStateMu.Unlock()
			if cached {
				t.Errorf("a %s state object's token was cached", phase)
			}
		})
	}
}

// TestSubstrateRestart_ExecStoreFailureIsOpaque: a store read failure on a
// cache miss fails the exec before any actor call, with an error that
// names neither credentials nor the backend's own message, and that is not
// mistaken for a missing token or a missing agent.
func TestSubstrateRestart_ExecStoreFailureIsOpaque(t *testing.T) {
	h := newStateHarness(t)
	id, persisted := runPersistedAgent(t, h, "store-down")
	t.Cleanup(WipeSubstrateAgentStateForTest())
	h.cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("backend detail %s %s", persisted.ControlToken, stateTestSecretValue))
	})

	_, err := h.rt.Exec(context.Background(), id, []string{"true"})
	if err == nil {
		t.Fatal("Exec() with an unreadable store error = nil, want a runtime error")
	}
	msg := err.Error()
	for _, s := range append([]string{persisted.ControlToken, "backend detail"}, stateTestExecSecrets...) {
		if strings.Contains(msg, s) {
			t.Errorf("Exec() store-failure error carries %q: %s", s, msg)
		}
	}
	if strings.Contains(msg, "no control token") || strings.Contains(msg, "not found") {
		t.Errorf("Exec() store-failure error = %q, want a store read failure, not a missing token or agent", msg)
	}
	if h.calls("exec") != 0 {
		t.Errorf("exec reached the actor %d time(s) with an unreadable store, want 0", h.calls("exec"))
	}
}

// TestSubstrateRestart_ExecCredentialRejected: a 401 from a committed
// agent is an explicit credential rejection, not a retry: one actor call,
// errControlCredentialRejected, and none of the response body echoed.
func TestSubstrateRestart_ExecCredentialRejected(t *testing.T) {
	const bodyMarker = "body-marker-must-not-surface"
	cases := map[string]func(rt *SubstrateRuntime, id string) error{
		"Exec": func(rt *SubstrateRuntime, id string) error {
			_, err := rt.Exec(context.Background(), id, []string{"true"})
			return err
		},
		"ExecWithStdin": func(rt *SubstrateRuntime, id string) error {
			_, err := rt.ExecWithStdin(context.Background(), id, []string{"cat"}, strings.NewReader("x"))
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			h := newStateHarness(t)
			id, _ := runPersistedAgent(t, h, "rejected")
			t.Cleanup(WipeSubstrateAgentStateForTest())
			h.fa.execStatus = http.StatusUnauthorized
			h.fa.execResp = execResponse{Stderr: bodyMarker + " " + stateTestSecretValue}
			before := h.calls("exec")

			err := call(h.rt, id)
			if !errors.Is(err, errControlCredentialRejected) {
				t.Fatalf("%s() on a 401 error = %v, want errControlCredentialRejected", name, err)
			}
			if n := h.calls("exec") - before; n != 1 {
				t.Errorf("%s() on a 401 made %d actor calls, want exactly 1 (no retry)", name, n)
			}
			if strings.Contains(err.Error(), bodyMarker) || strings.Contains(err.Error(), stateTestSecretValue) {
				t.Errorf("%s() on a 401 echoed the response body: %v", name, err)
			}
			if strings.Contains(err.Error(), "may be running an image older") {
				t.Errorf("%s() on a 401 was reported as version skew: %v", name, err)
			}
		})
	}
}

// TestSubstrateRestart_ExecNoCredentialLeak extends the no-leak contract to
// the control path: Exec and ExecWithStdin after a restart, on success and
// on every error path (command failure, non-2xx, 401, store failure,
// unusable state), never surface the control token or an exec secret in a
// returned value, error or log line.
func TestSubstrateRestart_ExecNoCredentialLeak(t *testing.T) {
	logs := captureRuntimeLog(t)
	h := newStateHarness(t)
	ctx := context.Background()
	id, persisted := runPersistedAgent(t, h, "leaky")
	secrets := append([]string{persisted.ControlToken}, stateTestExecSecrets...)
	echo := echoSecretsOutput()

	var surfaced []string
	note := func(out string, err error) {
		surfaced = append(surfaced, out)
		if err != nil {
			surfaced = append(surfaced, err.Error())
		}
	}
	execBoth := func() {
		t.Cleanup(WipeSubstrateAgentStateForTest())
		note(h.rt.Exec(ctx, id, []string{"env"}))
		t.Cleanup(WipeSubstrateAgentStateForTest())
		note(h.rt.ExecWithStdin(ctx, id, []string{"cat"}, strings.NewReader("x")))
	}

	h.fa.execResp = execResponse{Stdout: echo, Stderr: echo, StdinSupported: true}
	execBoth() // success
	h.fa.execResp = execResponse{Stdout: echo, Stderr: echo, StdinSupported: true, ExitCode: 1}
	execBoth() // command failure
	h.fa.execStatus = http.StatusInternalServerError
	execBoth() // non-2xx with a secret-bearing body
	h.fa.execStatus = http.StatusUnauthorized
	execBoth() // credential rejected
	h.fa.execStatus = 0

	h.cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("backend failure near " + strings.Join(secrets, " ")))
	})
	execBoth() // store failure
	h.cs.ReactionChain = h.cs.ReactionChain[1:]

	st, err := h.store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	st.Phase = substrateStateDeleting
	if err := h.store.Update(ctx, st); err != nil {
		t.Fatal(err)
	}
	execBoth() // unusable (deleting) state

	all := strings.Join(surfaced, "\n") + "\n" + logs.String()
	for _, s := range secrets {
		if strings.Contains(all, s) {
			t.Errorf("credential material leaked into an exec result, error or log line:\n%s", all)
		}
	}
}
