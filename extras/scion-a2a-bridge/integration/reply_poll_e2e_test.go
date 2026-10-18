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

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/extras/scion-a2a-bridge/internal/bridge"
	"github.com/GoogleCloudPlatform/scion/extras/scion-a2a-bridge/internal/identity"
	bridgestate "github.com/GoogleCloudPlatform/scion/extras/scion-a2a-bridge/internal/state"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/eventbus"
	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
)

// Agent replies reach A2A callers (ptone/scion#4603). A real hub serves a
// project with one agent; the A2A bridge sits in front of it in plugin mode
// (no durable SDK task store), receiving the hub's broker publications as a
// broker spoke. An A2A client calls the agent as a local hub user with a
// hub user JWT (the bridge's hubJWT scheme). The agent's reply carries the
// "web" channel, which the hub hands only to the in-process bus and the
// spoke that serves that channel, so the bridge never receives it as a push;
// the call must still return it.
//
// The agent is the one stand-in: a dispatcher records what the hub delivers
// to it, and the test answers as the agent through the hub's
// outbound-message route with an agent credential the hub signed.

const (
	rplDevToken   = "scion_dev_reply_poll_e2e_token_0123456789abcdef"
	rplSigningKey = "reply-poll-e2e-user-signing-key-0123456789abcdef"
	rplAgentSlug  = "helper"
	// rplWait bounds every wait on a delivery or an A2A answer. It is sized
	// for the A2A integration job's -race and -count=3 runs.
	rplWait = 60 * time.Second
	// rplBridgeSendTimeout is the bridge's own wait for an agent reply.
	rplBridgeSendTimeout = 5 * time.Second
)

// rplRequest is one request the hub served.
type rplRequest struct {
	method string
	path   string
	query  string
}

// rplRecorder records the requests the hub serves.
type rplRecorder struct {
	mu       sync.Mutex
	requests []rplRequest
}

func (r *rplRecorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, rplRequest{method: req.Method, path: req.URL.Path, query: req.URL.RawQuery})
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

func (r *rplRecorder) snapshot() []rplRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rplRequest(nil), r.requests...)
}

// rplDispatcher stands in for the hub's runtime broker: it records each
// message the hub delivers to an agent. Every other dispatch answers an
// error; embedding the interface keeps the type complete if it grows.
type rplDispatcher struct {
	hub.AgentDispatcher
	delivered chan *messages.StructuredMessage
}

var errRPLDispatchUnavailable = errors.New("not available in this test")

func (d *rplDispatcher) DispatchAgentMessage(_ context.Context, _ *store.Agent, message string, _ bool, structuredMsg *messages.StructuredMessage) error {
	var got messages.StructuredMessage
	if structuredMsg != nil {
		got = *structuredMsg
	} else {
		got.Msg = message
	}
	select {
	case d.delivered <- &got:
		return nil
	default:
		return errors.New("delivery buffer full")
	}
}

func (d *rplDispatcher) DispatchAgentCreate(context.Context, *store.Agent) (*hub.CreateDispatchResult, error) {
	return nil, errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentProvision(context.Context, *store.Agent) error {
	return errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentReprovision(context.Context, *store.Agent) error {
	return errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentStart(context.Context, *store.Agent, string, bool) error {
	return errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentStop(context.Context, *store.Agent) error {
	return errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentRestart(context.Context, *store.Agent) error {
	return errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentResetAuth(context.Context, *store.Agent) error {
	return errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentDelete(context.Context, *store.Agent, bool, bool, bool, time.Time) error {
	return errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentLogs(context.Context, *store.Agent, int) (string, error) {
	return "", errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentExec(context.Context, *store.Agent, []string, int) (string, int, error) {
	return "", 0, errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchCheckAgentPrompt(context.Context, *store.Agent) (bool, error) {
	return false, errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchAgentCreateWithGather(context.Context, *store.Agent) (*hub.CreateDispatchResult, error) {
	return nil, errRPLDispatchUnavailable
}

func (d *rplDispatcher) DispatchFinalizeEnv(context.Context, *store.Agent, map[string]string) (*hub.CreateDispatchResult, error) {
	return nil, errRPLDispatchUnavailable
}

// rplBrokerSpoke is the bridge's broker as a spoke of the hub's fan-out
// event bus, in place of the plugin RPC adapter: Publish hands the message
// to the bridge, Subscribe passes the pattern on. The spoke has no channel,
// so a message with a channel (the "web" default of an agent's reply) goes
// only to the in-process bus.
type rplBrokerSpoke struct {
	broker *bridge.BrokerServer
}

func (s *rplBrokerSpoke) Publish(ctx context.Context, topic string, msg *messages.StructuredMessage) error {
	return s.broker.Publish(ctx, topic, msg)
}

func (s *rplBrokerSpoke) Subscribe(pattern string, _ eventbus.EventHandler) (eventbus.Subscription, error) {
	if err := s.broker.Subscribe(pattern); err != nil {
		return nil, err
	}
	return rplSpokeSubscription{broker: s.broker, pattern: pattern}, nil
}

func (s *rplBrokerSpoke) Close() error { return nil }

type rplSpokeSubscription struct {
	broker  *bridge.BrokerServer
	pattern string
}

func (s rplSpokeSubscription) Unsubscribe() error { return s.broker.Unsubscribe(s.pattern) }

// rplEnv is the whole topology of a test.
type rplEnv struct {
	hub         *hub.Server
	hubStore    store.Store
	hubURL      string
	recorder    *rplRecorder
	bridgeURL   string
	projectID   string
	projectSlug string
	agent       *store.Agent
	agentToken  string
	dispatcher  *rplDispatcher
	client      *http.Client
}

// rplUser is a local hub user and the hub user JWT an A2A client holds.
type rplUser struct {
	user  *store.User
	token string
}

func rplListener(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	return ts, "http://" + ts.Listener.Addr().String()
}

// rplDo sends one request and returns the status and body.
func (e *rplEnv) rplDo(t *testing.T, method, url string, header map[string]string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode %s %s: %v", method, url, err)
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), rplWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, url, err)
	}
	return resp.StatusCode, data
}

func rplBearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func rplDecode(t *testing.T, what string, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v; body %s", what, err, body)
	}
}

// rplNewEnv starts the hub, the project, the agent and the bridge in plugin
// mode with the hubJWT scheme.
func rplNewEnv(t *testing.T) *rplEnv {
	t.Helper()
	// The hub creates its project's directory under the global directory
	// ($HOME/.scion); a per-test HOME keeps every run off the real one.
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	e := &rplEnv{
		recorder:   &rplRecorder{},
		dispatcher: &rplDispatcher{delivered: make(chan *messages.StructuredMessage, 16)},
		client:     &http.Client{Timeout: rplWait},
	}
	tsHub, hubURL := rplListener(t)
	tsBridge, bridgeURL := rplListener(t)
	e.hubURL, e.bridgeURL = hubURL, bridgeURL

	dsn := "file:" + filepath.Join(t.TempDir(), "hub.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	entClient, err := entc.OpenSQLite(dsn, entc.PoolConfig{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	st := entadapter.NewCompositeStore(entClient)
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate hub store: %v", err)
	}
	// As the pkg/hub test servers do: agent rows written directly to the
	// store have no delegation edges, so the backfill marker Migrate sets
	// is removed.
	_ = st.DeleteHubSetting(ctx, "migration_delegation_edge_backfill_v1")
	e.hubStore = st

	cfg := hub.DefaultServerConfig()
	cfg.HubEndpoint = hubURL
	cfg.CORSEnabled = false
	cfg.DisableCloudLogQuery = true
	cfg.DevAuthToken = rplDevToken
	cfg.DevUserConfig = hub.DevUserConfig{Username: "dev", DisplayName: "Development User", Email: "dev@localhost"}
	cfg.UserTokenConfig.SigningKey = []byte(rplSigningKey)
	srv, err := hub.New(cfg, st)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	e.hub = srv
	tsHub.Config.Handler = e.recorder.wrap(srv.Handler())
	tsHub.Start()

	// The project (owned by the dev user), a runtime broker, and a running
	// agent on it.
	status, body := e.rplDo(t, http.MethodPost, hubURL+"/api/v1/projects", rplBearer(rplDevToken),
		map[string]string{"name": "reply-poll-e2e"})
	if status != http.StatusCreated {
		t.Fatalf("create project: status %d, want 201; body %s", status, body)
	}
	var project struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	rplDecode(t, "project", body, &project)
	if project.ID == "" || project.Slug == "" {
		t.Fatalf("create project: no id or slug; body %s", body)
	}
	e.projectID, e.projectSlug = project.ID, project.Slug

	brokerID := uuid.NewString()
	if err := st.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: brokerID, Name: "reply-poll-e2e-broker", Slug: "reply-poll-e2e-broker", Status: store.BrokerStatusOnline,
	}); err != nil {
		t.Fatalf("create runtime broker: %v", err)
	}
	e.agent = &store.Agent{
		ID: uuid.NewString(), Name: rplAgentSlug, Slug: rplAgentSlug, ProjectID: e.projectID,
		RuntimeBrokerID: brokerID, Phase: "running", MessageMode: store.MessageModeProject,
		OwnerID: hub.DevUserID,
	}
	if err := st.CreateAgent(ctx, e.agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	token, cred, err := srv.GetAgentTokenService().SignAgentToken(hub.AgentTokenGrant{
		AgentID: e.agent.ID, ProjectID: e.projectID, Scopes: hub.ScopesForRole(hub.AgentRoleFull),
	}, "")
	if err != nil {
		t.Fatalf("sign agent credential: %v", err)
	}
	if err := st.CreateAgentCredential(ctx, cred); err != nil {
		t.Fatalf("record agent credential: %v", err)
	}
	e.agentToken = token

	// The A2A bridge in plugin mode: an in-memory SDK task store and no
	// durable SDK store, with the hubJWT scheme (the bridge holds the hub's
	// user signing key and mints a short credential per caller).
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stateStore, err := bridgestate.NewSQLite(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("bridge state store: %v", err)
	}
	t.Cleanup(func() { _ = stateStore.Close() })
	adminClient, err := hubclient.New(hubURL, hubclient.WithBearerToken(rplDevToken))
	if err != nil {
		t.Fatalf("bridge hub client: %v", err)
	}
	minter, err := identity.NewTokenMinter([]byte(rplSigningKey))
	if err != nil {
		t.Fatalf("bridge token minter: %v", err)
	}
	bridgeCfg := &bridge.Config{
		Bridge:   bridge.BridgeConfig{ExternalURL: bridgeURL},
		Hub:      bridge.HubConfig{Endpoint: hubURL, User: "a2a-bridge@example.invalid"},
		Auth:     bridge.AuthConfig{Scheme: "hubJWT"},
		Projects: []bridge.ProjectConfig{{Slug: e.projectSlug, ExposedAgents: []string{rplAgentSlug}}},
		Timeouts: bridge.TimeoutConfig{SendMessage: rplBridgeSendTimeout},
	}
	b := bridge.New(stateStore, adminClient, minter, bridgeCfg, nil, logger)
	t.Cleanup(b.Shutdown)
	broker := bridge.NewBrokerServer(b.HandleBrokerMessage, logger, context.Background())
	b.SetBroker(broker)
	executor := bridge.NewScionExecutor(b, logger)
	innerStore := taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: bridge.RouteKeyAuthenticator()})
	sdkHandler := a2asrv.NewHandler(executor,
		a2asrv.WithLogger(logger),
		a2asrv.WithCapabilityChecks(&a2a.AgentCapabilities{Streaming: true}),
		a2asrv.WithAgentInactivityTimeout(rplWait),
		a2asrv.WithTaskStore(bridge.NewScopedTaskStore(innerStore)),
	)
	b.SetSDKRequestHandler(sdkHandler)
	server := bridge.NewServer(b, bridgeCfg, nil, logger, a2asrv.NewJSONRPCHandler(sdkHandler))
	server.SetJWTValidator(bridge.NewJWTValidator([]byte(rplSigningKey)))
	tsBridge.Config.Handler = server.Handler()
	tsBridge.Start()

	// The hub's message broker: the in-process bus plus the bridge as a
	// broker spoke, with the broker proxy as the bridge's host callbacks.
	publisher := hub.NewChannelEventPublisher()
	t.Cleanup(publisher.Close)
	srv.SetEventPublisher(publisher)
	srv.SetDispatcher(e.dispatcher)
	fanout := eventbus.NewFanOutEventBus([]eventbus.NamedEventBus{
		{Name: eventbus.InProcessBusName, Bus: eventbus.NewInProcessEventBus(logger)},
		{Name: "a2a-bridge", Bus: &rplBrokerSpoke{broker: broker}},
	}, logger)
	srv.StartMessageBroker(fanout)
	proxy := srv.GetMessageBrokerProxy()
	if proxy == nil {
		t.Fatal("hub message broker not started")
	}
	broker.SetHostCallbacks(proxy)
	if err := proxy.EnsureProjectSubscriptions(ctx, e.projectID); err != nil {
		t.Fatalf("hub project subscriptions: %v", err)
	}
	return e
}

// rplLocalUser stores an active local hub user, makes them a member of the
// project, and returns them with a hub user JWT.
func (e *rplEnv) rplLocalUser(t *testing.T, email string) rplUser {
	t.Helper()
	ctx := context.Background()
	u := &store.User{
		ID: uuid.NewString(), Email: email, DisplayName: email,
		Role: "member", Status: store.UserStatusActive, Kind: store.UserKindHuman,
	}
	if err := e.hubStore.CreateUser(ctx, u); err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	role, err := e.hubStore.GetRoleDefinitionByName(ctx, store.ProjectRoleMember, store.RoleScopeProject)
	if err != nil {
		t.Fatalf("project member role: %v", err)
	}
	status, body := e.rplDo(t, http.MethodPost, e.hubURL+"/api/v1/projects/"+e.projectID+"/members", rplBearer(rplDevToken),
		map[string]string{"roleDefinitionId": role.ID, "principalType": "user", "principalId": u.ID})
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("add %s to the project: status %d, want 201; body %s", email, status, body)
	}
	token, _, err := e.hub.GetUserTokenService().GenerateAccessToken(u.ID, u.Email, u.DisplayName, u.Role, hub.ClientTypeWeb)
	if err != nil {
		t.Fatalf("user JWT for %s: %v", email, err)
	}
	return rplUser{user: u, token: token}
}

// rplSendResult is the bridge's answer to one JSON-RPC send.
type rplSendResult struct {
	status int
	body   []byte
	err    error
}

// rplSendAsync sends text to the agent through the bridge as an A2A client
// holding token, and delivers the answer on the returned channel.
func (e *rplEnv) rplSendAsync(ctx context.Context, token, text string) <-chan rplSendResult {
	done := make(chan rplSendResult, 1)
	go func() {
		reqBody, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      uuid.NewString(),
			"method":  "SendMessage",
			"params": map[string]any{
				"message": map[string]any{
					"messageId": uuid.NewString(),
					"role":      "ROLE_USER",
					"parts":     []map[string]any{{"text": text}},
				},
			},
		})
		if err != nil {
			done <- rplSendResult{err: err}
			return
		}
		url := e.bridgeURL + "/projects/" + e.projectSlug + "/agents/" + rplAgentSlug + "/jsonrpc"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
		if err != nil {
			done <- rplSendResult{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := e.client.Do(req)
		if err != nil {
			done <- rplSendResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		done <- rplSendResult{status: resp.StatusCode, body: body, err: err}
	}()
	return done
}

// rplOutcome is how a task ended, as the A2A client sees it.
type rplOutcome struct {
	State a2a.TaskState
	Text  string
}

// rplTaskOutcome decodes a JSON-RPC send answer into the task's final state
// and status message text.
func rplTaskOutcome(t *testing.T, label string, res rplSendResult) rplOutcome {
	t.Helper()
	if res.err != nil {
		t.Fatalf("%s: send failed: %v", label, res.err)
	}
	if res.status != http.StatusOK {
		t.Fatalf("%s: bridge answered HTTP %d, want 200; body %s", label, res.status, res.body)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	rplDecode(t, label+" JSON-RPC answer", res.body, &rpc)
	if rpc.Error != nil {
		t.Fatalf("%s: JSON-RPC error %d %q; body %s", label, rpc.Error.Code, rpc.Error.Message, res.body)
	}
	var wrapped struct {
		Task json.RawMessage `json:"task"`
	}
	rplDecode(t, label+" send result", rpc.Result, &wrapped)
	raw := []byte(rpc.Result)
	if len(wrapped.Task) > 0 && string(wrapped.Task) != "null" {
		raw = wrapped.Task
	}
	var task a2a.Task
	rplDecode(t, label+" task", raw, &task)
	var text []string
	if msg := task.Status.Message; msg != nil {
		for _, p := range msg.Parts {
			if p != nil {
				text = append(text, p.Text())
			}
		}
	}
	return rplOutcome{State: task.Status.State, Text: strings.Join(text, "\n")}
}

// rplAgentReply answers as the agent to recipient through the hub's
// outbound-message route.
func (e *rplEnv) rplAgentReply(t *testing.T, label, recipient, text, msgType string) {
	t.Helper()
	status, body := e.rplDo(t, http.MethodPost, e.hubURL+"/api/v1/agents/"+e.agent.ID+"/outbound-message",
		map[string]string{"X-Scion-Agent-Token": e.agentToken},
		hub.OutboundMessageRequest{Recipient: recipient, Msg: text, Type: msgType})
	if status != http.StatusOK {
		t.Fatalf("%s: agent reply: status %d, want 200; body %s", label, status, body)
	}
}

// rplAwaitDelivery waits for the hub to deliver the caller's message to the
// agent and returns it.
func (e *rplEnv) rplAwaitDelivery(t *testing.T, label, text string, done <-chan rplSendResult) *messages.StructuredMessage {
	t.Helper()
	select {
	case got := <-e.dispatcher.delivered:
		if got.Msg != text {
			t.Fatalf("%s: agent received %q, want %q", label, got.Msg, text)
		}
		if !strings.HasPrefix(got.Sender, "user:") {
			t.Fatalf("%s: envelope sender %q, want a user address", label, got.Sender)
		}
		return got
	case res := <-done:
		t.Fatalf("%s: the bridge answered before the hub delivered the message to the agent: HTTP %d, error %v, body %s",
			label, res.status, res.err, res.body)
	case <-time.After(rplWait):
		t.Fatalf("%s: the hub delivered nothing to the agent within %s", label, rplWait)
	}
	return nil
}

// rplAwaitAnswer waits for the bridge's answer to a send.
func rplAwaitAnswer(t *testing.T, label string, done <-chan rplSendResult) rplOutcome {
	t.Helper()
	select {
	case res := <-done:
		return rplTaskOutcome(t, label, res)
	case <-time.After(rplWait):
		t.Fatalf("%s: the bridge did not answer within %s", label, rplWait)
	}
	return rplOutcome{}
}

// rplSentMessageID returns the ID of the one message the user sent, as the
// hub stored it.
func (e *rplEnv) rplSentMessageID(t *testing.T, label string, u rplUser) string {
	t.Helper()
	res, err := e.hubStore.ListMessages(context.Background(), store.MessageFilter{SenderID: u.user.ID}, store.ListOptions{})
	if err != nil {
		t.Fatalf("%s: list the user's messages: %v", label, err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("%s: the hub stored %d messages from the user, want 1", label, len(res.Items))
	}
	return res.Items[0].ID
}

// TestAgentReplyReachesLocalUser_PluginMode: a local user calls the agent
// through the bridge in plugin mode; the agent answers with a default reply
// (filed in the user's direct conversation with the "web" channel), and the
// call returns that reply. The bridge reads it from the user's own
// conversation, anchored on the message ID the send returned.
func TestAgentReplyReachesLocalUser_PluginMode(t *testing.T) {
	e := rplNewEnv(t)
	alice := e.rplLocalUser(t, "alice@example.com")

	ctx, cancel := context.WithTimeout(context.Background(), rplWait)
	defer cancel()
	const label = "local user"
	done := e.rplSendAsync(ctx, alice.token, "hello from a local user")
	got := e.rplAwaitDelivery(t, label, "hello from a local user", done)
	e.rplAgentReply(t, label, got.Sender, "the agent's reply", messages.TypeInstruction)

	outcome := rplAwaitAnswer(t, label, done)
	if outcome.State != a2a.TaskStateCompleted || outcome.Text != "the agent's reply" {
		t.Fatalf("%s: task ended %v with %q, want %v with the agent's reply %q",
			label, outcome.State, outcome.Text, a2a.TaskStateCompleted, "the agent's reply")
	}

	// The anchor is the message the send returned: the bridge read exactly
	// that message of the conversation.
	anchorID := e.rplSentMessageID(t, label, alice)
	var anchorReads int
	for _, r := range e.recorder.snapshot() {
		if r.method == http.MethodGet && strings.HasPrefix(r.path, "/api/v1/conversations/") &&
			strings.Contains(r.path, "/messages/") {
			if !strings.HasSuffix(r.path, "/messages/"+anchorID) {
				t.Fatalf("%s: the bridge read message %s, want only the anchor %s", label, r.path, anchorID)
			}
			anchorReads++
		}
	}
	if anchorReads != 1 {
		t.Fatalf("%s: the bridge read the anchor %d times, want 1", label, anchorReads)
	}
}
