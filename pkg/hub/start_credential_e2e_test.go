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

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/wsprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hub/broker start-boundary contract test (retro #6663, R1), in process.
//
// The hub dispatcher (real env and secret resolution, real credential
// issue and run record) talks to a real runtimebroker.Server, whose
// create, start and restart handlers build the agent manager's
// StartOptions. The agent manager is a stand-in that records its
// StartOptions; pkg/agent's refusal of a start without a hub-issued
// credential is covered there (ErrNoHubToken).
//
// Each lifecycle path (create, start, resume, restart) runs on both hub
// transports:
//   - http: HTTPRuntimeBrokerClient over a real HTTP server;
//   - control-channel: ControlChannelBrokerClient whose tunnel serves each
//     request envelope through the broker's HTTP handlers, as the broker's
//     control channel does (runtimebroker serveTunneledRequest).
//
// The test checks the contract at two points: the request body the broker
// handler receives (the wire), and the StartOptions the broker hands the
// agent manager. Neither half is a fixture: a hub that stops sending a
// field the broker needs, or a broker that stops honoring one, fails here.
// The absent-field case (an older hub that sends no classification or no
// agent token) is pinned by TestStartContract_E2E_AbsentFieldFailsClosed.
//
// This file is the R292 boundary test set's anchor and runs as its own CI
// job (.github/workflows/ci.yml, job start-contract).

const (
	contractPlainKey   = "E2E_CONTRACT_PLAIN"
	contractPlainValue = "plain-value"
	contractEnvSecret  = "E2E_CONTRACT_SECRET_ENV"
	contractEnvValue   = "env-secret-value"
	contractFileSecret = "e2e-contract-file"
	contractFileTarget = "/home/scion/.e2e-contract"
	contractFileValue  = "file-secret-value"
)

// credentialStartManager is runLabelManager with a Start that records its
// options and runs the entry with the run ID it was given, as pkg/agent's
// Start labels the container.
type credentialStartManager struct {
	runLabelManager
	startMu sync.Mutex
	starts  []api.StartOptions
}

func (m *credentialStartManager) Start(_ context.Context, opts api.StartOptions) (*api.AgentInfo, error) {
	m.startMu.Lock()
	m.starts = append(m.starts, opts)
	n := len(m.starts)
	m.startMu.Unlock()
	cid := fmt.Sprintf("cid-start-%d", n)
	m.run(opts.Name, cid, opts.HubProjectID, opts.ProjectPath, opts.RunID)
	return &api.AgentInfo{Name: opts.Name, ContainerID: cid, Phase: "running", RunID: opts.RunID}, nil
}

func (m *credentialStartManager) lastStart(t *testing.T) api.StartOptions {
	t.Helper()
	m.startMu.Lock()
	defer m.startMu.Unlock()
	require.NotEmpty(t, m.starts, "the broker never called the agent manager's Start")
	return m.starts[len(m.starts)-1]
}

// wireRequest is one create, start or restart body as the broker handler
// received it. raw keeps the top-level keys so a test can tell an absent
// field from an empty one.
type wireRequest struct {
	Transport string
	Path      string
	raw       map[string]json.RawMessage
	body      struct {
		ResolvedEnv        map[string]string      `json:"resolvedEnv"`
		EnvClassifications map[string]api.EnvKind `json:"envClassifications"`
		ResolvedSecrets    []api.ResolvedSecret   `json:"resolvedSecrets"`
		AgentToken         string                 `json:"agentToken"`
		RunID              string                 `json:"runId"`
	}
}

func (w *wireRequest) has(key string) bool {
	_, ok := w.raw[key]
	return ok
}

// wireRecorder sits in front of the broker's handlers. It records every
// lifecycle request body and, when strip is set, removes those top-level
// keys before the broker sees the body (an older hub that does not send
// them).
type wireRecorder struct {
	next  http.Handler
	strip []string
	mu    sync.Mutex
	reqs  []*wireRequest
}

func isLifecyclePath(method, path string) bool {
	if method != http.MethodPost || !strings.HasPrefix(path, "/api/v1/agents") {
		return false
	}
	return path == "/api/v1/agents" || strings.HasSuffix(path, "/start") || strings.HasSuffix(path, "/restart")
}

func (rec *wireRecorder) serve(transport string, w http.ResponseWriter, r *http.Request) {
	if !isLifecyclePath(r.Method, r.URL.Path) {
		rec.next.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wr := &wireRequest{Transport: transport, Path: r.URL.Path}
	if err := json.Unmarshal(body, &wr.raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &wr.body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(rec.strip) > 0 {
		forward := make(map[string]json.RawMessage, len(wr.raw))
		for k, v := range wr.raw {
			forward[k] = v
		}
		for _, k := range rec.strip {
			delete(forward, k)
		}
		if body, err = json.Marshal(forward); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	rec.mu.Lock()
	rec.reqs = append(rec.reqs, wr)
	rec.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rec.next.ServeHTTP(w, r)
}

func (rec *wireRecorder) last(t *testing.T) *wireRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotEmpty(t, rec.reqs, "the broker received no create, start or restart request")
	return rec.reqs[len(rec.reqs)-1]
}

// inProcessTunnel is a control-channel tunnel to one broker that serves
// each request envelope through the broker's HTTP handlers, as the
// broker's control channel does (runtimebroker serveTunneledRequest):
// rebuild the request from the envelope, serve it, return the response
// as an envelope. The hub side is the real ControlChannelBrokerClient.
type inProcessTunnel struct {
	brokerID string
	rec      *wireRecorder
}

func (t *inProcessTunnel) IsConnected(brokerID string) bool { return brokerID == t.brokerID }

func (t *inProcessTunnel) TunnelRequest(ctx context.Context, brokerID string, req *wsprotocol.RequestEnvelope) (*wsprotocol.ResponseEnvelope, error) {
	if brokerID != t.brokerID {
		return nil, fmt.Errorf("broker %s not connected", brokerID)
	}
	path := req.Path
	if req.Query != "" {
		path += "?" + req.Query
	}
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq := httptest.NewRequest(req.Method, path, body).WithContext(ctx)
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	t.rec.serve(transportControlChannel, w, httpReq)
	res := w.Result()
	respBody, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	headers := make(map[string]string, len(res.Header))
	for k := range res.Header {
		headers[k] = res.Header.Get(k)
	}
	return wsprotocol.NewResponseEnvelope(req.RequestID, res.StatusCode, headers, respBody), nil
}

const (
	transportHTTP           = "http"
	transportControlChannel = "control-channel"
)

var contractTransports = []string{transportHTTP, transportControlChannel}

// contractPath is one lifecycle path across the boundary.
type contractPath struct {
	name     string
	create   bool // credential travels in the create request's agentToken
	running  bool // the agent's container is up (restart stops it first)
	resume   bool
	dispatch func(ctx context.Context, d *HTTPAgentDispatcher, a *store.Agent) error
}

var contractPaths = []contractPath{
	{
		name:   "create",
		create: true,
		dispatch: func(ctx context.Context, d *HTTPAgentDispatcher, a *store.Agent) error {
			_, err := d.DispatchAgentCreate(ctx, a)
			return err
		},
	},
	{
		name: "start",
		dispatch: func(ctx context.Context, d *HTTPAgentDispatcher, a *store.Agent) error {
			return d.DispatchAgentStart(ctx, a, "", false)
		},
	},
	{
		name:   "resume",
		resume: true,
		dispatch: func(ctx context.Context, d *HTTPAgentDispatcher, a *store.Agent) error {
			return d.DispatchAgentStart(ctx, a, "", true)
		},
	},
	{
		name:    "restart",
		running: true,
		dispatch: func(ctx context.Context, d *HTTPAgentDispatcher, a *store.Agent) error {
			return d.DispatchAgentRestart(ctx, a)
		},
	},
}

// contractFixture is a hub store, dispatcher and token service wired to a
// real broker over one transport.
type contractFixture struct {
	store store.Store
	d     *HTTPAgentDispatcher
	svc   *AgentTokenService
	mgr   *credentialStartManager
	wire  *wireRecorder
	agent *store.Agent
}

func newContractFixture(t *testing.T, transport string, p contractPath, strip ...string) *contractFixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	t.Chdir(workDir)

	// A linked project on the broker whose settings select the mock
	// runtime, so the broker resolves the stand-in manager.
	const slug, name = "e2e-cred", "dev"
	projectID := tid("project-e2e-cred")
	scionDir := filepath.Join(workDir, ".scion")
	require.NoError(t, os.MkdirAll(scionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(scionDir, "settings.yaml"), []byte(`schema_version: "1"
active_profile: local
profiles:
    local:
        runtime: mock
runtimes:
    mock:
        type: mock
`), 0o644))
	require.NoError(t, config.WriteProjectID(scionDir, projectID))

	mgr := &credentialStartManager{}
	cfg := runtimebroker.DefaultServerConfig()
	cfg.BrokerAuthEnabled = false // no hub keys here: serve the broker API without auth
	cfg.BrokerID = "e2e-cred-broker"
	cfg.BrokerName = "e2e-cred-broker"
	cfg.ForceRuntime = "mock"
	brokerSrv := runtimebroker.New(cfg, mgr, &runtime.MockRuntime{NameFunc: func() string { return "mock" }}, config.RuntimeBuildRecord{})
	wire := &wireRecorder{next: brokerSrv.Handler(), strip: strip}

	brokerID := tid("broker-e2e-cred")
	var client RuntimeBrokerClient
	endpoint := "http://control-channel-only.invalid"
	switch transport {
	case transportHTTP:
		httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wire.serve(transportHTTP, w, r)
		}))
		t.Cleanup(httpSrv.Close)
		endpoint = httpSrv.URL
		client = NewHTTPRuntimeBrokerClient()
	case transportControlChannel:
		client = &ControlChannelBrokerClient{manager: &inProcessTunnel{brokerID: brokerID, rec: wire}}
	default:
		t.Fatalf("unknown transport %q", transport)
	}

	s := createTestStore(t)
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Name: slug, Slug: slug}))
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: brokerID, Name: "e2e-cred-broker", Slug: "e2e-cred-broker", Endpoint: endpoint, Status: store.BrokerStatusOnline,
	}))
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: projectID, BrokerID: brokerID, BrokerName: "e2e-cred-broker",
		LocalPath: scionDir, Status: store.BrokerStatusOnline,
	}))
	ownerID := tid("user-e2e-cred")
	a := &store.Agent{
		ID: tid("agent-e2e-cred"), Name: name, Slug: name, ProjectID: projectID, RuntimeBrokerID: brokerID,
		OwnerID: ownerID,
		Phase:   "stopped",
		AppliedConfig: &store.AgentAppliedConfig{
			HarnessConfig: "claude",
			Env:           map[string]string{contractPlainKey: contractPlainValue},
		},
	}
	if p.create {
		a.Phase = "created"
	}
	if p.running {
		a.Phase = "running"
	}
	require.NoError(t, s.CreateAgent(ctx, a))
	if !p.create {
		_, err := s.SetAgentRunID(ctx, a.ID, "run-before", nil)
		require.NoError(t, err)
	}
	a, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	if p.running {
		mgr.run(name, "cid-before", projectID, scionDir, "run-before")
	}

	svc, err := NewAgentTokenService(AgentTokenConfig{})
	require.NoError(t, err)
	d := NewHTTPAgentDispatcherWithClient(s, client, false, slog.Default())
	d.SetTokenGenerator(runTokenGenerator{svc: svc})
	// One environment secret and one file secret, user scope (no
	// project-scope authorization in this fixture).
	d.SetSecretBackend(&mockSecretBackend{secrets: []secret.SecretWithValue{
		{SecretMeta: secret.SecretMeta{Name: "e2e-contract-env", SecretType: "environment", Target: contractEnvSecret, Scope: "user", ScopeID: ownerID, InjectionMode: "always"}, Value: contractEnvValue},
		{SecretMeta: secret.SecretMeta{Name: contractFileSecret, SecretType: "file", Target: contractFileTarget, Scope: "user", ScopeID: ownerID, InjectionMode: "always"}, Value: contractFileValue},
	}})

	return &contractFixture{store: s, d: d, svc: svc, mgr: mgr, wire: wire, agent: a}
}

// TestStartCredential_E2E_HubDispatchReachesAgentStart is the R1 contract
// test: for every lifecycle path on both transports, the broker receives
// the whole env, secret and classification contract and hands the agent
// manager the hub-issued credential.
func TestStartCredential_E2E_HubDispatchReachesAgentStart(t *testing.T) {
	for _, transport := range contractTransports {
		for _, p := range contractPaths {
			t.Run(transport+"/"+p.name, func(t *testing.T) {
				ctx := context.Background()
				f := newContractFixture(t, transport, p)
				require.NoError(t, p.dispatch(ctx, f.d, f.agent))

				// --- The wire: what the broker handler received. ---
				wire := f.wire.last(t)
				assert.Equal(t, transport, wire.Transport, "the request crossed the transport under test")
				require.True(t, wire.has("envClassifications"),
					"the hub sent no envClassifications on %s: the broker cannot accept a start credential without them (#6663)", p.name)
				cls := wire.body.EnvClassifications
				for k := range wire.body.ResolvedEnv {
					assert.Contains(t, cls, k, "resolved env key %s has no classification on the wire", k)
				}
				assert.Equal(t, contractPlainValue, wire.body.ResolvedEnv[contractPlainKey], "agent config env")
				assert.Equal(t, api.EnvKindPlain, cls[contractPlainKey], "agent config env is plain")
				assert.Equal(t, contractEnvValue, wire.body.ResolvedEnv[contractEnvSecret], "environment secret in resolved env")
				assert.Equal(t, api.EnvKindSecretFetchable, cls[contractEnvSecret], "environment secret classification")
				assert.Equal(t, f.agent.ID, wire.body.ResolvedEnv["SCION_AGENT_ID"])
				assert.Equal(t, f.agent.ProjectID, wire.body.ResolvedEnv["SCION_PROJECT_ID"])
				assertContractSecrets(t, wire.body.ResolvedSecrets, "wire resolvedSecrets")
				if p.create {
					assert.NotEmpty(t, wire.body.AgentToken, "create carries the credential in agentToken")
					assert.NotContains(t, wire.body.ResolvedEnv, startCredentialEnvKey, "create sends the credential only as agentToken")
				} else {
					assert.Empty(t, wire.body.AgentToken, "start and restart carry no agentToken field")
					assert.NotEmpty(t, wire.body.ResolvedEnv[startCredentialEnvKey], "start and restart carry the credential in resolved env")
					assert.Equal(t, api.EnvKindSecretBootstrap, cls[startCredentialEnvKey],
						"the credential must be classified secret-bootstrap or the broker drops it (#6663)")
				}
				assert.NotEmpty(t, wire.body.RunID, "the hub sends the run it minted")

				// --- The agent manager: what the broker handed on. ---
				opts := f.mgr.lastStart(t)
				require.NotEmpty(t, opts.HubIssuedToken, "the agent received no hub-issued credential")
				claims, err := f.svc.ValidateAgentToken(opts.HubIssuedToken)
				require.NoError(t, err, "the agent's credential is not one this hub issued")
				assert.Equal(t, f.agent.ID, claims.Subject, "credential agent")
				assert.NotEqual(t, "run-before", opts.RunID)
				assert.Equal(t, wire.body.RunID, opts.RunID, "the broker starts the run the hub sent")
				assert.Equal(t, opts.RunID, claims.RunID, "the credential is the one issued for this run")
				got, err := f.store.GetAgent(ctx, f.agent.ID)
				require.NoError(t, err)
				assert.Equal(t, opts.RunID, got.RunID, "the hub recorded the run the agent started with")
				assert.Equal(t, p.resume, opts.Resume, "resume flag")
				_, inEnv := opts.Env[startCredentialEnvKey]
				assert.False(t, inEnv, "the credential must reach the agent only as the hub-issued credential, not as a plain env value")
				assert.Equal(t, contractPlainValue, opts.Env[contractPlainKey], "agent config env reaches the agent")
				assert.Equal(t, contractEnvValue, opts.Env[contractEnvSecret], "environment secret reaches the agent env")
				assert.Equal(t, f.agent.ID, opts.Env["SCION_AGENT_ID"])
				assertContractSecrets(t, opts.ResolvedSecrets, "StartOptions.ResolvedSecrets")
			})
		}
	}
}

// assertContractSecrets checks both fixture secrets arrived with their
// type, target and value.
func assertContractSecrets(t *testing.T, got []api.ResolvedSecret, where string) {
	t.Helper()
	byName := make(map[string]api.ResolvedSecret, len(got))
	for _, s := range got {
		byName[s.Name] = s
	}
	env, ok := byName["e2e-contract-env"]
	if assert.True(t, ok, "%s: environment secret missing", where) {
		assert.Equal(t, "environment", env.Type, where)
		assert.Equal(t, contractEnvSecret, env.Target, where)
		assert.Equal(t, contractEnvValue, env.Value, where)
	}
	file, ok := byName[contractFileSecret]
	if assert.True(t, ok, "%s: file secret missing", where) {
		assert.Equal(t, "file", file.Type, where)
		assert.Equal(t, contractFileTarget, file.Target, where)
		assert.Equal(t, contractFileValue, file.Value, where)
	}
}

// TestStartContract_E2E_AbsentFieldFailsClosed pins the consumer side of
// the contract when the producer omits a field (an older hub, a hub
// rollback): the broker must fail closed. With no envClassifications on a
// start or restart, or no agentToken on a create, the agent manager gets
// no hub-issued credential and the env SCION_AUTH_TOKEN never reaches the
// agent as a plain value. The rest of the env and the secrets still flow
// (the classification map gates only the credential).
func TestStartContract_E2E_AbsentFieldFailsClosed(t *testing.T) {
	for _, transport := range contractTransports {
		for _, p := range contractPaths {
			strip := "envClassifications"
			if p.create {
				strip = "agentToken"
			}
			t.Run(transport+"/"+p.name+"/no-"+strip, func(t *testing.T) {
				ctx := context.Background()
				f := newContractFixture(t, transport, p, strip)
				// pkg/agent refuses the start (ErrNoHubToken); the
				// stand-in manager records it instead, so the dispatch
				// error is not part of this contract.
				_ = p.dispatch(ctx, f.d, f.agent)

				wire := f.wire.last(t)
				require.True(t, wire.has(strip), "the hub sent %s; the broker saw it stripped", strip)
				opts := f.mgr.lastStart(t)
				assert.Empty(t, opts.HubIssuedToken, "without %s the broker must not accept a credential", strip)
				_, inEnv := opts.Env[startCredentialEnvKey]
				assert.False(t, inEnv, "an unaccepted credential must not reach the agent as a plain env value")
				assert.Equal(t, contractPlainValue, opts.Env[contractPlainKey], "agent config env still reaches the agent")
				assertContractSecrets(t, opts.ResolvedSecrets, "StartOptions.ResolvedSecrets")
			})
		}
	}
}
