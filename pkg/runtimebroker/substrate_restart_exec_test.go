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

package runtimebroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Broker-level coverage of the control path after a restart: exec,
// message delivery and reset-auth for an agent started before the restart
// reach the actor with the persisted control token, and no HTTP response
// body or broker log line ever carries that token or an exec secret, on
// the success or the error paths of Run, Exec, Message, reset-auth, List
// and Delete.

const restartExecSecret = "sk-restart-exec-secret-0123456789"

// recordingActorServer stands in for sciontool substrate-serve and records
// what the broker sent it.
type recordingActorServer struct {
	*httptest.Server

	mu             sync.Mutex
	bootstrapToken string
	execs          []recordedExec
	execStatus     int
	execBody       map[string]any
}

type recordedExec struct {
	auth  string
	argv  []string
	stdin string
}

func newRecordingActorServer(t *testing.T) *recordingActorServer {
	t.Helper()
	s := &recordingActorServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/scion/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "awaiting-bootstrap"})
	})
	mux.HandleFunc("/scion/v1/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ControlToken string `json:"control_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.bootstrapToken = req.ControlToken
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/scion/v1/exec", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Argv  []string `json:"argv"`
			Stdin string   `json:"stdin"` // base64, as encoding/json marshals []byte
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		stdin, _ := base64.StdEncoding.DecodeString(req.Stdin)
		s.mu.Lock()
		s.execs = append(s.execs, recordedExec{auth: r.Header.Get("Authorization"), argv: req.Argv, stdin: string(stdin)})
		code, body := s.execStatus, s.execBody
		s.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		if body == nil {
			body = map[string]any{"stdout": "", "exit_code": 0, "stdin_supported": len(stdin) > 0}
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *recordingActorServer) set(code int, body map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execStatus, s.execBody = code, body
}

func (s *recordingActorServer) recorded() []recordedExec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.execs)
}

// restartExecBroker is a broker server over a substrate runtime whose
// actors are served by actor, with every broker logger captured.
type restartExecBroker struct {
	srv   *Server
	fc    *fakeSubstrateControlClient
	state *k8sfake.Clientset
	actor *recordingActorServer
	logs  *lockedBuffer
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newRestartExecBroker builds the broker. Loggers derived from the default
// slog handler at package init (the runtime and agent packages') write
// through the standard log package, so redirecting its output captures
// them alongside the server's own loggers.
func newRestartExecBroker(t *testing.T, fc *fakeSubstrateControlClient, state *k8sfake.Clientset, logs *lockedBuffer) *restartExecBroker {
	t.Helper()
	actor := newRecordingActorServer(t)
	rt := runtime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient(actor.URL), state, config.V1SubstrateConfig{StateNamespace: testSubstrateStateNamespace})
	mgr := agent.NewManager(rt)
	t.Cleanup(mgr.Close)
	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.ForceRuntime = ""
	srv := New(cfg, mgr, rt)
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv.agentLifecycleLog = logger
	srv.messageLog = logger
	srv.dedicatedMessageLog = nil
	return &restartExecBroker{srv: srv, fc: fc, state: state, actor: actor, logs: logs}
}

func captureStdLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// runRestartExecAgent starts agentSlug in project projb with a secret in
// its env, through the broker's runtime, as AgentManager.Start would.
func runRestartExecAgent(t *testing.T, b *restartExecBroker, agentSlug string) error {
	t.Helper()
	am := b.srv.manager.(*agent.AgentManager)
	labels := map[string]string{"scion.name": agentSlug, "scion.agent": "true"}
	for k, v := range projectkeys.ProjectNameLabels("projb") {
		labels[k] = v
	}
	for k, v := range projectkeys.ProjectIDLabels(gapProjBID) {
		labels[k] = v
	}
	_, err := am.Runtime.Run(context.Background(), runtime.RunConfig{
		Name:         "projb--" + agentSlug,
		Project:      "projb",
		ProjectID:    gapProjBID,
		Image:        "us-docker.pkg.dev/proj/repo/scion-agent@sha256:" + strings.Repeat("a", 64),
		UnixUsername: "scion",
		NoAuth:       true,
		Env:          []string{"API_KEY=" + restartExecSecret},
		Labels:       labels,
		Annotations:  projectkeys.ProjectPathLabels(testProjectScionDir(t, "projb")),
	})
	return err
}

// persistedControlToken reads the control token from the agent's state
// Secret (the only object in the state namespace for it).
func persistedControlToken(t *testing.T, state *k8sfake.Clientset) string {
	t.Helper()
	list, err := state.CoreV1().Secrets(testSubstrateStateNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("%d state objects, want 1", len(list.Items))
	}
	tok := string(list.Items[0].Data["control_token"])
	if tok == "" {
		t.Fatal("state object has no control_token")
	}
	return tok
}

func jsonRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(method, path, bytes.NewReader(data))
}

func failStateVerb(cs *k8sfake.Clientset, verb, embedded string) (undo func()) {
	cs.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("backend failure near " + embedded))
	})
	return func() { cs.ReactionChain = cs.ReactionChain[1:] }
}

// TestSubstrateBroker_RealRestart_ExecMessageResetAuth: after a restart
// (same runtime with its cache wiped, and a new broker and runtime over the
// same cluster), exec, message and reset-auth of a pre-restart agent all
// reach the actor authorized with the persisted control token, and exec
// output is redacted with the restored exec secrets.
func TestSubstrateBroker_RealRestart_ExecMessageResetAuth(t *testing.T) {
	for _, newProcess := range []bool{false, true} {
		name := "same runtime, cache wiped"
		if newProcess {
			name = "new runtime instance"
		}
		t.Run(name, func(t *testing.T) {
			t.Cleanup(runtime.WipeSubstrateAgentStateForTest())
			logs := captureStdLog(t)
			fc := newFakeSubstrateControlClient(&substrateEgressRecorder{})
			state := k8sfake.NewClientset()
			b := newRestartExecBroker(t, fc, state, logs)
			if err := runRestartExecAgent(t, b, "dev"); err != nil {
				t.Fatalf("Run error = %v", err)
			}
			token := persistedControlToken(t, state)
			if b.actor.bootstrapToken != token {
				t.Fatal("bootstrap token differs from the persisted control_token")
			}

			simulateBrokerRestart(t)
			if newProcess {
				b = newRestartExecBroker(t, fc, state, logs)
			}
			wantAuth := "Bearer " + token

			// Exec: output echoes the secret and must come back redacted.
			b.actor.set(0, map[string]any{"stdout": "API_KEY=" + restartExecSecret, "exit_code": 0})
			w := httptest.NewRecorder()
			b.srv.execCommand(w, jsonRequest(t, http.MethodPost, "/api/v1/agents/dev/exec", ExecRequest{Command: []string{"env"}}), "dev", gapProjBID)
			if w.Code != http.StatusOK {
				t.Fatalf("exec after restart: status=%d body=%s, want 200", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), restartExecSecret) || !strings.Contains(w.Body.String(), "redacted]") {
				t.Errorf("exec after restart body = %s, want the secret redacted", w.Body.String())
			}
			b.actor.set(0, nil)

			// Message (interrupt: delivered synchronously).
			const msg = "hello after restart"
			before := len(b.actor.recorded())
			w = httptest.NewRecorder()
			b.srv.sendMessage(w, jsonRequest(t, http.MethodPost, "/api/v1/agents/dev/message", MessageRequest{Message: msg, Interrupt: true}), "dev", gapProjBID)
			if w.Code != http.StatusOK {
				t.Fatalf("message after restart: status=%d body=%s, want 200", w.Code, w.Body.String())
			}
			delivered := false
			for _, e := range b.actor.recorded()[before:] {
				if e.stdin == msg || slices.ContainsFunc(e.argv, func(a string) bool { return strings.Contains(a, msg) }) {
					delivered = true
				}
			}
			if !delivered {
				t.Errorf("message after restart was never delivered to the actor: %+v", b.actor.recorded()[before:])
			}

			// Reset-auth: the new hub token arrives on stdin.
			const hubToken = "fresh-hub-token-after-restart"
			before = len(b.actor.recorded())
			w = httptest.NewRecorder()
			b.srv.resetAuth(w, jsonRequest(t, http.MethodPost, "/api/v1/agents/dev/reset-auth", ResetAuthRequest{Token: hubToken}), "dev", gapProjBID)
			if w.Code != http.StatusOK {
				t.Fatalf("reset-auth after restart: status=%d body=%s, want 200", w.Code, w.Body.String())
			}
			if !slices.ContainsFunc(b.actor.recorded()[before:], func(e recordedExec) bool { return e.stdin == hubToken }) {
				t.Error("reset-auth after restart never wrote the token to the actor")
			}

			for _, e := range b.actor.recorded() {
				if e.auth != wantAuth {
					t.Fatalf("an exec after restart was not authorized with the persisted control token (argv %v)", e.argv)
				}
			}
		})
	}
}

// TestSubstrateBroker_ExecCredentialRejectedIsOpaque: a 401 from the actor
// is a 500 with the fixed exec failure body, the rejection is in the
// broker log, and the exec is not retried.
func TestSubstrateBroker_ExecCredentialRejectedIsOpaque(t *testing.T) {
	t.Cleanup(runtime.WipeSubstrateAgentStateForTest())
	logs := captureStdLog(t)
	b := newRestartExecBroker(t, newFakeSubstrateControlClient(&substrateEgressRecorder{}), k8sfake.NewClientset(), logs)
	if err := runRestartExecAgent(t, b, "dev"); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	simulateBrokerRestart(t)

	b.actor.set(http.StatusUnauthorized, map[string]any{"error": "body-marker"})
	w := httptest.NewRecorder()
	b.srv.execCommand(w, jsonRequest(t, http.MethodPost, "/api/v1/agents/dev/exec", ExecRequest{Command: []string{"true"}}), "dev", gapProjBID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("exec on a 401: status=%d body=%s, want 500", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Failed to execute command on agent") || strings.Contains(w.Body.String(), "credential") || strings.Contains(w.Body.String(), "body-marker") {
		t.Errorf("exec on a 401 body = %s, want only the fixed opaque failure text", w.Body.String())
	}
	if !strings.Contains(logs.String(), "control credential rejected") {
		t.Errorf("broker log does not record the credential rejection:\n%s", logs.String())
	}
	if n := len(b.actor.recorded()); n != 1 {
		t.Errorf("exec on a 401 reached the actor %d times, want 1 (no retry)", n)
	}
}

// TestSubstrateBroker_RestartNoCredentialLeak captures every broker log
// line and every HTTP response body across Run, Exec, Message, reset-auth,
// List and Delete, on success and on error paths (store failures whose
// backend message embeds the credentials, actor failures whose bodies
// echo them, a 401), and asserts that neither the control token nor the
// exec secret appears anywhere.
func TestSubstrateBroker_RestartNoCredentialLeak(t *testing.T) {
	t.Cleanup(runtime.WipeSubstrateAgentStateForTest())
	logs := captureStdLog(t)
	fc := newFakeSubstrateControlClient(&substrateEgressRecorder{})
	state := k8sfake.NewClientset()
	b := newRestartExecBroker(t, fc, state, logs)

	var surfaced []string
	note := func(what string, w *httptest.ResponseRecorder) {
		surfaced = append(surfaced, what+": "+w.Body.String())
	}
	noteErr := func(err error) {
		if err != nil {
			surfaced = append(surfaced, err.Error())
		}
	}

	noteErr(runRestartExecAgent(t, b, "dev"))
	token := persistedControlToken(t, state)
	secrets := []string{token, restartExecSecret}
	embedded := token + " " + restartExecSecret
	echo := map[string]any{"stdout": embedded, "stderr": embedded, "exit_code": 0, "stdin_supported": true}
	failing := map[string]any{"stdout": embedded, "stderr": embedded, "exit_code": 2, "stdin_supported": true}

	exec := func(what string) {
		simulateBrokerRestart(t)
		w := httptest.NewRecorder()
		b.srv.execCommand(w, jsonRequest(t, http.MethodPost, "/api/v1/agents/dev/exec", ExecRequest{Command: []string{"env"}}), "dev", gapProjBID)
		note("exec "+what, w)
	}
	message := func(what string) {
		simulateBrokerRestart(t)
		w := httptest.NewRecorder()
		b.srv.sendMessage(w, jsonRequest(t, http.MethodPost, "/api/v1/agents/dev/message", MessageRequest{Message: "hi", Interrupt: true}), "dev", gapProjBID)
		note("message "+what, w)
	}
	resetAuth := func(what string) {
		simulateBrokerRestart(t)
		w := httptest.NewRecorder()
		b.srv.resetAuth(w, jsonRequest(t, http.MethodPost, "/api/v1/agents/dev/reset-auth", ResetAuthRequest{Token: "new-hub-token"}), "dev", gapProjBID)
		note("reset-auth "+what, w)
	}
	list := func(what string) {
		simulateBrokerRestart(t)
		w := httptest.NewRecorder()
		b.srv.listAgents(w, httptest.NewRequest(http.MethodGet, "/api/v1/agents?projectId="+gapProjBID, nil))
		note("list "+what, w)
	}
	controlPath := func(what string) {
		exec(what)
		message(what)
		resetAuth(what)
	}

	// Success paths (the actor echoes the credentials in its output).
	b.actor.set(0, echo)
	controlPath("success")
	list("success")

	// Actor-side failures.
	b.actor.set(0, failing)
	controlPath("command failure")
	b.actor.set(http.StatusInternalServerError, echo)
	controlPath("non-2xx")
	b.actor.set(http.StatusUnauthorized, echo)
	controlPath("401")
	b.actor.set(0, nil)

	// Store failures on the read-through and on List.
	undo := failStateVerb(state, "get", embedded)
	controlPath("store get failure")
	undo()
	undo = failStateVerb(state, "list", embedded)
	list("store list failure")
	controlPath("store list failure")
	undo()

	// Run failing at the state claim.
	undo = failStateVerb(state, "create", embedded)
	noteErr(runRestartExecAgent(t, b, "dev-2"))
	undo()

	// Delete failing at the store read, then succeeding.
	undo = failStateVerb(state, "get", embedded)
	simulateBrokerRestart(t)
	w := httptest.NewRecorder()
	b.srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil), "dev", gapProjBID)
	if w.Code < 400 {
		t.Errorf("delete with an unreadable store: status=%d, want an error", w.Code)
	}
	note("delete store get failure", w)
	undo()
	simulateBrokerRestart(t)
	w = httptest.NewRecorder()
	b.srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil), "dev", gapProjBID)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete after restart: status=%d body=%s, want 204", w.Code, w.Body.String())
	}
	note("delete success", w)

	// Control path against a deleted agent.
	controlPath("after delete")

	// The capture is live: the store-failure detail reached the broker log.
	if !strings.Contains(logs.String(), "read agent state") {
		t.Fatalf("broker log capture missed the read-through store failure:\n%s", logs.String())
	}

	all := strings.Join(surfaced, "\n") + "\n" + logs.String()
	for _, s := range secrets {
		if strings.Contains(all, s) {
			t.Errorf("credential material leaked into an HTTP body or log line:\n%s", all)
		}
	}
	// Sanity: the corpus really covers each operation's body.
	for _, op := range []string{"exec success", "message success", "reset-auth success", "list success", "delete success", "exec 401", "list store list failure"} {
		if !strings.Contains(all, op+": ") {
			t.Errorf("no captured body for %q", op)
		}
	}
}
