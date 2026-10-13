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

package bridge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"

	"github.com/GoogleCloudPlatform/scion/extras/scion-a2a-bridge/internal/identity"
	"github.com/GoogleCloudPlatform/scion/extras/scion-a2a-bridge/internal/state"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
)

// Fixed identifiers of the federated user tests. IDs that end up in a DM key
// must be canonical UUIDs.
const (
	fedUserIssuer    = "https://hub-a.example.com"
	fedUserSubject   = "alice-on-hub-a"
	fedUserEmail     = "alice@corp.example.com"
	fedUserAccountID = "11111111-2222-4333-8444-555555555555"
	fedUserProjectID = "99999999-8888-4777-8666-555555555555"
	fedUserAgentID   = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	fedUserReplyText = "reply from agent1 to the federated user"
)

// fedUserToken is a user access credential from a peer hub, as the bridge
// receives it (unsigned here: the bridge never verifies it, the hub does).
func fedUserToken(t *testing.T) string {
	t.Helper()
	return buildTestJWT(t, map[string]interface{}{
		"iss":              fedUserIssuer,
		"sub":              fedUserSubject,
		"aud":              "https://hub-b.example.com",
		"email":            fedUserEmail,
		"email_verified":   true,
		"scion_token_kind": "user",
	})
}

// fedAgentToken is an agent identity credential from a peer hub.
func fedAgentToken(t *testing.T) string {
	t.Helper()
	return buildTestJWT(t, map[string]interface{}{
		"iss":              fedUserIssuer,
		"sub":              "fed-agent-1",
		"project_id":       "remote-project",
		"agent_name":       "remote-agent",
		"ancestry":         []string{"root-user"},
		"root_user":        "root-user",
		"scion_token_kind": "agent",
	})
}

// --- classification ---

func TestDecodeFederationToken_Classification(t *testing.T) {
	tests := []struct {
		name      string
		claims    map[string]interface{}
		wantErr   bool
		wantUser  bool
		wantAgent string
	}{
		{
			name: "user kind",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": fedUserSubject, "email": fedUserEmail,
				"scion_token_kind": "user",
			},
			wantUser: true,
		},
		{
			name: "agent kind",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": "agent-1", "project_id": "p", "ancestry": []string{"u"},
				"scion_token_kind": "agent",
			},
			wantAgent: "agent-1",
		},
		{
			name: "agent kind with an email stays an agent",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": "agent-2", "email": "x@example.com",
				"scion_token_kind": "agent",
			},
			wantAgent: "agent-2",
		},
		{
			name: "no kind with agent claims is an agent",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": "agent-3", "project_id": "p", "root_user": "u",
			},
			wantAgent: "agent-3",
		},
		{
			name: "no kind with agent claims and an email is an agent",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": "agent-4", "agent_name": "a", "email": "x@example.com",
			},
			wantAgent: "agent-4",
		},
		{
			name: "no kind with only an email is a user",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": fedUserSubject, "email": fedUserEmail,
			},
			wantUser: true,
		},
		{
			name: "no kind and neither email nor agent claims is an agent",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": "agent-5",
			},
			wantAgent: "agent-5",
		},
		{
			name: "user kind with an agent claim is refused",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": fedUserSubject, "email": fedUserEmail, "project_id": "p",
				"scion_token_kind": "user",
			},
			wantErr: true,
		},
		{
			name: "user kind with ancestry is refused",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": fedUserSubject, "email": fedUserEmail, "ancestry": []string{"u"},
				"scion_token_kind": "user",
			},
			wantErr: true,
		},
		{
			name: "user kind without an email is refused",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": fedUserSubject,
				"scion_token_kind": "user",
			},
			wantErr: true,
		},
		{
			name: "unknown kind is refused",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": fedUserSubject, "email": fedUserEmail,
				"scion_token_kind": "service",
			},
			wantErr: true,
		},
		{
			name: "kind that is not a string is refused",
			claims: map[string]interface{}{
				"iss": fedUserIssuer, "sub": fedUserSubject, "email": fedUserEmail,
				"scion_token_kind": []string{"user", "agent"},
			},
			wantErr: true,
		},
		{
			name: "user credential whose issuer has no host is refused",
			claims: map[string]interface{}{
				"iss": "not-a-url", "sub": fedUserSubject, "email": fedUserEmail,
				"scion_token_kind": "user",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller, err := decodeFederationToken(buildTestJWT(t, tt.claims))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("decodeFederationToken = %+v, want an error", caller)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeFederationToken: %v", err)
			}
			if caller.TokenType != "federation" {
				t.Errorf("TokenType = %q, want federation", caller.TokenType)
			}
			if got := caller.IsFederatedUser(); got != tt.wantUser {
				t.Errorf("IsFederatedUser() = %v, want %v", got, tt.wantUser)
			}
			if tt.wantUser {
				if caller.IsAgent() {
					t.Errorf("IsAgent() = true for a user credential")
				}
				if caller.Subject != fedUserSubject || caller.Email != fedUserEmail || caller.IssuerURL != fedUserIssuer {
					t.Errorf("user caller = %+v, want subject %q, email %q, issuer %q", caller, fedUserSubject, fedUserEmail, fedUserIssuer)
				}
				if caller.UserID != "" {
					t.Errorf("UserID = %q before the hub named the account, want empty", caller.UserID)
				}
				return
			}
			if caller.AgentID != tt.wantAgent || !caller.IsAgent() {
				t.Errorf("AgentID = %q (IsAgent %v), want %q", caller.AgentID, caller.IsAgent(), tt.wantAgent)
			}
			if caller.Subject != "" || caller.Email != "" {
				t.Errorf("agent caller carries user fields: %+v", caller)
			}
		})
	}
}

// TestDecodeFederationToken_AgentUnchanged pins the agent caller built from
// an agent credential: exactly the fields set before credential kinds.
func TestDecodeFederationToken_AgentUnchanged(t *testing.T) {
	token := fedAgentToken(t)
	caller, err := decodeFederationToken(token)
	if err != nil {
		t.Fatalf("decodeFederationToken: %v", err)
	}
	want := CallerIdentity{
		TokenType: "federation",
		RawToken:  token,
		AgentID:   "fed-agent-1",
		IssuerURL: fedUserIssuer,
		ProjectID: "remote-project",
		Ancestry:  []string{"root-user"},
	}
	if caller.TokenType != want.TokenType || caller.RawToken != want.RawToken || caller.AgentID != want.AgentID ||
		caller.IssuerURL != want.IssuerURL || caller.ProjectID != want.ProjectID ||
		len(caller.Ancestry) != 1 || caller.Ancestry[0] != "root-user" ||
		caller.UserID != "" || caller.Email != "" || caller.Subject != "" || caller.Role != "" {
		t.Fatalf("agent caller = %+v, want %+v", caller, want)
	}
	if caller.SenderLabel() != "agent:fed-agent-1" {
		t.Errorf("SenderLabel() = %q, want agent:fed-agent-1", caller.SenderLabel())
	}
	if caller.CallerKey() != "agent:hub-a.example.com:fed-agent-1" {
		t.Errorf("CallerKey() = %q, want agent:hub-a.example.com:fed-agent-1", caller.CallerKey())
	}
}

// --- caller identity ---

func TestCallerIdentity_FederatedUser(t *testing.T) {
	caller := &CallerIdentity{
		TokenType: "federation",
		UserID:    fedUserAccountID,
		Email:     fedUserEmail,
		IssuerURL: fedUserIssuer,
		Subject:   fedUserSubject,
	}
	if !caller.IsFederatedUser() || caller.IsAgent() {
		t.Fatalf("IsFederatedUser = %v, IsAgent = %v; want true, false", caller.IsFederatedUser(), caller.IsAgent())
	}
	if got, want := caller.SenderLabel(), "user:"+fedUserAccountID; got != want {
		t.Errorf("SenderLabel() = %q, want %q (the account ID, never the email)", got, want)
	}
	if got, want := caller.CallerKey(), "fed:hub-a.example.com:"+fedUserSubject; got != want {
		t.Errorf("CallerKey() = %q, want %q", got, want)
	}

	noHost := *caller
	noHost.IssuerURL = "issuer-without-host"
	if got, want := noHost.CallerKey(), "fed:issuer-without-host:"+fedUserSubject; got != want {
		t.Errorf("CallerKey() without issuer host = %q, want %q", got, want)
	}

	var nilCaller *CallerIdentity
	if nilCaller.IsFederatedUser() {
		t.Error("nil caller is a federated user")
	}
	if (&CallerIdentity{UserID: "u", Email: "e", TokenType: "uat"}).IsFederatedUser() {
		t.Error("a UAT caller is a federated user")
	}
	if (&CallerIdentity{AgentID: "a", Subject: "s", TokenType: "federation"}).IsFederatedUser() {
		t.Error("an agent caller is a federated user")
	}
}

// --- hub test double ---

// fedUserHub stands in for the hub the bridge talks to, for both the
// bridge admin client and the federated user's own credential.
type fedUserHub struct {
	*httptest.Server
	userToken string

	mu           sync.Mutex
	meStatus     int    // 0: 200 with the visitor account
	meRole       string // role in the /auth/me answer
	sendStatus   int    // 0: 200
	dmReadStatus int    // 0: 200
	grants       []map[string]interface{}
	adminAgents  []map[string]interface{}
	projAgents   []map[string]interface{}
	meCalls      int
	meFedTokens  []string
	sent         []fedUserHubSend
	listQueries  []url.Values
	listTokens   []string
	dmReadKeys   []string
	dmReadTokens []string
	createCalls  int
}

type fedUserHubSend struct {
	AgentID  string
	Body     map[string]interface{}
	FedToken string
}

func writeHubError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"code": "forbidden", "message": federatedUserRefusalText},
	})
}

func newFedUserHub(t *testing.T, userToken string) *fedUserHub {
	t.Helper()
	h := &fedUserHub{
		userToken: userToken,
		meRole:    "viewer",
		grants: []map[string]interface{}{
			{"projectId": fedUserProjectID, "projectSlug": "proj1", "projectName": "Project 1", "status": "active"},
		},
		adminAgents: []map[string]interface{}{
			{"id": fedUserAgentID, "name": "agent1", "slug": "agent1", "projectId": fedUserProjectID, "status": "running"},
		},
		projAgents: []map[string]interface{}{
			{"name": "agent1", "slug": "agent1", "status": "idle"},
			{"name": "agent3", "slug": "agent3", "status": "idle"},
		},
	}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fedToken := r.Header.Get(FederationTokenHeader)
		h.mu.Lock()
		defer h.mu.Unlock()
		writeJSON := func(v interface{}) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
			h.meCalls++
			h.meFedTokens = append(h.meFedTokens, fedToken)
			if fedToken != h.userToken {
				writeHubError(w, http.StatusUnauthorized)
				return
			}
			if h.meStatus != 0 {
				writeHubError(w, h.meStatus)
				return
			}
			writeJSON(map[string]interface{}{
				"id": fedUserAccountID, "email": "visitor+synthetic@hub-b.invalid", "display_name": "Alice", "role": h.meRole,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/me/visitor-grants":
			if fedToken != h.userToken {
				writeHubError(w, http.StatusForbidden)
				return
			}
			writeJSON(map[string]interface{}{"grants": h.grants})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+fedUserProjectID+"/agents":
			h.listQueries = append(h.listQueries, r.URL.Query())
			h.listTokens = append(h.listTokens, fedToken)
			if fedToken != h.userToken {
				writeHubError(w, http.StatusForbidden)
				return
			}
			writeJSON(map[string]interface{}{"agents": h.projAgents})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents":
			// Agent create (auto-provisioning): recorded and refused.
			h.createCalls++
			writeHubError(w, http.StatusForbidden)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			// Bridge admin agent list (resolveContext, lookupAgent).
			writeJSON(map[string]interface{}{"agents": h.adminAgents, "totalCount": len(h.adminAgents)})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/agents/") && strings.HasSuffix(r.URL.Path, "/message"):
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			agentID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/agents/"), "/message")
			h.sent = append(h.sent, fedUserHubSend{AgentID: agentID, Body: body, FedToken: fedToken})
			if h.sendStatus != 0 {
				writeHubError(w, h.sendStatus)
				return
			}
			writeJSON(map[string]interface{}{"message_id": "msg-1", "status": "delivered"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/chat/conversations/") && strings.HasSuffix(r.URL.Path, "/messages"):
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/chat/conversations/"), "/messages")
			h.dmReadKeys = append(h.dmReadKeys, key)
			h.dmReadTokens = append(h.dmReadTokens, fedToken)
			if fedToken != h.userToken {
				writeHubError(w, http.StatusForbidden)
				return
			}
			if h.dmReadStatus != 0 {
				writeHubError(w, h.dmReadStatus)
				return
			}
			writeJSON(map[string]interface{}{"messages": []interface{}{}})
		default:
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *fedUserHub) set(fn func(h *fedUserHub)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

// fedUserHubCalls is a copy of what the hub double has seen.
type fedUserHubCalls struct {
	meCalls      int
	meFedTokens  []string
	sent         []fedUserHubSend
	listQueries  []url.Values
	listTokens   []string
	dmReadKeys   []string
	dmReadTokens []string
	createCalls  int
}

func (h *fedUserHub) snapshot() fedUserHubCalls {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fedUserHubCalls{
		meCalls:      h.meCalls,
		meFedTokens:  append([]string(nil), h.meFedTokens...),
		sent:         append([]fedUserHubSend(nil), h.sent...),
		listQueries:  append([]url.Values(nil), h.listQueries...),
		listTokens:   append([]string(nil), h.listTokens...),
		dmReadKeys:   append([]string(nil), h.dmReadKeys...),
		dmReadTokens: append([]string(nil), h.dmReadTokens...),
		createCalls:  h.createCalls,
	}
}

func testMinter(t *testing.T) *identity.TokenMinter {
	t.Helper()
	m, err := identity.NewTokenMinter([]byte("federated-user-test-signing-key-0123456789"))
	if err != nil {
		t.Fatalf("NewTokenMinter: %v", err)
	}
	return m
}

// newFedUserTestServer builds a bridge with the federation scheme, a real
// SDK handler and executor, a broker server (for subscriptions), and the hub
// double as the hub. It mirrors newIntegrationTestServer, plus a signing key
// (the federation scheme mints the bridge credential it sends alongside the
// caller's).
func newFedUserTestServer(t *testing.T, hub *fedUserHub, configure ...func(*Config)) (*Bridge, *BrokerServer, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	store, err := state.NewSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := &Config{
		Bridge: BridgeConfig{ExternalURL: "https://bridge.example.com"},
		Hub:    HubConfig{Endpoint: hub.URL, User: "admin@test"},
		Auth:   AuthConfig{Scheme: "federation"},
		Projects: []ProjectConfig{
			{Slug: "proj1", ExposedAgents: []string{"agent1", "agent2"}},
		},
		Timeouts: TimeoutConfig{SendMessage: 3 * time.Second},
	}
	for _, fn := range configure {
		fn(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	adminClient, err := hubclient.New(hub.URL, hubclient.WithBearerToken("admin-token"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(store, adminClient, testMinter(t), cfg, nil, log)
	t.Cleanup(b.Shutdown)
	broker := NewBrokerServer(nil, log, context.Background())
	b.SetBroker(broker)

	executor := NewScionExecutor(b, log)
	innerStore := taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: RouteKeyAuthenticator()})
	sdkRequestHandler := a2asrv.NewHandler(
		executor,
		a2asrv.WithLogger(log),
		a2asrv.WithCapabilityChecks(&a2a.AgentCapabilities{Streaming: true}),
		a2asrv.WithAgentInactivityTimeout(10*time.Second),
		a2asrv.WithTaskStore(NewScopedTaskStore(innerStore)),
	)
	b.SetSDKRequestHandler(sdkRequestHandler)
	srv := NewServer(b, cfg, nil, log, a2asrv.NewJSONRPCHandler(sdkRequestHandler))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return b, broker, ts
}

const fedUserSendPayload = `{
	"jsonrpc": "2.0",
	"id": "fed-user-1",
	"method": "SendMessage",
	"params": {
		"message": {
			"messageId": "msg-fed-user-001",
			"role": "ROLE_USER",
			"parts": [{"text": "Hello from hub A"}]
		}
	}
}`

// sendAsync sends the JSON-RPC SendMessage in the background and returns a
// channel with the status and body.
func sendAsync(t *testing.T, ts *httptest.Server, agentSlug, token string) <-chan [2]interface{} {
	t.Helper()
	done := make(chan [2]interface{}, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/projects/proj1/agents/"+agentSlug+"/jsonrpc", strings.NewReader(fedUserSendPayload))
		if err != nil {
			done <- [2]interface{}{0, err.Error()}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- [2]interface{}{0, err.Error()}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		done <- [2]interface{}{resp.StatusCode, string(body)}
	}()
	return done
}

func waitForSend(t *testing.T, hub *fedUserHub) fedUserHubSend {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := hub.snapshot(); len(s.sent) > 0 {
			return s.sent[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("hub received no message send")
	return fedUserHubSend{}
}

func awaitResult(t *testing.T, done <-chan [2]interface{}) (int, string) {
	t.Helper()
	select {
	case r := <-done:
		status, _ := r[0].(int)
		body, _ := r[1].(string)
		return status, body
	case <-time.After(10 * time.Second):
		t.Fatal("JSON-RPC send did not return")
		return 0, ""
	}
}

// deliverReply delivers agent1's reply to the federated user the way the hub
// does: on the user's own topic, with no a2aTaskId metadata.
func deliverReply(t *testing.T, b *Bridge) {
	t.Helper()
	reply := &messages.StructuredMessage{
		Version:   1,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Sender:    "agent:agent1",
		Recipient: "user:" + fedUserAccountID,
		Msg:       fedUserReplyText,
		Type:      messages.TypeInstruction,
	}
	topic := projectkeys.UserTopic(fedUserProjectID, fedUserAccountID)
	if err := b.HandleBrokerMessage(context.Background(), topic, reply); err != nil {
		t.Fatalf("HandleBrokerMessage: %v", err)
	}
}

func brokerSubscribed(broker *BrokerServer, pattern string) bool {
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	return broker.subscriptions[pattern]
}

// --- auth middleware: the user caller path ---

func TestAuthMiddleware_FederatedUserIdentity(t *testing.T) {
	token := fedUserToken(t)
	hub := newFedUserHub(t, token)
	b, _, _ := newFedUserTestServer(t, hub)

	var seen *CallerIdentity
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = callerIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	srv := NewServer(b, b.config, nil, b.log, echo)
	mw := srv.authMiddleware(echo)

	serve := func(bearer string) *httptest.ResponseRecorder {
		seen = nil
		req := httptest.NewRequest(http.MethodPost, "/projects/proj1/agents/agent1/jsonrpc", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		return w
	}

	t.Run("user credential is identified by the hub", func(t *testing.T) {
		w := serve(token)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
		}
		if seen == nil || !seen.IsFederatedUser() {
			t.Fatalf("caller = %+v, want a federated user", seen)
		}
		if seen.UserID != fedUserAccountID {
			t.Errorf("UserID = %q, want the account the hub named (%q)", seen.UserID, fedUserAccountID)
		}
		if seen.Email != fedUserEmail {
			t.Errorf("Email = %q, want the credential's email %q", seen.Email, fedUserEmail)
		}
		if seen.TokenType != "federation" || seen.RawToken != token {
			t.Errorf("TokenType/RawToken = %q/%q, want federation and the caller's credential", seen.TokenType, seen.RawToken)
		}
		if got, want := seen.SenderLabel(), "user:"+fedUserAccountID; got != want {
			t.Errorf("SenderLabel() = %q, want %q", got, want)
		}
		if got, want := seen.CallerKey(), "fed:hub-a.example.com:"+fedUserSubject; got != want {
			t.Errorf("CallerKey() = %q, want %q", got, want)
		}
		snap := hub.snapshot()
		if snap.meCalls != 1 || snap.meFedTokens[0] != token {
			t.Errorf("auth/me calls = %d with credentials %v, want 1 with the caller's credential", snap.meCalls, snap.meFedTokens)
		}
	})

	t.Run("hub does not accept the credential", func(t *testing.T) {
		hub.set(func(h *fedUserHub) { h.meStatus = http.StatusUnauthorized })
		defer hub.set(func(h *fedUserHub) { h.meStatus = 0 })
		if w := serve(token); w.Code != http.StatusUnauthorized || seen != nil {
			t.Fatalf("status = %d (caller %+v), want 401 and no caller", w.Code, seen)
		}
	})

	t.Run("hub refuses the account", func(t *testing.T) {
		hub.set(func(h *fedUserHub) { h.meStatus = http.StatusForbidden })
		defer hub.set(func(h *fedUserHub) { h.meStatus = 0 })
		w := serve(token)
		if w.Code != http.StatusForbidden || seen != nil {
			t.Fatalf("status = %d (caller %+v), want 403 and no caller", w.Code, seen)
		}
		if !strings.Contains(w.Body.String(), federatedUserRefusalText) {
			t.Errorf("body = %q, want the uniform refusal", w.Body.String())
		}
	})

	t.Run("hub names an agent for a user credential", func(t *testing.T) {
		hub.set(func(h *fedUserHub) { h.meRole = "agent" })
		defer hub.set(func(h *fedUserHub) { h.meRole = "viewer" })
		if w := serve(token); w.Code != http.StatusForbidden || seen != nil {
			t.Fatalf("status = %d (caller %+v), want 403 and no caller", w.Code, seen)
		}
	})

	t.Run("agent credential is not sent to auth/me", func(t *testing.T) {
		before := hub.snapshot().meCalls
		agentToken := fedAgentToken(t)
		if w := serve(agentToken); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if seen == nil || !seen.IsAgent() || seen.IsFederatedUser() || seen.AgentID != "fed-agent-1" || seen.UserID != "" {
			t.Fatalf("caller = %+v, want the unchanged agent caller", seen)
		}
		if after := hub.snapshot().meCalls; after != before {
			t.Errorf("auth/me calls %d -> %d for an agent credential, want none", before, after)
		}
	})
}

// --- executor: send, reply correlation, revocation ---

func TestExecutor_FederatedUserReplyThroughUserTopic(t *testing.T) {
	token := fedUserToken(t)
	hub := newFedUserHub(t, token)
	b, broker, ts := newFedUserTestServer(t, hub)

	done := sendAsync(t, ts, "agent1", token)
	sent := waitForSend(t, hub)

	if sent.AgentID != fedUserAgentID {
		t.Errorf("send went to agent %q, want %q", sent.AgentID, fedUserAgentID)
	}
	if sent.FedToken != token {
		t.Errorf("send carried federation credential %q, want the caller's", sent.FedToken)
	}
	sm, _ := sent.Body["structured_message"].(map[string]interface{})
	if sm == nil {
		t.Fatalf("send body has no structured_message: %v", sent.Body)
	}
	if _, has := sm["metadata"]; has {
		t.Errorf("structured_message carries metadata %v; the hub accepts only a text message from a federated user", sm["metadata"])
	}
	for _, field := range []string{"interrupt", "notify", "wake", "mentions"} {
		if _, has := sent.Body[field]; has {
			t.Errorf("send body sets %q", field)
		}
	}
	if got, want := sm["sender"], "user:"+fedUserAccountID; got != want {
		t.Errorf("sender = %v, want %q", got, want)
	}
	if !brokerSubscribed(broker, projectkeys.AllUserTopic(fedUserProjectID)) {
		t.Errorf("no subscription to the project's user topics (%s)", projectkeys.AllUserTopic(fedUserProjectID))
	}

	deliverReply(t, b)

	status, body := awaitResult(t, done)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, body)
	}
	if !strings.Contains(body, fedUserReplyText) {
		t.Fatalf("response does not carry the agent's reply; body %s", body)
	}

	snap := hub.snapshot()
	wantKey, _ := messages.DMConversationKey("agent", fedUserAgentID, "user", fedUserAccountID)
	if len(snap.dmReadKeys) != 1 || snap.dmReadKeys[0] != wantKey || snap.dmReadTokens[0] != token {
		t.Errorf("access re-checks = %v with %v, want one read of %q with the caller's credential", snap.dmReadKeys, snap.dmReadTokens, wantKey)
	}
}

func TestExecutor_FederatedUserRevokedEndsTask(t *testing.T) {
	token := fedUserToken(t)
	hub := newFedUserHub(t, token)
	b, _, ts := newFedUserTestServer(t, hub)

	done := sendAsync(t, ts, "agent1", token)
	waitForSend(t, hub)

	// The grant is revoked while the agent works: the hub now refuses the
	// caller's read of its conversation with the agent.
	hub.set(func(h *fedUserHub) { h.dmReadStatus = http.StatusForbidden })
	deliverReply(t, b)

	status, body := awaitResult(t, done)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, body)
	}
	if strings.Contains(body, fedUserReplyText) {
		t.Fatalf("reply forwarded after the hub refused access; body %s", body)
	}
	if !strings.Contains(body, federatedUserRefusalText) {
		t.Fatalf("response does not carry the uniform refusal; body %s", body)
	}
	if !strings.Contains(body, "FAILED") && !strings.Contains(body, "failed") {
		t.Errorf("task did not end failed; body %s", body)
	}
}

func TestExecutor_FederatedUserRecheckUnavailableStopsReply(t *testing.T) {
	token := fedUserToken(t)
	hub := newFedUserHub(t, token)
	b, _, ts := newFedUserTestServer(t, hub)

	done := sendAsync(t, ts, "agent1", token)
	waitForSend(t, hub)
	hub.set(func(h *fedUserHub) { h.dmReadStatus = http.StatusInternalServerError })
	deliverReply(t, b)

	_, body := awaitResult(t, done)
	if strings.Contains(body, fedUserReplyText) {
		t.Fatalf("reply forwarded although the hub could not confirm access; body %s", body)
	}
}

// TestExecutor_FederatedUserRefusalsAreUniform: an agent the hub does not let
// the caller message, and an agent that does not exist, give the same
// answer, and the bridge never creates an agent for a federated user.
func TestExecutor_FederatedUserRefusalsAreUniform(t *testing.T) {
	token := fedUserToken(t)

	refusedHub := newFedUserHub(t, token)
	refusedHub.set(func(h *fedUserHub) { h.sendStatus = http.StatusForbidden })
	_, _, refusedTS := newFedUserTestServer(t, refusedHub)
	refusedStatus, refusedBody := awaitResult(t, sendAsync(t, refusedTS, "agent1", token))

	unknownHub := newFedUserHub(t, token)
	unknownHub.set(func(h *fedUserHub) { h.adminAgents = nil })
	// agent2 is exposed but does not exist; auto-provisioning is on.
	_, _, unknownTS := newFedUserTestServer(t, unknownHub, func(cfg *Config) {
		cfg.Projects[0].AutoProvision = true
		cfg.Projects[0].DefaultTemplate = "default"
	})
	unknownStatus, unknownBody := awaitResult(t, sendAsync(t, unknownTS, "agent2", token))

	for name, r := range map[string]struct {
		status int
		body   string
	}{"refused agent": {refusedStatus, refusedBody}, "unknown agent": {unknownStatus, unknownBody}} {
		if r.status != http.StatusOK {
			t.Errorf("%s: status = %d, want 200; body %s", name, r.status, r.body)
		}
		if !strings.Contains(r.body, federatedUserRefusalText) {
			t.Errorf("%s: response does not carry the uniform refusal; body %s", name, r.body)
		}
		if strings.Contains(r.body, `"error"`) {
			t.Errorf("%s: answered with a JSON-RPC error instead of a failed task; body %s", name, r.body)
		}
	}
	if calls := unknownHub.snapshot(); len(calls.sent) != 0 || calls.createCalls != 0 {
		t.Errorf("unknown agent: %d sends and %d agent creates reached the hub, want 0 and 0", len(calls.sent), calls.createCalls)
	}
}

// TestExecutor_FederatedUserHubAccessOff: while the hub does not admit
// federated users it refuses their credential, and the bridge sends nothing.
func TestExecutor_FederatedUserHubAccessOff(t *testing.T) {
	token := fedUserToken(t)
	hub := newFedUserHub(t, token)
	hub.set(func(h *fedUserHub) { h.meStatus = http.StatusUnauthorized })
	_, _, ts := newFedUserTestServer(t, hub)

	status, body := awaitResult(t, sendAsync(t, ts, "agent1", token))
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", status, body)
	}
	if n := len(hub.snapshot().sent); n != 0 {
		t.Errorf("%d sends reached the hub, want 0", n)
	}
}

// TestExecutor_FederatedAgentUnchanged is the regression for agent
// credentials: metadata, sender and admin topic subscription as before, and
// no auth/me call or access re-check.
func TestExecutor_FederatedAgentUnchanged(t *testing.T) {
	agentToken := fedAgentToken(t)
	hub := newFedUserHub(t, "unused-user-token")
	_, broker, ts := newFedUserTestServer(t, hub)

	done := sendAsync(t, ts, "agent1", agentToken)
	sent := waitForSend(t, hub)
	awaitResult(t, done) // no reply: the send times out

	if sent.FedToken != agentToken {
		t.Errorf("send carried federation credential %q, want the agent's", sent.FedToken)
	}
	sm, _ := sent.Body["structured_message"].(map[string]interface{})
	md, _ := sm["metadata"].(map[string]interface{})
	if md == nil || md["a2aTaskId"] == nil || md["a2aTaskId"] == "" {
		t.Errorf("agent send metadata = %v, want a2aTaskId", sm["metadata"])
	}
	if sm["sender"] != "agent:fed-agent-1" {
		t.Errorf("sender = %v, want agent:fed-agent-1", sm["sender"])
	}
	if !brokerSubscribed(broker, projectkeys.UserTopic(fedUserProjectID, "admin@test")) {
		t.Error("agent caller did not subscribe to the bridge admin's user topic")
	}
	if brokerSubscribed(broker, projectkeys.AllUserTopic(fedUserProjectID)) {
		t.Error("agent caller subscribed to all user topics")
	}
	snap := hub.snapshot()
	if snap.meCalls != 0 || len(snap.dmReadKeys) != 0 {
		t.Errorf("auth/me calls = %d, access re-checks = %d for an agent credential, want 0 and 0", snap.meCalls, len(snap.dmReadKeys))
	}
}

// TestValidateTopicUser_FederatedUser: the caller ID the task stores for a
// federated user is its account ID, so a reply on its own user topic is
// accepted and one on another user's topic is not.
func TestValidateTopicUser_FederatedUser(t *testing.T) {
	b, _ := newLifecycleTestBridge(t)
	caller := &CallerIdentity{TokenType: "federation", UserID: fedUserAccountID, Email: fedUserEmail, IssuerURL: fedUserIssuer, Subject: fedUserSubject}
	own := projectkeys.UserTopic(fedUserProjectID, fedUserAccountID)
	other := projectkeys.UserTopic(fedUserProjectID, "22222222-3333-4444-8555-666666666666")
	if err := b.validateTopicUser(own, caller.UserID, "task-1"); err != nil {
		t.Errorf("own user topic refused: %v", err)
	}
	if err := b.validateTopicUser(other, caller.UserID, "task-1"); err == nil {
		t.Error("another user's topic accepted")
	}
}

// --- agent cards ---

func TestFederatedUserAgentCards(t *testing.T) {
	token := fedUserToken(t)
	hub := newFedUserHub(t, token)
	_, _, ts := newFedUserTestServer(t, hub)

	get := func(path, bearer string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	t.Run("lists only messageable, exposed agents", func(t *testing.T) {
		status, body := get("/projects/proj1/agents", token)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", status, body)
		}
		var resp struct {
			Agents []map[string]interface{} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode: %v; body %s", err, body)
		}
		if len(resp.Agents) != 1 {
			t.Fatalf("cards = %d, want 1 (agent1; agent3 is not exposed, agent2 not messageable); body %s", len(resp.Agents), body)
		}
		if resp.Agents[0]["name"] != "agent1" || resp.Agents[0]["url"] != "https://bridge.example.com/projects/proj1/agents/agent1" {
			t.Errorf("card = %v, want agent1's", resp.Agents[0])
		}
		snap := hub.snapshot()
		if len(snap.listQueries) != 1 || snap.listQueries[0].Get("messageable") != "true" {
			t.Errorf("hub list queries = %v, want one with messageable=true", snap.listQueries)
		}
		if snap.listTokens[0] != token {
			t.Errorf("hub list used federation credential %q, want the caller's", snap.listTokens[0])
		}
	})

	t.Run("no active grant on the project is refused", func(t *testing.T) {
		hub.set(func(h *fedUserHub) { h.grants[0]["status"] = "expired" })
		defer hub.set(func(h *fedUserHub) { h.grants[0]["status"] = "active" })
		status, body := get("/projects/proj1/agents", token)
		if status != http.StatusForbidden || !strings.Contains(body, federatedUserRefusalText) {
			t.Fatalf("status = %d body %q, want 403 with the uniform refusal", status, body)
		}
	})

	t.Run("unknown project is refused the same way", func(t *testing.T) {
		status, body := get("/projects/nope/agents", token)
		if status != http.StatusForbidden || !strings.Contains(body, federatedUserRefusalText) {
			t.Fatalf("status = %d body %q, want 403 with the uniform refusal", status, body)
		}
	})

	t.Run("agent caller gets no per-caller list", func(t *testing.T) {
		if status, _ := get("/projects/proj1/agents", fedAgentToken(t)); status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
	})
}

// --- resubscribe re-check ---

func TestDurableHandler_FederatedUserStreamRefusal(t *testing.T) {
	token := fedUserToken(t)
	hub := newFedUserHub(t, token)
	b, _, _ := newFedUserTestServer(t, hub)
	caller := &CallerIdentity{TokenType: "federation", RawToken: token, UserID: fedUserAccountID, Email: fedUserEmail, IssuerURL: fedUserIssuer, Subject: fedUserSubject}
	ctx := WithRouteInfo(context.Background(), RouteInfo{ProjectSlug: "proj1", AgentSlug: "agent1"})

	h := &DurableRequestHandler{}
	if h.federatedUserStreamRefusal(ctx, caller) == nil {
		t.Error("without a bridge the reply is streamed; want it held back")
	}
	h.SetBridge(b)
	if msg := h.federatedUserStreamRefusal(context.Background(), caller); msg == nil {
		t.Error("without route info the reply is streamed; want it held back")
	}
	if msg := h.federatedUserStreamRefusal(ctx, caller); msg != nil {
		t.Errorf("access granted, but the reply is held back: %v", msg)
	}
	hub.set(func(h *fedUserHub) { h.dmReadStatus = http.StatusForbidden })
	msg := h.federatedUserStreamRefusal(ctx, caller)
	if msg == nil {
		t.Fatal("access revoked, but the reply is streamed")
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text() != federatedUserRefusalText {
		t.Errorf("refusal message = %v, want the uniform refusal", msg.Parts)
	}
}
