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
	"log/slog"
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

func newIntegrationHub(t *testing.T) (*hub.Server, store.Store) {
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
	srv.SetDispatcher(&noopMessageDispatcher{})
	return srv, st
}

func TestIntegration_SendAndReadMessagesWithRealHub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hubSrv, st := newIntegrationHub(t)

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

	// 2. Start the embedded MCP listener on 127.0.0.1:0 with an in-process RoundTripper.
	var expEnabled atomic.Bool
	expEnabled.Store(false) // starts off to test 404 gate

	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		Version:    "integration-test",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, name string) (bool, error) {
			if name == experiments.MCPServer {
				return expEnabled.Load(), nil
			}
			return hubSrv.ExperimentEnabled(name), nil
		},
		DisableTransportAuth: true,
	})

	mcpSrv, err := mcpserver.New(mcpserver.Options{
		Version: "integration-test",
		Backend: backend,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
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
	defer func() {
		srvCancel()
		if err := <-srvDone; err != nil {
			t.Errorf("mcpSrv.Serve shutdown error: %v", err)
		}
	}()

	endpoint := "http://" + ln.Addr().String() + "/mcp"

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

	// Flip hub.mcp_server on.
	expEnabled.Store(true)

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
// regression test: an agent reply committed concurrently with (before
// SendToAgent returns) must still be returned by read_messages(send_cursor).
func TestIntegration_SendMessageCursorDoesNotSkipConcurrentReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hubSrv, st := newIntegrationHub(t)

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
	raceRT := &raceInjectingTransport{
		base: &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
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

	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  raceRT,
		Version:    "race-test",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	mcpSrv, err := mcpserver.New(mcpserver.Options{Version: "race-test", Backend: backend})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srvCtx, srvCancel := context.WithCancel(ctx)
	srvDone := make(chan error, 1)
	go func() { srvDone <- mcpSrv.Serve(srvCtx, ln) }()
	defer func() {
		srvCancel()
		if err := <-srvDone; err != nil {
			t.Errorf("Serve shutdown error: %v", err)
		}
	}()

	cl := mcp.NewClient(&mcp.Implementation{Name: "race-client", Version: "1.0"}, nil)
	hc := &http.Client{
		Transport: &headerRoundTripper{
			base:    &http.Transport{DisableKeepAlives: true},
			headers: map[string]string{"Authorization": "Bearer " + uat.Token},
		},
	}
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   "http://" + ln.Addr().String() + "/mcp",
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

// TestIntegration_VisitorSendAndWatchOwnDM covers design §9 B11 & §10 AC6:
// a local mock OIDC issuer issues a JWT presented in X-Scion-Federation-Token;
// a linked project visitor sends a message to an agent via MCP send_message,
// reads the agent's reply via MCP read_messages (backed by /conversations/{id}/watch),
// logs principal_kind="visitor" in the MCP audit log, and is refused on another
// user's DM, a group conversation, the conversation list, and when hub.mcp_server
// is turned off.
func TestIntegration_VisitorSendAndWatchOwnDM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Local mock OIDC issuer (RSA key + JWKS endpoint).
	const (
		issuerURL = "https://idp-mcp-visitor.example.com"
		audience  = "https://hub-mcp-visitor.example.com"
		kid       = "mcp-visitor-key-1"
	)
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	jwksData, err := json.Marshal(jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       &privKey.PublicKey,
			KeyID:     kid,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}},
	})
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksData)
	}))
	t.Cleanup(jwksSrv.Close)

	signVisitorJWT := func(sub, email, name string) string {
		t.Helper()
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: privKey},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid),
		)
		if err != nil {
			t.Fatalf("NewSigner: %v", err)
		}
		now := time.Now()
		claims := map[string]any{
			"iss":            issuerURL,
			"sub":            sub,
			"aud":            audience,
			"iat":            jwt.NewNumericDate(now.Add(-time.Minute)),
			"nbf":            jwt.NewNumericDate(now.Add(-time.Minute)),
			"exp":            jwt.NewNumericDate(now.Add(5 * time.Minute)),
			"email":          email,
			"email_verified": true,
			"name":           name,
		}
		raw, err := jwt.Signed(signer).Claims(claims).Serialize()
		if err != nil {
			t.Fatalf("Sign JWT: %v", err)
		}
		return raw
	}

	// 2. Real in-process Hub with Federation enabled and both experiments on.
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
		Host:        "127.0.0.1",
		Port:        0,
		HubEndpoint: audience,
		Mode:        "dev",
		Federation: config.FederationConfig{
			Enabled: true,
			TrustedIssuers: []config.TrustedIssuerConfig{{
				IssuerURL:          issuerURL,
				IssuerType:         "user",
				JWKSURL:            jwksSrv.URL,
				ExpectedAudience:   audience,
				AllowVisitorGrants: true,
				AllowedEmails:      []string{"*@corp.example"},
			}},
		},
	}, st)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	hubSrv.SetDispatcher(&noopMessageDispatcher{})
	ops := hub.NewOperationalSettings(st, koanf.New("."), koanf.New("."))
	hubSrv.SetOperationalSettings(ops)

	// Enable both federation.project_visitors and hub.mcp_server via operational settings.
	var expRev int64
	setExperiments := func(projectVisitors, mcpEnabled bool) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"overrides": map[string]bool{
				experiments.FederationProjectVisitors: projectVisitors,
				experiments.MCPServer:                 mcpEnabled,
			},
		})
		if err != nil {
			t.Fatalf("marshal experiments: %v", err)
		}
		expRev, err = ops.Update(ctx, "experiments", raw, "test", expRev, "managed")
		if err != nil {
			t.Fatalf("ops.Update(experiments): %v", err)
		}
	}
	setExperiments(true, true)

	// 3. Seed project owner, project, agent, another user's DM, and a group conversation.
	owner := &store.User{
		ID:          uuid.NewString(),
		Email:       "owner@example.com",
		DisplayName: "Owner",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	if err := st.CreateUser(ctx, owner); err != nil {
		t.Fatalf("CreateUser(owner): %v", err)
	}
	hubMemberRole, err := st.GetRoleDefinitionByName(ctx, "hub-member", store.RoleScopeSystem)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName(hub-member): %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		PrincipalType:    "user",
		PrincipalID:      owner.ID,
		RoleDefinitionID: hubMemberRole.ID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        owner.ID,
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

	ownerJWT, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
		UserID:      owner.ID,
		Email:       owner.Email,
		DisplayName: owner.DisplayName,
		Role:        owner.Role,
		ClientType:  hub.ClientTypeWeb,
	})
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	ownerHubClient := inProcClient(hubclient.WithBearerToken(ownerJWT))

	proj, err := ownerHubClient.Projects().Create(ctx, &hubclient.CreateProjectRequest{
		Name: "Visitor MCP Project",
		Slug: "visitor-mcp",
	})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}

	agent := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "echo-v",
		Name:            "echo-v",
		ProjectID:       proj.ID,
		OwnerID:         owner.ID,
		CreatedBy:       owner.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "running",
		MessageMode:     "project",
		RunID:           "run-v1",
	}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	agentToken, agentCred, err := hubSrv.SignAgentToken(hub.AgentTokenGrant{
		AgentID:   agent.ID,
		ProjectID: proj.ID,
		Ancestry:  []string{owner.ID},
		Scopes:    []hub.AgentTokenScope{hub.ScopeAgentStatusUpdate, hub.ScopeProjectRead},
	}, "run-v1")
	if err != nil {
		t.Fatalf("SignAgentToken: %v", err)
	}
	if err := st.CreateAgentCredential(ctx, agentCred); err != nil {
		t.Fatalf("CreateAgentCredential: %v", err)
	}

	// Grant visitor alice@corp.example on proj via the real REST endpoint.
	grantBody, _ := json.Marshal(map[string]any{
		"issuer": issuerURL,
		"email":  "alice@corp.example",
	})
	grantReq := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+proj.ID+"/visitors", bytes.NewReader(grantBody))
	grantReq.Header.Set("Content-Type", "application/json")
	grantReq.Header.Set("Authorization", "Bearer "+ownerJWT)
	grantRec := httptest.NewRecorder()
	hubSrv.Handler().ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant visitor: status=%d body=%s", grantRec.Code, grantRec.Body.String())
	}
	var grantResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(grantRec.Body.Bytes(), &grantResp); err != nil {
		t.Fatalf("unmarshal grant response: %v", err)
	}
	visitorID := grantResp.ID
	if visitorID == "" {
		t.Fatal("grant response missing visitor ID")
	}

	// Seed another user's DM with agent and a group conversation in proj.
	now := time.Now().UTC()
	ownerDMKey, err := messages.DMConversationKey("agent", agent.ID, "user", owner.ID)
	if err != nil {
		t.Fatalf("DMConversationKey: %v", err)
	}
	otherDM := &store.Conversation{
		ID:             uuid.NewString(),
		Kind:           "direct",
		Surface:        "native",
		ExternalRef:    ownerDMKey,
		DriftState:     "active",
		LastActivityAt: now,
		CreatedAt:      now,
	}
	if err := st.CreateConversation(ctx, otherDM); err != nil {
		t.Fatalf("CreateConversation(otherDM): %v", err)
	}
	projIDCopy := proj.ID
	groupConv := &store.Conversation{
		ID:             uuid.NewString(),
		Kind:           "group",
		Surface:        "native",
		ProjectID:      &projIDCopy,
		DisplayName:    "visitor-forbidden-group",
		DriftState:     "active",
		LastActivityAt: now,
		CreatedAt:      now,
	}
	if err := st.CreateConversation(ctx, groupConv); err != nil {
		t.Fatalf("CreateConversation(groupConv): %v", err)
	}

	// 4. Start embedded MCP server wired to hubSrv.ExperimentEnabled directly.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		Version:    "visitor-integration-test",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, name string) (bool, error) {
			return hubSrv.ExperimentEnabled(name), nil
		},
		DisableTransportAuth: true,
	})
	mcpSrv, err := mcpserver.New(mcpserver.Options{
		Version: "visitor-integration-test",
		Backend: backend,
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srvCtx, srvCancel := context.WithCancel(ctx)
	srvDone := make(chan error, 1)
	go func() { srvDone <- mcpSrv.Serve(srvCtx, ln) }()
	defer func() {
		srvCancel()
		if err := <-srvDone; err != nil {
			t.Errorf("Serve shutdown error: %v", err)
		}
	}()

	visitorToken := signVisitorJWT("sub-alice", "alice@corp.example", "Alice Visitor")

	// 5. Connect MCP client using ONLY X-Scion-Federation-Token (no Authorization).
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "visitor-client", Version: "1.0"}, nil)
	hc := &http.Client{
		Transport: &headerRoundTripper{
			base:    &http.Transport{DisableKeepAlives: true},
			headers: map[string]string{mcpserver.HeaderFederationToken: visitorToken},
		},
	}
	sess, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   "http://" + ln.Addr().String() + "/mcp",
		HTTPClient: hc,
	}, nil)
	if err != nil {
		t.Fatalf("visitor MCP Connect failed: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// 6. Visitor sends message to echo-v.
	sendRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": proj.ID,
			"agent":   agent.Slug,
			"text":    "ping from visitor",
		},
	})
	if err != nil || sendRes.IsError {
		t.Fatalf("visitor send_message failed: err=%v res=%+v", err, sendRes)
	}
	var sendOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(sendRes.Content[0].(*mcp.TextContent).Text), &sendOut); err != nil {
		t.Fatalf("unmarshal sendOut: %v", err)
	}
	if sendOut.ConversationID == "" || sendOut.Cursor == "" {
		t.Fatalf("expected non-empty conversation_id and cursor, got: %+v", sendOut)
	}

	// Verify Open Question 6: DM conversation has Kind=direct and canonical ExternalRef.
	visitorDMKey, err := messages.DMConversationKey("agent", agent.ID, "user", visitorID)
	if err != nil {
		t.Fatalf("DMConversationKey: %v", err)
	}
	storedConv, err := st.GetConversation(ctx, sendOut.ConversationID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if storedConv.Kind != "direct" || storedConv.ExternalRef != visitorDMKey {
		t.Fatalf("stored DM = {Kind:%q ExternalRef:%q}, want {Kind:\"direct\" ExternalRef:%q}",
			storedConv.Kind, storedConv.ExternalRef, visitorDMKey)
	}

	// 7. Agent replies into the visitor's DM using its agent JWT.
	agentHubClient := inProcClient(hubclient.WithAgentToken(agentToken))
	if _, err := agentHubClient.Agents().SendOutboundMessage(ctx, agent.ID, &hubclient.OutboundMessageRequest{
		Recipient:      "user:" + visitorID,
		Msg:            "PONG to visitor",
		Type:           messages.TypeReply,
		ConversationID: sendOut.ConversationID,
	}); err != nil {
		t.Fatalf("agent reply failed: %v", err)
	}

	// 8. Visitor reads/watches its own DM from send_message's cursor and gets the reply.
	readRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"cursor":          sendOut.Cursor,
		},
	})
	if err != nil || readRes.IsError {
		t.Fatalf("visitor read_messages failed: err=%v res=%+v", err, readRes)
	}
	var readOut mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(readRes.Content[0].(*mcp.TextContent).Text), &readOut); err != nil {
		t.Fatalf("unmarshal readOut: %v", err)
	}
	if len(readOut.Messages) != 1 || readOut.Messages[0].Text != "PONG to visitor" {
		t.Fatalf("expected agent reply 'PONG to visitor', got: %+v", readOut.Messages)
	}

	// Verify audit log records principal_kind="visitor" and credential_class="federation".
	logText := logBuf.String()
	for _, want := range []string{
		`"principal_kind":"visitor"`,
		`"principal_id":"` + visitorID + `"`,
		`"credential_class":"federation"`,
	} {
		if !strings.Contains(logText, want) {
			t.Errorf("audit log missing %s; got:\n%s", want, logText)
		}
	}

	// 9. Refusals: another user's DM and a group conversation are refused via MCP.
	for _, tc := range []struct {
		name   string
		convID string
	}{
		{"other user DM", otherDM.ID},
		{"group conversation", groupConv.ID},
	} {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name: "read_messages",
			Arguments: map[string]any{
				"conversation_id": tc.convID,
			},
		})
		if err != nil {
			t.Fatalf("%s: unexpected transport error: %v", tc.name, err)
		}
		if !res.IsError {
			t.Fatalf("%s: expected tool error (403 refusal), got success: %+v", tc.name, res)
		}
	}

	// 10. Refusal: GET /api/v1/conversations (list) is refused for visitors.
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil)
	listReq.Header.Set(mcpserver.HeaderFederationToken, visitorToken)
	listRec := httptest.NewRecorder()
	hubSrv.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/v1/conversations for visitor: status=%d, want 403 (%s)", listRec.Code, listRec.Body.String())
	}

	// 11. Refusal: with hub.mcp_server turned off, Hub refuses /watch (403) and MCP returns 404.
	setExperiments(true, false)
	watchReq := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/"+sendOut.ConversationID+"/watch?wait=0", nil)
	watchReq.Header.Set(mcpserver.HeaderFederationToken, visitorToken)
	watchRec := httptest.NewRecorder()
	hubSrv.Handler().ServeHTTP(watchRec, watchReq)
	if watchRec.Code != http.StatusForbidden {
		t.Fatalf("GET /conversations/{id}/watch with hub.mcp_server off: status=%d, want 403 (%s)", watchRec.Code, watchRec.Body.String())
	}

	mcpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+ln.Addr().String()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	mcpReq.Header.Set("Content-Type", "application/json")
	mcpReq.Header.Set(mcpserver.HeaderFederationToken, visitorToken)
	mcpResp, err := http.DefaultClient.Do(mcpReq)
	if err != nil {
		t.Fatalf("POST /mcp with experiment off: %v", err)
	}
	_ = mcpResp.Body.Close()
	if mcpResp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /mcp with hub.mcp_server off: status=%d, want 404", mcpResp.StatusCode)
	}
}
