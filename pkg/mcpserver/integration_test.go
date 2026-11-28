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

package mcpserver_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/mcpserver"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/knadh/koanf/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range h.headers {
		clone.Header.Set(k, v)
	}
	return h.base.RoundTrip(clone)
}

type noopMessageDispatcher struct {
	hub.AgentDispatcher
}

func (*noopMessageDispatcher) DispatchAgentMessage(_ context.Context, _ *store.Agent, _ string, _ bool, _ *messages.StructuredMessage) error {
	return nil
}

func newIntegrationHub(t *testing.T) (*hub.Server, store.Store, *hub.OperationalSettings) {
	t.Helper()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "hub.db")
	entClient, err := entc.OpenSQLite(dbPath, entc.PoolConfig{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("entc.OpenSQLite: %v", err)
	}
	if err := entc.AutoMigrate(ctx, entClient); err != nil {
		t.Fatalf("entc.AutoMigrate: %v", err)
	}
	st := entadapter.NewCompositeStore(entClient)
	t.Cleanup(func() { _ = st.Close() })

	srv, err := hub.New(hub.ServerConfig{
		Host: "127.0.0.1",
		Port: 0,
	}, st)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	srv.SetDispatcher(&noopMessageDispatcher{})

	ops := hub.NewOperationalSettings(st, koanf.New("."), koanf.New("."))
	if _, err := ops.Refresh(ctx); err != nil {
		t.Fatalf("ops.Refresh: %v", err)
	}
	srv.SetOperationalSettings(ops)
	return srv, st, ops
}

func setMCPServerExperiment(t *testing.T, ctx context.Context, st store.Store, ops *hub.OperationalSettings, enabled bool) {
	t.Helper()
	doc, err := json.Marshal(map[string]any{
		"overrides": map[string]bool{experiments.MCPServer: enabled},
	})
	if err != nil {
		t.Fatalf("marshal experiment override: %v", err)
	}
	if _, err := st.UpsertHubSetting(ctx, "experiments", doc, "test", -1, "seeded"); err != nil {
		t.Fatalf("UpsertHubSetting(experiments): %v", err)
	}
	if _, err := ops.Refresh(ctx); err != nil {
		t.Fatalf("ops.Refresh: %v", err)
	}
}

type mcpMode string

const (
	modeEmbedded   mcpMode = "embedded"
	modeStandalone mcpMode = "standalone"
)

func startMCPServerForMode(t *testing.T, ctx context.Context, mode mcpMode, hubSrv *hub.Server, wrapTransport func(http.RoundTripper) http.RoundTripper) (endpoint string, cleanup func()) {
	t.Helper()

	var backendOpts mcpserver.BackendOptions
	switch mode {
	case modeEmbedded:
		var rt http.RoundTripper = &mcpserver.InProcessTransport{Handler: hubSrv.Handler()}
		if wrapTransport != nil {
			rt = wrapTransport(rt)
		}
		backendOpts = mcpserver.BackendOptions{
			HubBaseURL: "http://in-process",
			Transport:  rt,
			Version:    "integration-test",
			ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, name string) (bool, error) {
				return hubSrv.ExperimentEnabled(name), nil
			},
			DisableTransportAuth: true,
		}
	case modeStandalone:
		hubTS := httptest.NewServer(hubSrv.Handler())
		t.Cleanup(hubTS.Close)
		rt := hubTS.Client().Transport
		if wrapTransport != nil {
			rt = wrapTransport(rt)
		}
		backendOpts = mcpserver.BackendOptions{
			HubBaseURL:           hubTS.URL,
			Transport:            rt,
			Version:              "integration-test",
			ExperimentCacheTTL:   5 * time.Millisecond,
			DisableTransportAuth: false,
		}
	default:
		t.Fatalf("unknown mcpMode: %q", mode)
	}

	srvOpts := mcpserver.Options{
		Version: "integration-test",
		Backend: mcpserver.NewBackend(backendOpts),
	}
	if mode == modeStandalone {
		srvOpts.HubHealthProbe = mcpserver.NewHubHealthProbe(backendOpts)
	}
	mcpSrv, err := mcpserver.New(srvOpts)
	if err != nil {
		t.Fatalf("mcpserver.New(%s): %v", mode, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srvCtx, srvCancel := context.WithCancel(ctx)
	srvDone := make(chan error, 1)
	go func() {
		srvDone <- mcpSrv.Serve(srvCtx, ln)
	}()
	return "http://" + ln.Addr().String() + "/mcp", func() {
		srvCancel()
		if err := <-srvDone; err != nil {
			t.Errorf("mcpSrv.Serve(%s) shutdown error: %v", mode, err)
		}
	}
}

func TestIntegration_SendAndReadMessagesWithRealHub(t *testing.T) {
	for _, mode := range []mcpMode{modeEmbedded, modeStandalone} {
		t.Run(string(mode), func(t *testing.T) {
			runIntegrationSendAndReadMessages(t, mode)
		})
	}
}

func runIntegrationSendAndReadMessages(t *testing.T, mode mcpMode) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hubSrv, st, ops := newIntegrationHub(t)

	// 1. Seed a user in the real Hub store and grant hub-member so they can create a project.
	user := &store.User{
		ID:          uuid.NewString(),
		Email:       "alice@example.com",
		DisplayName: "Alice",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	hubMemberRole, err := st.GetRoleDefinitionByName(ctx, "hub-member", store.RoleScopeSystem)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName(hub-member): %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		PrincipalType:    "user",
		PrincipalID:      user.ID,
		RoleDefinitionID: hubMemberRole.ID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        user.ID,
	}); err != nil {
		t.Fatalf("CreateRoleBinding(hub-member): %v", err)
	}

	inProcClient := func(opt hubclient.Option) hubclient.Client {
		hc := &http.Client{
			Transport: &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		}
		cl, err := hubclient.New("http://in-process",
			hubclient.WithHTTPClient(hc),
			opt,
			hubclient.WithTransportAuth(nil, 0),
		)
		if err != nil {
			t.Fatalf("hubclient.New: %v", err)
		}
		return cl
	}

	// Mint a user JWT to create the project and UATs through the real Hub REST API.
	userJWT, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		ClientType:  hub.ClientTypeCLI,
	})
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	userHubClient := inProcClient(hubclient.WithBearerToken(userJWT))

	proj, err := userHubClient.Projects().Create(ctx, &hubclient.CreateProjectRequest{
		Name: "MCP Test Project",
		Slug: "mcp-test",
	})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}

	agent := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "echo-t",
		Name:            "echo-t",
		ProjectID:       proj.ID,
		OwnerID:         user.ID,
		CreatedBy:       user.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "running",
		MessageMode:     "project",
		RunID:           "run-1",
	}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Mint an agent JWT for echo-t so the test can write replies through the Hub API.
	agentToken, agentCred, err := hubSrv.SignAgentToken(hub.AgentTokenGrant{
		AgentID:   agent.ID,
		ProjectID: proj.ID,
		Ancestry:  []string{user.ID},
		Scopes:    []hub.AgentTokenScope{hub.ScopeAgentStatusUpdate, hub.ScopeProjectRead},
	}, "run-1")
	if err != nil {
		t.Fatalf("SignAgentToken: %v", err)
	}
	if err := st.CreateAgentCredential(ctx, agentCred); err != nil {
		t.Fatalf("CreateAgentCredential: %v", err)
	}
	fullUAT, err := userHubClient.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "mcp-p1-full",
		ProjectID: proj.ID,
		Scopes: []string{
			"project:read",
			"agent:read",
			"agent:message",
			"inbox:read",
			"inbox:write",
		},
	})
	if err != nil {
		t.Fatalf("create full UAT: %v", err)
	}

	// Also mint a UAT lacking agent:message to verify Hub authz reason propagation.
	noMsgUAT, err := userHubClient.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "mcp-p1-no-msg",
		ProjectID: proj.ID,
		Scopes: []string{
			"project:read",
			"agent:read",
			"inbox:read",
		},
	})
	if err != nil {
		t.Fatalf("create no-msg UAT: %v", err)
	}

	// 2. Start the MCP listener in the requested mode (embedded or standalone).
	// hub.mcp_server starts off by default to test the 404 gate.
	endpoint, stopMCP := startMCPServerForMode(t, ctx, mode, hubSrv, nil)
	defer stopMCP()

	// 3. With hub.mcp_server off, /mcp answers 404.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+fullUAT.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp (exp off): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("experiment off: got status %d, want 404", resp.StatusCode)
	}

	// Flip hub.mcp_server on via real Hub operational settings.
	setMCPServerExperiment(t, ctx, st, ops, true)
	if mode == modeStandalone {
		// Allow the short test experiment cache (5ms) to expire.
		time.Sleep(15 * time.Millisecond)
	}

	// 4. Connect a go-sdk StreamableClientTransport with the static UAT header.
	connectClient := func(token string) *mcp.ClientSession {
		cl := mcp.NewClient(&mcp.Implementation{Name: "mcp-integration-client", Version: "1.0"}, nil)
		hc := &http.Client{
			Transport: &headerRoundTripper{
				base:    &http.Transport{DisableKeepAlives: true},
				headers: map[string]string{"Authorization": "Bearer " + token},
			},
		}
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: hc,
		}, nil)
		if err != nil {
			t.Fatalf("mcp Connect: %v", err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}

	session := connectClient(fullUAT.Token)

	// 5. tools/list returns exactly the two P1 tools with schemas and annotations.
	listRes, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listRes.Tools) != 2 {
		t.Fatalf("ListTools returned %d tools, want 2", len(listRes.Tools))
	}
	toolByName := make(map[string]*mcp.Tool, len(listRes.Tools))
	for _, tl := range listRes.Tools {
		toolByName[tl.Name] = tl
	}
	sendTool := toolByName["send_message"]
	readTool := toolByName["read_messages"]
	if sendTool == nil || readTool == nil {
		t.Fatalf("expected send_message and read_messages, got %v", toolByName)
	}
	if sendTool.InputSchema == nil || readTool.InputSchema == nil {
		t.Fatal("expected non-nil JSON input schemas on both tools")
	}
	if sendTool.Annotations == nil || sendTool.Annotations.OpenWorldHint == nil || !*sendTool.Annotations.OpenWorldHint {
		t.Fatalf("send_message annotations = %+v, want OpenWorldHint=true", sendTool.Annotations)
	}
	if readTool.Annotations == nil || !readTool.Annotations.ReadOnlyHint {
		t.Fatalf("read_messages annotations = %+v, want ReadOnlyHint=true", readTool.Annotations)
	}
	if !strings.Contains(strings.ToLower(readTool.Description), "untrusted") {
		t.Errorf("read_messages description should warn that message text is untrusted: %q", readTool.Description)
	}

	// 6. Client calls send_message to echo-t in proj.ID.
	sendRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": proj.ID,
			"agent":   agent.Slug,
			"text":    "ping-p1-nonce",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(send_message): %v", err)
	}
	if sendRes.IsError {
		t.Fatalf("send_message returned tool error: %s", sendRes.Content[0].(*mcp.TextContent).Text)
	}
	var sendOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(sendRes.Content[0].(*mcp.TextContent).Text), &sendOut); err != nil {
		t.Fatalf("unmarshal send_message output: %v", err)
	}
	if sendOut.MessageID == "" || sendOut.ConversationID == "" || sendOut.Cursor == "" {
		t.Fatalf("send_message missing fields: %+v", sendOut)
	}

	// 7. Agent reply is written through the Hub API using the agent's JWT.
	agentHubClient := inProcClient(hubclient.WithAgentToken(agentToken))
	replyOut, err := agentHubClient.Agents().SendOutboundMessage(ctx, agent.ID, &hubclient.OutboundMessageRequest{
		Recipient:      "user:" + user.Email,
		ConversationID: sendOut.ConversationID,
		Msg:            "PONG-p1-nonce",
		Type:           messages.TypeReply,
	})
	if err != nil {
		t.Fatalf("agent SendOutboundMessage: %v", err)
	}
	if replyOut == nil || replyOut.MessageID == "" {
		t.Fatalf("unexpected empty agent reply result: %+v", replyOut)
	}

	// 8. Client calls read_messages from the send cursor -> returns exactly the agent's reply.
	readRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"cursor":          sendOut.Cursor,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(read_messages): %v", err)
	}
	if readRes.IsError {
		t.Fatalf("read_messages returned tool error: %+v", readRes.Content)
	}
	var readOut mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(readRes.Content[0].(*mcp.TextContent).Text), &readOut); err != nil {
		t.Fatalf("unmarshal read_messages output: %v", err)
	}
	if len(readOut.Messages) != 1 {
		t.Fatalf("read_messages returned %d messages, want 1: %+v", len(readOut.Messages), readOut.Messages)
	}
	gotMsg := readOut.Messages[0]
	if gotMsg.Text != "PONG-p1-nonce" {
		t.Fatalf("reply text = %q, want %q", gotMsg.Text, "PONG-p1-nonce")
	}
	if gotMsg.Sender.Kind != "agent" {
		t.Fatalf("reply sender.kind = %q, want %q", gotMsg.Sender.Kind, "agent")
	}
	if gotMsg.ConversationID != sendOut.ConversationID {
		t.Fatalf("reply conversation_id = %q, want %q", gotMsg.ConversationID, sendOut.ConversationID)
	}
	if readOut.NextCursor == "" || readOut.NextCursor == sendOut.Cursor {
		t.Fatalf("expected next_cursor to advance from %q, got %q", sendOut.Cursor, readOut.NextCursor)
	}

	// 9. A UAT lacking agent:message gets a tool error carrying the Hub's reason.
	noMsgSession := connectClient(noMsgUAT.Token)
	deniedRes, err := noMsgSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": proj.ID,
			"agent":   agent.Slug,
			"text":    "should fail",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(send_message denied) protocol error: %v", err)
	}
	if !deniedRes.IsError {
		t.Fatalf("expected tool error for UAT lacking agent:message, got success: %+v", deniedRes.Content)
	}
	errText := deniedRes.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(errText, "403") && !strings.Contains(strings.ToLower(errText), "scope") && !strings.Contains(strings.ToLower(errText), "agent:message") {
		t.Fatalf("expected Hub authz reason in tool error, got: %q", errText)
	}

	// 10. Real Hub-minted agent JWT sent to /mcp is refused with 401 unsupported_credential.
	agentReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	agentReq.Header.Set("Authorization", "Bearer "+agentToken)
	agentReq.Header.Set("Content-Type", "application/json")
	agentResp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		t.Fatalf("POST /mcp (real agent JWT): %v", err)
	}
	var agentErrBody map[string]string
	_ = json.NewDecoder(agentResp.Body).Decode(&agentErrBody)
	_ = agentResp.Body.Close()
	if agentResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("real agent JWT: got status %d, want 401", agentResp.StatusCode)
	}
	if agentErrBody["error"] != "unsupported_credential" {
		t.Fatalf("real agent JWT: error = %q, want unsupported_credential", agentErrBody["error"])
	}

	// 11. Five-selector UAT calling send_message with project slug gets clear guidance.
	slugRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": proj.Slug,
			"agent":   agent.Slug,
			"text":    "via slug",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(send_message slug) protocol error: %v", err)
	}
	if !slugRes.IsError {
		t.Fatalf("expected tool error for slug without project:list, got success: %+v", slugRes.Content)
	}
	slugErrText := slugRes.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(slugErrText, "project ID") || !strings.Contains(slugErrText, "project:list") {
		t.Fatalf("expected slug guidance error, got: %q", slugErrText)
	}

	// 12. R3: read_messages without cursor returns last 'limit' messages; from_start=true returns oldest.
	for i := 1; i <= 5; i++ {
		if _, err := agentHubClient.Agents().SendOutboundMessage(ctx, agent.ID, &hubclient.OutboundMessageRequest{
			Recipient:      "user:" + user.Email,
			ConversationID: sendOut.ConversationID,
			Msg:            fmt.Sprintf("extra-%d", i),
			Type:           messages.TypeReply,
		}); err != nil {
			t.Fatalf("SendOutboundMessage extra-%d: %v", i, err)
		}
	}
	// Conversation now has 7 messages: ping-p1-nonce, PONG-p1-nonce, extra-1..extra-5.
	tailRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"limit":           3,
		},
	})
	if err != nil || tailRes.IsError {
		t.Fatalf("read_messages (tail) failed: err=%v res=%+v", err, tailRes)
	}
	var tailOut mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(tailRes.Content[0].(*mcp.TextContent).Text), &tailOut); err != nil {
		t.Fatalf("unmarshal tail read_messages: %v", err)
	}
	if len(tailOut.Messages) != 3 {
		t.Fatalf("tail read_messages count = %d, want 3", len(tailOut.Messages))
	}
	wantTail := []string{"extra-3", "extra-4", "extra-5"}
	for i, want := range wantTail {
		if tailOut.Messages[i].Text != want {
			t.Fatalf("tail[%d] = %q, want %q", i, tailOut.Messages[i].Text, want)
		}
	}
	if tailOut.NextCursor == "" {
		t.Fatal("expected non-empty next_cursor from tail read_messages")
	}

	startRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"from_start":      true,
			"limit":           3,
		},
	})
	if err != nil || startRes.IsError {
		t.Fatalf("read_messages (from_start) failed: err=%v res=%+v", err, startRes)
	}
	var startOut mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(startRes.Content[0].(*mcp.TextContent).Text), &startOut); err != nil {
		t.Fatalf("unmarshal from_start read_messages: %v", err)
	}
	if len(startOut.Messages) != 3 {
		t.Fatalf("from_start read_messages count = %d, want 3", len(startOut.Messages))
	}
	wantStart := []string{"ping-p1-nonce", "PONG-p1-nonce", "extra-1"}
	for i, want := range wantStart {
		if startOut.Messages[i].Text != want {
			t.Fatalf("from_start[%d] = %q, want %q", i, startOut.Messages[i].Text, want)
		}
	}
	if !startOut.HasMore {
		t.Fatal("expected has_more=true for from_start with limit=3 on 7-message conversation")
	}
}

// raceInjectingTransport intercepts POST .../message responses and immediately
// commits a concurrent agent reply before returning the response to SendToAgent.
// On the old two-step Watch(AfterMessage, Immediate) implementation, this reply
// landed before the cursor fetch and was silently skipped.
type raceInjectingTransport struct {
	base   http.RoundTripper
	onSent func(convID string) error
	fired  atomic.Bool
}

func (r *raceInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err != nil || req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/message") {
		return resp, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if readErr != nil || resp.StatusCode >= 400 {
		return resp, nil
	}
	var decoded struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil && decoded.ConversationID != "" && r.fired.CompareAndSwap(false, true) {
		if cbErr := r.onSent(decoded.ConversationID); cbErr != nil {
			return nil, cbErr
		}
	}
	return resp, nil
}

// TestIntegration_SendMessageCursorDoesNotSkipConcurrentReply is the R1
// regression test, run table-driven over {embedded, standalone}: an agent reply
// committed concurrently with (before SendToAgent returns) must still be
// returned by read_messages(send_cursor).
func TestIntegration_SendMessageCursorDoesNotSkipConcurrentReply(t *testing.T) {
	for _, mode := range []mcpMode{modeEmbedded, modeStandalone} {
		t.Run(string(mode), func(t *testing.T) {
			runIntegrationConcurrentReply(t, mode)
		})
	}
}

func runIntegrationConcurrentReply(t *testing.T, mode mcpMode) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hubSrv, st, ops := newIntegrationHub(t)
	setMCPServerExperiment(t, ctx, st, ops, true)

	user := &store.User{
		ID:          uuid.NewString(),
		Email:       "race@example.com",
		DisplayName: "Race Tester",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	hubMemberRole, err := st.GetRoleDefinitionByName(ctx, "hub-member", store.RoleScopeSystem)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName: %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		PrincipalType:    "user",
		PrincipalID:      user.ID,
		RoleDefinitionID: hubMemberRole.ID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        user.ID,
	}); err != nil {
		t.Fatalf("CreateRoleBinding: %v", err)
	}

	inProcClient := func(opt hubclient.Option) hubclient.Client {
		hc := &http.Client{Transport: &mcpserver.InProcessTransport{Handler: hubSrv.Handler()}}
		cl, err := hubclient.New("http://in-process", hubclient.WithHTTPClient(hc), opt, hubclient.WithTransportAuth(nil, 0))
		if err != nil {
			t.Fatalf("hubclient.New: %v", err)
		}
		return cl
	}

	userJWT, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		ClientType:  hub.ClientTypeCLI,
	})
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	userHubClient := inProcClient(hubclient.WithBearerToken(userJWT))
	proj, err := userHubClient.Projects().Create(ctx, &hubclient.CreateProjectRequest{
		Name: "MCP Race Project",
		Slug: "mcp-race",
	})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}

	agent := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "fast-echo",
		Name:            "fast-echo",
		ProjectID:       proj.ID,
		OwnerID:         user.ID,
		CreatedBy:       user.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "running",
		MessageMode:     "project",
		RunID:           "run-race-1",
	}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	agentToken, agentCred, err := hubSrv.SignAgentToken(hub.AgentTokenGrant{
		AgentID:   agent.ID,
		ProjectID: proj.ID,
		Ancestry:  []string{user.ID},
		Scopes:    []hub.AgentTokenScope{hub.ScopeAgentStatusUpdate, hub.ScopeProjectRead},
	}, "run-race-1")
	if err != nil {
		t.Fatalf("SignAgentToken: %v", err)
	}
	if err := st.CreateAgentCredential(ctx, agentCred); err != nil {
		t.Fatalf("CreateAgentCredential: %v", err)
	}

	uat, err := userHubClient.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "mcp-race-uat",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "agent:read", "agent:message", "inbox:read", "inbox:write"},
	})
	if err != nil {
		t.Fatalf("Tokens.Create: %v", err)
	}

	agentHubClient := inProcClient(hubclient.WithAgentToken(agentToken))
	var raceRT *raceInjectingTransport
	endpoint, stopMCP := startMCPServerForMode(t, ctx, mode, hubSrv, func(base http.RoundTripper) http.RoundTripper {
		raceRT = &raceInjectingTransport{
			base: base,
			onSent: func(convID string) error {
				_, err := agentHubClient.Agents().SendOutboundMessage(ctx, agent.ID, &hubclient.OutboundMessageRequest{
					Recipient:      "user:" + user.Email,
					ConversationID: convID,
					Msg:            "CONCURRENT-REPLY",
					Type:           messages.TypeReply,
				})
				return err
			},
		}
		return raceRT
	})
	defer stopMCP()

	cl := mcp.NewClient(&mcp.Implementation{Name: "race-client", Version: "1.0"}, nil)
	hc := &http.Client{
		Transport: &headerRoundTripper{
			base:    &http.Transport{DisableKeepAlives: true},
			headers: map[string]string{"Authorization": "Bearer " + uat.Token},
		},
	}
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: hc,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	sendRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": proj.ID,
			"agent":   agent.Slug,
			"text":    "trigger-fast-reply",
		},
	})
	if err != nil || sendRes.IsError {
		t.Fatalf("send_message failed: err=%v res=%+v", err, sendRes)
	}
	var sendOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(sendRes.Content[0].(*mcp.TextContent).Text), &sendOut); err != nil {
		t.Fatalf("unmarshal sendOut: %v", err)
	}
	if !raceRT.fired.Load() {
		t.Fatal("expected concurrent reply hook to fire")
	}

	readRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"cursor":          sendOut.Cursor,
		},
	})
	if err != nil || readRes.IsError {
		t.Fatalf("read_messages failed: err=%v res=%+v", err, readRes)
	}
	var readOut mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(readRes.Content[0].(*mcp.TextContent).Text), &readOut); err != nil {
		t.Fatalf("unmarshal readOut: %v", err)
	}
	if len(readOut.Messages) != 1 || readOut.Messages[0].Text != "CONCURRENT-REPLY" {
		t.Fatalf("expected CONCURRENT-REPLY after send cursor, got: %+v", readOut.Messages)
	}
}

// TestIntegration_CrossReplicaCursorResume implements §9 B5: two standalone MCP
// server replicas pointing at one Hub. A cursor obtained on replica 1 resumes
// cleanly on replica 2 with no loss and no duplicate delivery.
func TestIntegration_CrossReplicaCursorResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hubSrv, st, ops := newIntegrationHub(t)
	setMCPServerExperiment(t, ctx, st, ops, true)

	user := &store.User{
		ID:          uuid.NewString(),
		Email:       "replica@example.com",
		DisplayName: "Replica User",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	hubMemberRole, err := st.GetRoleDefinitionByName(ctx, "hub-member", store.RoleScopeSystem)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName: %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		PrincipalType:    "user",
		PrincipalID:      user.ID,
		RoleDefinitionID: hubMemberRole.ID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        user.ID,
	}); err != nil {
		t.Fatalf("CreateRoleBinding: %v", err)
	}

	hubTS := httptest.NewServer(hubSrv.Handler())
	defer hubTS.Close()

	newHubClient := func(opt hubclient.Option) hubclient.Client {
		cl, err := hubclient.New(hubTS.URL, hubclient.WithHTTPClient(hubTS.Client()), opt, hubclient.WithTransportAuth(nil, 0))
		if err != nil {
			t.Fatalf("hubclient.New: %v", err)
		}
		return cl
	}

	userJWT, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		ClientType:  hub.ClientTypeCLI,
	})
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	userHubClient := newHubClient(hubclient.WithBearerToken(userJWT))
	proj, err := userHubClient.Projects().Create(ctx, &hubclient.CreateProjectRequest{
		Name: "Cross-Replica Project",
		Slug: "cross-replica",
	})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}

	agent := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "echo-replica",
		Name:            "echo-replica",
		ProjectID:       proj.ID,
		OwnerID:         user.ID,
		CreatedBy:       user.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "running",
		MessageMode:     "project",
		RunID:           "run-replica-1",
	}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	agentToken, agentCred, err := hubSrv.SignAgentToken(hub.AgentTokenGrant{
		AgentID:   agent.ID,
		ProjectID: proj.ID,
		Ancestry:  []string{user.ID},
		Scopes:    []hub.AgentTokenScope{hub.ScopeAgentStatusUpdate, hub.ScopeProjectRead},
	}, "run-replica-1")
	if err != nil {
		t.Fatalf("SignAgentToken: %v", err)
	}
	if err := st.CreateAgentCredential(ctx, agentCred); err != nil {
		t.Fatalf("CreateAgentCredential: %v", err)
	}
	uat, err := userHubClient.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "mcp-replica-uat",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "agent:read", "agent:message", "inbox:read", "inbox:write"},
	})
	if err != nil {
		t.Fatalf("Tokens.Create: %v", err)
	}

	// Start two independent standalone MCP server replicas against the same Hub.
	startReplica := func(name string) (string, func()) {
		backendOpts := mcpserver.BackendOptions{
			HubBaseURL:           hubTS.URL,
			Transport:            hubTS.Client().Transport,
			Version:              name,
			DisableTransportAuth: false,
		}
		srv, err := mcpserver.New(mcpserver.Options{
			Version:        name,
			Backend:        mcpserver.NewBackend(backendOpts),
			HubHealthProbe: mcpserver.NewHubHealthProbe(backendOpts),
		})
		if err != nil {
			t.Fatalf("mcpserver.New(%s): %v", name, err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen(%s): %v", name, err)
		}
		rCtx, rCancel := context.WithCancel(ctx)
		rDone := make(chan error, 1)
		go func() { rDone <- srv.Serve(rCtx, ln) }()
		return "http://" + ln.Addr().String() + "/mcp", func() {
			rCancel()
			if err := <-rDone; err != nil {
				t.Errorf("%s shutdown error: %v", name, err)
			}
		}
	}

	ep1, stop1 := startReplica("replica-1")
	defer stop1()
	ep2, stop2 := startReplica("replica-2")
	defer stop2()

	connectTo := func(ep string) *mcp.ClientSession {
		cl := mcp.NewClient(&mcp.Implementation{Name: "replica-client", Version: "1.0"}, nil)
		hc := &http.Client{
			Transport: &headerRoundTripper{
				base:    &http.Transport{DisableKeepAlives: true},
				headers: map[string]string{"Authorization": "Bearer " + uat.Token},
			},
		}
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   ep,
			HTTPClient: hc,
		}, nil)
		if err != nil {
			t.Fatalf("Connect(%s): %v", ep, err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}

	sess1 := connectTo(ep1)
	sess2 := connectTo(ep2)

	// Send initial message through replica 1.
	sendRes, err := sess1.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": proj.ID,
			"agent":   agent.Slug,
			"text":    "hello-from-replica-1",
		},
	})
	if err != nil || sendRes.IsError {
		t.Fatalf("send_message via replica 1 failed: err=%v res=%+v", err, sendRes)
	}
	var sendOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(sendRes.Content[0].(*mcp.TextContent).Text), &sendOut); err != nil {
		t.Fatalf("unmarshal sendOut: %v", err)
	}

	// Agent writes reply 1; read it through replica 1 to obtain cursor2.
	agentHubClient := newHubClient(hubclient.WithAgentToken(agentToken))
	if _, err := agentHubClient.Agents().SendOutboundMessage(ctx, agent.ID, &hubclient.OutboundMessageRequest{
		Recipient:      "user:" + user.Email,
		ConversationID: sendOut.ConversationID,
		Msg:            "reply-1",
		Type:           messages.TypeReply,
	}); err != nil {
		t.Fatalf("SendOutboundMessage reply-1: %v", err)
	}

	read1, err := sess1.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"cursor":          sendOut.Cursor,
		},
	})
	if err != nil || read1.IsError {
		t.Fatalf("read_messages via replica 1 failed: err=%v res=%+v", err, read1)
	}
	var out1 mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(read1.Content[0].(*mcp.TextContent).Text), &out1); err != nil {
		t.Fatalf("unmarshal out1: %v", err)
	}
	if len(out1.Messages) != 1 || out1.Messages[0].Text != "reply-1" {
		t.Fatalf("replica 1 read = %+v, want [reply-1]", out1.Messages)
	}

	// Agent writes reply 2; resume on replica 2 using replica 1's next_cursor.
	if _, err := agentHubClient.Agents().SendOutboundMessage(ctx, agent.ID, &hubclient.OutboundMessageRequest{
		Recipient:      "user:" + user.Email,
		ConversationID: sendOut.ConversationID,
		Msg:            "reply-2",
		Type:           messages.TypeReply,
	}); err != nil {
		t.Fatalf("SendOutboundMessage reply-2: %v", err)
	}

	read2, err := sess2.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"cursor":          out1.NextCursor,
		},
	})
	if err != nil || read2.IsError {
		t.Fatalf("read_messages via replica 2 failed: err=%v res=%+v", err, read2)
	}
	var out2 mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(read2.Content[0].(*mcp.TextContent).Text), &out2); err != nil {
		t.Fatalf("unmarshal out2: %v", err)
	}
	if len(out2.Messages) != 1 || out2.Messages[0].Text != "reply-2" {
		t.Fatalf("replica 2 resume = %+v, want [reply-2]", out2.Messages)
	}
	if out2.NextCursor == "" || out2.NextCursor == out1.NextCursor {
		t.Fatalf("expected replica 2 next_cursor to advance from %q, got %q", out1.NextCursor, out2.NextCursor)
	}
}

// TestIntegration_VisitorPassesFrontDoorInBothModes verifies R2: a linked
// project visitor authenticating with X-Scion-Federation-Token gets parity
// between embedded and standalone modes (404 when hub.mcp_server is off,
// 200 OK on initialize when hub.mcp_server is on — never 403 at the
// standalone experiment probe).
func TestIntegration_VisitorPassesFrontDoorInBothModes(t *testing.T) {
	for _, mode := range []mcpMode{modeEmbedded, modeStandalone} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			const (
				issuerURL = "https://idp-mcp-visitor.example.com"
				audience  = "https://hub-mcp-visitor.example.com"
				kid       = "mcp-visitor-kid-1"
			)

			privKey, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatalf("rsa.GenerateKey: %v", err)
			}
			jwk := jose.JSONWebKey{Key: &privKey.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig"}
			jwksBytes, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
			if err != nil {
				t.Fatalf("marshal jwks: %v", err)
			}
			jwksTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(jwksBytes)
			}))
			defer jwksTS.Close()

			dbPath := filepath.Join(t.TempDir(), "hub-visitor.db")
			entClient, err := entc.OpenSQLite(dbPath, entc.PoolConfig{MaxOpenConns: 1})
			if err != nil {
				t.Fatalf("entc.OpenSQLite: %v", err)
			}
			if err := entc.AutoMigrate(ctx, entClient); err != nil {
				t.Fatalf("entc.AutoMigrate: %v", err)
			}
			st := entadapter.NewCompositeStore(entClient)
			t.Cleanup(func() { _ = st.Close() })

			hubSrv, err := hub.New(hub.ServerConfig{
				Host: "127.0.0.1",
				Port: 0,
				Federation: config.FederationConfig{
					Enabled: true,
					TrustedIssuers: []config.TrustedIssuerConfig{
						{
							IssuerURL:          issuerURL,
							IssuerType:         "user",
							JWKSURL:            jwksTS.URL,
							ExpectedAudience:   audience,
							AllowVisitorGrants: true,
						},
					},
				},
			}, st)
			if err != nil {
				t.Fatalf("hub.New: %v", err)
			}
			t.Cleanup(func() { _ = hubSrv.Shutdown(context.Background()) })

			ops := hub.NewOperationalSettings(st, koanf.New("."), koanf.New("."))
			if _, err := ops.Refresh(ctx); err != nil {
				t.Fatalf("ops.Refresh: %v", err)
			}
			hubSrv.SetOperationalSettings(ops)

			setFlags := func(mcpOn bool) {
				doc, err := json.Marshal(map[string]any{
					"overrides": map[string]bool{
						experiments.FederationProjectVisitors: true,
						experiments.MCPServer:                 mcpOn,
					},
				})
				if err != nil {
					t.Fatalf("marshal flags: %v", err)
				}
				if _, err := st.UpsertHubSetting(ctx, "experiments", doc, "test", -1, "seeded"); err != nil {
					t.Fatalf("UpsertHubSetting: %v", err)
				}
				if _, err := ops.Refresh(ctx); err != nil {
					t.Fatalf("ops.Refresh: %v", err)
				}
			}

			// Seed linked federated visitor account.
			iss := issuerURL
			email := "visitor-mcp@corp.example.com"
			fedUser := &store.User{
				DisplayName:    "MCP Visitor",
				Kind:           store.UserKindFederated,
				Status:         store.UserStatusActive,
				HomeIssuer:     &iss,
				FederatedEmail: &email,
			}
			if err := st.CreateFederatedUser(ctx, fedUser); err != nil {
				t.Fatalf("CreateFederatedUser: %v", err)
			}
			if _, err := st.LinkFederatedIdentity(ctx, fedUser.ID, issuerURL, "sub-mcp-visitor", email); err != nil {
				t.Fatalf("LinkFederatedIdentity: %v", err)
			}

			// Mint signed OIDC JWT for the visitor.
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privKey}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
			if err != nil {
				t.Fatalf("jose.NewSigner: %v", err)
			}
			now := time.Now()
			visitorJWT, err := jwt.Signed(signer).Claims(map[string]any{
				"iss":            issuerURL,
				"sub":            "sub-mcp-visitor",
				"aud":            audience,
				"iat":            now.Unix(),
				"exp":            now.Add(time.Hour).Unix(),
				"email":          email,
				"email_verified": true,
				"name":           "MCP Visitor",
			}).Serialize()
			if err != nil {
				t.Fatalf("sign visitor JWT: %v", err)
			}

			setFlags(false)
			endpoint, stopMCP := startMCPServerForMode(t, ctx, mode, hubSrv, nil)
			defer stopMCP()

			postInit := func() (int, string) {
				t.Helper()
				initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"visitor-client","version":"0.1"}}}`
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(initBody))
				if err != nil {
					t.Fatalf("NewRequest: %v", err)
				}
				req.Header.Set(mcpserver.HeaderFederationToken, visitorJWT)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("POST /mcp: %v", err)
				}
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				return resp.StatusCode, string(b)
			}

			// 1. hub.mcp_server off -> 404 in both embedded and standalone modes.
			if code, body := postInit(); code != http.StatusNotFound {
				t.Fatalf("mode=%s hub.mcp_server=false: got %d (%s), want 404", mode, code, body)
			}

			// 2. hub.mcp_server on -> 200 OK in both modes (never 403).
			setFlags(true)
			if mode == modeStandalone {
				time.Sleep(15 * time.Millisecond)
			}
			if code, body := postInit(); code != http.StatusOK {
				t.Fatalf("mode=%s hub.mcp_server=true: got %d (%s), want 200", mode, code, body)
			}
		})
	}
}
