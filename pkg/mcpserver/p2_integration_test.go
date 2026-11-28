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
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/mcpserver"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

func TestIntegration_P2_ToolsResourcesStreamCapAndAuthz(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	hubSrv, st := newIntegrationHub(t)

	// Seed primary user (alice) and non-member user (bob).
	alice := &store.User{
		ID:          uuid.NewString(),
		Email:       "alice-p2@example.com",
		DisplayName: "Alice P2",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	bob := &store.User{
		ID:          uuid.NewString(),
		Email:       "bob-p2@example.com",
		DisplayName: "Bob P2",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	for _, u := range []*store.User{alice, bob} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser(%s): %v", u.Email, err)
		}
	}
	hubMemberRole, err := st.GetRoleDefinitionByName(ctx, "hub-member", store.RoleScopeSystem)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName: %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		PrincipalType:    "user",
		PrincipalID:      alice.ID,
		RoleDefinitionID: hubMemberRole.ID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        alice.ID,
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

	mintUserJWT := func(u *store.User) string {
		tok, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
			UserID:      u.ID,
			Email:       u.Email,
			DisplayName: u.DisplayName,
			Role:        u.Role,
			ClientType:  hub.ClientTypeCLI,
		})
		if err != nil {
			t.Fatalf("MintAccessToken(%s): %v", u.Email, err)
		}
		return tok
	}

	aliceJWT := mintUserJWT(alice)
	bobJWT := mintUserJWT(bob)
	aliceHub := inProcClient(hubclient.WithBearerToken(aliceJWT))

	proj, err := aliceHub.Projects().Create(ctx, &hubclient.CreateProjectRequest{
		Name: "MCP P2 Project",
		Slug: "mcp-p2",
	})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}

	agent1 := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "echo-p2",
		Name:            "echo-p2",
		ProjectID:       proj.ID,
		OwnerID:         alice.ID,
		CreatedBy:       alice.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "running",
		MessageMode:     "project",
		RunID:           "run-p2-1",
	}
	agent2 := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "helper-p2",
		Name:            "helper-p2",
		ProjectID:       proj.ID,
		OwnerID:         alice.ID,
		CreatedBy:       alice.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "stopped",
		MessageMode:     "project",
		RunID:           "run-p2-2",
	}
	for _, a := range []*store.Agent{agent1, agent2} {
		if err := st.CreateAgent(ctx, a); err != nil {
			t.Fatalf("CreateAgent(%s): %v", a.Slug, err)
		}
	}

	agentToken, agentCred, err := hubSrv.SignAgentToken(hub.AgentTokenGrant{
		AgentID:   agent1.ID,
		ProjectID: proj.ID,
		Ancestry:  []string{alice.ID},
		Scopes:    []hub.AgentTokenScope{hub.ScopeAgentStatusUpdate, hub.ScopeProjectRead},
	}, "run-p2-1")
	if err != nil {
		t.Fatalf("SignAgentToken: %v", err)
	}
	if err := st.CreateAgentCredential(ctx, agentCred); err != nil {
		t.Fatalf("CreateAgentCredential: %v", err)
	}
	agentHub := inProcClient(hubclient.WithAgentToken(agentToken))

	fullUAT, err := aliceHub.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "p2-full",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "agent:list", "agent:read", "agent:message", "inbox:read", "inbox:write"},
	})
	if err != nil {
		t.Fatalf("create full UAT: %v", err)
	}
	noAgentListUAT, err := aliceHub.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "p2-no-agent-list",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "agent:read", "inbox:read"},
	})
	if err != nil {
		t.Fatalf("create no-agent-list UAT: %v", err)
	}
	noInboxReadUAT, err := aliceHub.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "p2-no-inbox-read",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "agent:read", "agent:message"},
	})
	if err != nil {
		t.Fatalf("create no-inbox-read UAT: %v", err)
	}

	// Start embedded MCP server with MaxStreamsPerPrincipal=2 to test §9 B9.
	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		Version:    "p2-integration",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	mcpSrv, err := mcpserver.New(mcpserver.Options{
		Version:                "p2-integration",
		Backend:                backend,
		MaxStreamsPerPrincipal: 2,
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
			t.Errorf("mcpSrv.Serve shutdown error: %v", err)
		}
	}()

	endpoint := "http://" + ln.Addr().String() + "/mcp"
	connectClient := func(token string) *mcp.ClientSession {
		cl := mcp.NewClient(&mcp.Implementation{Name: "p2-client", Version: "1.0"}, nil)
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
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}

	session := connectClient(fullUAT.Token)

	// 1. AC1: tools/list returns exactly the 5 tools with schemas, annotations, and untrusted warnings.
	toolsRes, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(toolsRes.Tools) != 5 {
		t.Fatalf("ListTools count = %d, want 5", len(toolsRes.Tools))
	}
	toolMap := make(map[string]*mcp.Tool, 5)
	for _, tl := range toolsRes.Tools {
		toolMap[tl.Name] = tl
	}
	for _, name := range []string{"list_agents", "list_conversations", "read_messages", "wait_for_messages"} {
		tl := toolMap[name]
		if tl == nil || tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q missing ReadOnlyHint=true: %+v", name, tl)
		}
	}
	for _, name := range []string{"send_message", "list_conversations", "read_messages", "wait_for_messages"} {
		if !strings.Contains(strings.ToLower(toolMap[name].Description), "untrusted") {
			t.Errorf("tool %q description missing untrusted-content wording: %q", name, toolMap[name].Description)
		}
	}
	// O3: list_agents documents agent:list requirement.
	if !strings.Contains(toolMap["list_agents"].Description, "agent:list") {
		t.Errorf("list_agents description should mention agent:list: %q", toolMap["list_agents"].Description)
	}
	// O4 & R2: list_conversations documents DM exclusion, empty participants, and no page_token.
	if !strings.Contains(strings.ToLower(toolMap["list_conversations"].Description), "excludes dms") {
		t.Errorf("list_conversations description should note project filter excludes DMs: %q", toolMap["list_conversations"].Description)
	}
	if schemaBytes, err := json.Marshal(toolMap["list_conversations"].InputSchema); err == nil {
		if strings.Contains(string(schemaBytes), "page_token") {
			t.Errorf("list_conversations input schema must not advertise unsupported page_token: %s", string(schemaBytes))
		}
	}

	// 2. list_agents returns both agents in proj.ID.
	listAgentsRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_agents",
		Arguments: map[string]any{"project": proj.ID},
	})
	if err != nil || listAgentsRes.IsError {
		t.Fatalf("list_agents failed: err=%v res=%+v", err, listAgentsRes)
	}
	var agentsOut mcpserver.ListAgentsOutput
	if err := json.Unmarshal([]byte(listAgentsRes.Content[0].(*mcp.TextContent).Text), &agentsOut); err != nil {
		t.Fatalf("unmarshal list_agents: %v", err)
	}
	if len(agentsOut.Agents) != 2 {
		t.Fatalf("list_agents returned %d agents, want 2: %+v", len(agentsOut.Agents), agentsOut.Agents)
	}

	// 3. §9 B1: send_message -> cursor -> wait_for_messages returns exactly the reply; next_cursor advances.
	sendRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": proj.ID,
			"agent":   agent1.Slug,
			"text":    "ping-b1",
		},
	})
	if err != nil || sendRes.IsError {
		t.Fatalf("send_message failed: err=%v res=%+v", err, sendRes)
	}
	var sendOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(sendRes.Content[0].(*mcp.TextContent).Text), &sendOut); err != nil {
		t.Fatalf("unmarshal send_message: %v", err)
	}

	type waitCallResult struct {
		res *mcp.CallToolResult
		err error
	}
	waitCh := make(chan waitCallResult, 1)
	go func() {
		r, e := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "wait_for_messages",
			Arguments: map[string]any{
				"conversation_id": sendOut.ConversationID,
				"cursor":          sendOut.Cursor,
				"timeout_seconds": 10,
				"filter":          "others",
			},
		})
		waitCh <- waitCallResult{r, e}
	}()

	time.Sleep(150 * time.Millisecond)
	replyOut, err := agentHub.Agents().SendOutboundMessage(ctx, agent1.ID, &hubclient.OutboundMessageRequest{
		Recipient:      "user:" + alice.Email,
		ConversationID: sendOut.ConversationID,
		Msg:            "PONG-b1",
		Type:           messages.TypeReply,
	})
	if err != nil {
		t.Fatalf("agent SendOutboundMessage: %v", err)
	}

	wr := <-waitCh
	if wr.err != nil || wr.res.IsError {
		t.Fatalf("wait_for_messages failed: err=%v res=%+v", wr.err, wr.res)
	}
	var waitOut mcpserver.WaitForMessagesOutput
	if err := json.Unmarshal([]byte(wr.res.Content[0].(*mcp.TextContent).Text), &waitOut); err != nil {
		t.Fatalf("unmarshal wait_for_messages: %v", err)
	}
	if waitOut.TimedOut {
		t.Fatalf("expected timed_out=false, got true")
	}
	if len(waitOut.Messages) != 1 || waitOut.Messages[0].Text != "PONG-b1" {
		t.Fatalf("wait_for_messages messages = %+v, want [PONG-b1]", waitOut.Messages)
	}
	if waitOut.NextCursor == "" || waitOut.NextCursor == sendOut.Cursor {
		t.Fatalf("expected next_cursor to advance from %q, got %q", sendOut.Cursor, waitOut.NextCursor)
	}

	// 4. send_message to conversation_id delivers into the same conversation.
	convSendRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"text":            "follow-up-via-conversation-id",
		},
	})
	if err != nil || convSendRes.IsError {
		t.Fatalf("send_message(conversation_id) failed: err=%v res=%+v", err, convSendRes)
	}
	var convSendOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(convSendRes.Content[0].(*mcp.TextContent).Text), &convSendOut); err != nil {
		t.Fatalf("unmarshal conv send: %v", err)
	}
	if convSendOut.ConversationID != sendOut.ConversationID || convSendOut.MessageID == "" || convSendOut.Cursor == "" {
		t.Fatalf("unexpected conv send output: %+v", convSendOut)
	}

	// 5. list_conversations returns the DM conversation.
	listConvRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_conversations",
		Arguments: map[string]any{"limit": 10},
	})
	if err != nil || listConvRes.IsError {
		t.Fatalf("list_conversations failed: err=%v res=%+v", err, listConvRes)
	}
	var convsOut mcpserver.ListConversationsOutput
	if err := json.Unmarshal([]byte(listConvRes.Content[0].(*mcp.TextContent).Text), &convsOut); err != nil {
		t.Fatalf("unmarshal list_conversations: %v", err)
	}
	foundConv := false
	for _, c := range convsOut.Conversations {
		if c.ConversationID == sendOut.ConversationID && c.Kind == "direct" {
			foundConv = true
			break
		}
	}
	if !foundConv {
		t.Fatalf("list_conversations did not include DM %q: %+v", sendOut.ConversationID, convsOut.Conversations)
	}

	// 6. Read-only resource templates: list templates and read both conversation and message URIs.
	tmplRes, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(tmplRes.ResourceTemplates) != 2 {
		t.Fatalf("ListResourceTemplates count = %d, want 2", len(tmplRes.ResourceTemplates))
	}

	convURI := "scion://conversations/" + sendOut.ConversationID
	convRes, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: convURI})
	if err != nil {
		t.Fatalf("ReadResource(%s): %v", convURI, err)
	}
	if len(convRes.Contents) != 1 || convRes.Contents[0].MIMEType != "application/json" {
		t.Fatalf("unexpected conversation resource contents: %+v", convRes.Contents)
	}
	var convSummary mcpserver.ConversationSummary
	if err := json.Unmarshal([]byte(convRes.Contents[0].Text), &convSummary); err != nil {
		t.Fatalf("unmarshal conversation resource: %v", err)
	}
	if convSummary.ConversationID != sendOut.ConversationID || convSummary.Kind != "direct" {
		t.Fatalf("conversation resource = %+v, want id=%s kind=direct", convSummary, sendOut.ConversationID)
	}

	msgURI := "scion://conversations/" + sendOut.ConversationID + "/messages/" + replyOut.MessageID
	msgRes, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: msgURI})
	if err != nil {
		t.Fatalf("ReadResource(%s): %v", msgURI, err)
	}
	var msgDTO mcpserver.MessageDTO
	if err := json.Unmarshal([]byte(msgRes.Contents[0].Text), &msgDTO); err != nil {
		t.Fatalf("unmarshal message resource: %v", err)
	}
	if msgDTO.ID != replyOut.MessageID || msgDTO.Text != "PONG-b1" || msgDTO.Sender.Kind != "agent" {
		t.Fatalf("message resource = %+v, want id=%s text=PONG-b1 sender.kind=agent", msgDTO, replyOut.MessageID)
	}

	// 7. §9 B9: stream cap (-32025) when > MaxStreamsPerPrincipal (2) wait_for_messages run concurrently.
	capCtx, capCancel := context.WithCancel(ctx)
	var capWG sync.WaitGroup
	for i := 0; i < 2; i++ {
		capWG.Add(1)
		go func() {
			defer capWG.Done()
			s := connectClient(fullUAT.Token)
			_, _ = s.CallTool(capCtx, &mcp.CallToolParams{
				Name: "wait_for_messages",
				Arguments: map[string]any{
					"conversation_id": sendOut.ConversationID,
					"cursor":          convSendOut.Cursor,
					"timeout_seconds": 10,
				},
			})
		}()
	}
	// Give the two long-polls time to acquire their stream slots.
	time.Sleep(250 * time.Millisecond)

	_, capErr := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "wait_for_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"cursor":          convSendOut.Cursor,
			"timeout_seconds": 5,
		},
	})
	var rpcErr *jsonrpc.Error
	if !errors.As(capErr, &rpcErr) || rpcErr.Code != mcpserver.CodeResourceExhausted {
		capCancel()
		capWG.Wait()
		t.Fatalf("3rd concurrent wait_for_messages err = %v, want JSON-RPC %d", capErr, mcpserver.CodeResourceExhausted)
	}
	capCancel()
	capWG.Wait()

	// 8. §9 B10: Authz parity for new tools and resources.
	// Token has project:read + agent:read + inbox:read, but lacks agent:list:
	// Hub answers 404 not_found ("Project not found") to avoid an existence oracle.
	noAgentListSession := connectClient(noAgentListUAT.Token)
	deniedList, err := noAgentListSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_agents",
		Arguments: map[string]any{"project": proj.ID},
	})
	if err != nil || !deniedList.IsError {
		t.Fatalf("list_agents without agent:list: err=%v res=%+v, want tool error", err, deniedList)
	}
	if txt := deniedList.Content[0].(*mcp.TextContent).Text; txt != "hub error (404 not_found): Project not found" {
		t.Fatalf("list_agents without agent:list error = %q, want %q", txt, "hub error (404 not_found): Project not found")
	}

	// Token lacks inbox:read: Hub answers 403 forbidden ("Insufficient permissions").
	noInboxSession := connectClient(noInboxReadUAT.Token)
	deniedConvs, err := noInboxSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_conversations",
		Arguments: map[string]any{},
	})
	if err != nil || !deniedConvs.IsError {
		t.Fatalf("list_conversations without inbox:read: err=%v res=%+v, want tool error", err, deniedConvs)
	}
	if txt := deniedConvs.Content[0].(*mcp.TextContent).Text; txt != "hub error (403 forbidden): Insufficient permissions" {
		t.Fatalf("list_conversations without inbox:read error = %q, want %q", txt, "hub error (403 forbidden): Insufficient permissions")
	}

	deniedWait, err := noInboxSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "wait_for_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"timeout_seconds": 1,
		},
	})
	if err != nil || !deniedWait.IsError {
		t.Fatalf("wait_for_messages without inbox:read: err=%v res=%+v, want tool error", err, deniedWait)
	}

	if _, err := noInboxSession.ReadResource(ctx, &mcp.ReadResourceParams{URI: msgURI}); err == nil {
		t.Fatal("expected error reading message resource without inbox:read")
	}

	// Non-member (bob) cannot read alice's DM or list alice's project agents.
	bobSession := connectClient(bobJWT)
	bobAgents, err := bobSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_agents",
		Arguments: map[string]any{"project": proj.ID},
	})
	if err != nil || !bobAgents.IsError {
		t.Fatalf("non-member list_agents: err=%v res=%+v, want tool error", err, bobAgents)
	}
	if _, err := bobSession.ReadResource(ctx, &mcp.ReadResourceParams{URI: convURI}); err == nil {
		t.Fatal("non-member should not be able to read alice's DM resource")
	}
}

func TestIntegration_P2_AuditM1_HubTrustedProxiesXFF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build an unsigned Google-shaped JWT (triggers Hub's externalBearerRateLimiter
	// on /api/v1/auth/me when a Google user issuer is configured).
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://accounts.google.com","sub":"fake"}`))
	fakeGoogleJWT := hdr + "." + payload + ".invalidsig"

	runCase := func(t *testing.T, trustedProxies []string, wantHub429 bool) {
		t.Helper()
		dbPath := filepath.Join(t.TempDir(), "hub.db")
		entClient, err := entc.OpenSQLite(dbPath, entc.PoolConfig{MaxOpenConns: 1})
		if err != nil {
			t.Fatalf("OpenSQLite: %v", err)
		}
		if err := entc.AutoMigrate(ctx, entClient); err != nil {
			t.Fatalf("AutoMigrate: %v", err)
		}
		st := entadapter.NewCompositeStore(entClient)
		t.Cleanup(func() { _ = st.Close() })

		hubSrv, err := hub.New(hub.ServerConfig{
			Host:           "127.0.0.1",
			Port:           0,
			TrustedProxies: trustedProxies,
			Federation: config.FederationConfig{
				Enabled: true,
				TrustedIssuers: []config.TrustedIssuerConfig{
					{
						IssuerURL:        "https://accounts.google.com",
						IssuerType:       "user",
						ExpectedAudience: "mcp-audit-m1-test",
					},
				},
			},
		}, st)
		if err != nil {
			t.Fatalf("hub.New: %v", err)
		}

		// Record the Hub's actual HTTP status codes and forwarded XFF headers.
		var mu sync.Mutex
		var hub429Count int
		var sawXFF int
		spyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
			hubSrv.Handler().ServeHTTP(rec, r)
			if r.URL.Path == "/api/v1/auth/me" {
				mu.Lock()
				if r.Header.Get("X-Forwarded-For") != "" {
					sawXFF++
				}
				if rec.status == http.StatusTooManyRequests {
					hub429Count++
				}
				mu.Unlock()
			}
		})

		backend := mcpserver.NewBackend(mcpserver.BackendOptions{
			HubBaseURL: "http://in-process",
			Transport:  &mcpserver.InProcessTransport{Handler: spyHandler},
			ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, _ string) (bool, error) {
				return true, nil
			},
			DisableTransportAuth: true,
		})
		mcpSrv, err := mcpserver.New(mcpserver.Options{Backend: backend})
		if err != nil {
			t.Fatalf("mcpserver.New: %v", err)
		}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srvCtx, srvCancel := context.WithCancel(ctx)
		srvDone := make(chan error, 1)
		go func() { srvDone <- mcpSrv.Serve(srvCtx, ln) }()
		defer func() {
			srvCancel()
			if err := <-srvDone; err != nil {
				t.Errorf("mcpSrv.Serve shutdown error: %v", err)
			}
		}()

		// Send 25 distinct uncached requests (exceeding the per-IP burst of 20) from distinct XFF IPs.
		initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"xff-client","version":"1.0"}}}`
		for i := 1; i <= 25; i++ {
			token := fmt.Sprintf("%s.%d", fakeGoogleJWT, i)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+ln.Addr().String()+"/mcp", strings.NewReader(initBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /mcp #%d: %v", i, err)
			}
			_ = resp.Body.Close()
		}

		mu.Lock()
		got429 := hub429Count
		gotXFF := sawXFF
		mu.Unlock()

		if gotXFF != 25 {
			t.Fatalf("Hub saw X-Forwarded-For on %d/25 requests, want 25", gotXFF)
		}
		if wantHub429 && got429 == 0 {
			t.Fatalf("untrusted proxy: expected Hub 429 rate-limit hits when 25 requests collapse to 127.0.0.1, got 0")
		}
		if !wantHub429 && got429 > 0 {
			t.Fatalf("trusted proxy: expected 0 Hub 429s across 25 distinct XFF client IPs, got %d", got429)
		}
	}

	t.Run("trusted_proxy_honours_xff", func(t *testing.T) {
		runCase(t, []string{"127.0.0.0/8"}, false)
	})
	t.Run("untrusted_proxy_ignores_xff", func(t *testing.T) {
		runCase(t, nil, true)
	})
}

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func TestIntegration_P2_ShutdownDrainsInFlightWaitForMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hubSrv, st := newIntegrationHub(t)

	user := &store.User{
		ID:          uuid.NewString(),
		Email:       "drain-p2@example.com",
		DisplayName: "Drain P2",
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
	userHub := inProcClient(hubclient.WithBearerToken(userJWT))
	proj, err := userHub.Projects().Create(ctx, &hubclient.CreateProjectRequest{Name: "Drain Proj", Slug: "drain-proj"})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}
	agent := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "drain-agent",
		Name:            "drain-agent",
		ProjectID:       proj.ID,
		OwnerID:         user.ID,
		CreatedBy:       user.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "running",
		MessageMode:     "project",
		RunID:           "run-drain-1",
	}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	sendResp, err := userHub.ProjectAgents(proj.ID).SendStructuredMessageWithOptions(ctx, agent.Slug, &messages.StructuredMessage{
		Version: messages.Version,
		Msg:     "seed",
		Type:    messages.TypeInstruction,
	}, hubclient.SendMessageOptions{})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}

	var drainCtxUsed bool
	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	mcpSrv, err := mcpserver.New(mcpserver.Options{
		Backend: backend,
		ShutdownContext: func(parent context.Context) (context.Context, context.CancelFunc) {
			drainCtxUsed = true
			return context.WithTimeout(parent, 5*time.Second)
		},
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srvCtx, srvCancel := context.WithCancel(ctx)
	srvDone := make(chan error, 1)
	go func() { srvDone <- mcpSrv.Serve(srvCtx, ln) }()

	cl := mcp.NewClient(&mcp.Implementation{Name: "drain-client", Version: "1.0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: "http://" + ln.Addr().String() + "/mcp",
		HTTPClient: &http.Client{
			Transport: &headerRoundTripper{
				base:    &http.Transport{DisableKeepAlives: true},
				headers: map[string]string{"Authorization": "Bearer " + userJWT},
			},
		},
	}, nil)
	if err != nil {
		srvCancel()
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Launch a 30s wait_for_messages in flight.
	waitDone := make(chan error, 1)
	go func() {
		_, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name: "wait_for_messages",
			Arguments: map[string]any{
				"conversation_id": sendResp.ConversationID,
				"cursor":          sendResp.Cursor,
				"timeout_seconds": 30,
			},
		})
		waitDone <- err
	}()
	time.Sleep(200 * time.Millisecond)

	// Simulate shutdownPhaseMark (Hub + MCP BeginShutdown) followed by cancel.
	start := time.Now()
	hubSrv.BeginShutdown()
	mcpSrv.BeginShutdown()
	srvCancel()

	select {
	case err := <-srvDone:
		if err != nil {
			t.Fatalf("mcpSrv.Serve returned error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mcpSrv.Serve did not drain within 5s")
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Logf("in-flight wait_for_messages ended on shutdown with: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight wait_for_messages did not end on shutdown")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s, expected prompt drain well under 30s wait", elapsed)
	}
	if !drainCtxUsed {
		t.Fatal("expected ShutdownContext hook to be used during Serve shutdown")
	}
}

// TestIntegration_P2_GroupConversationSendAndWait exercises:
//   - R1: send_message{conversation_id} to a project group conversation backed
//     by a real WebChatStore (both user JWT and UAT), read_messages sees it,
//     and wait_for_messages from the returned cursor sees only later messages.
//   - R2: list_conversations surfaces Hub truncated=true and total when > limit.
//   - O1: send_message{conversation_id} to an agent<->agent DM fails rather
//     than silently landing in the caller's own DM with either agent.
func TestIntegration_P2_GroupConversationSendAndWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "hub-webchat.db")
	entClient, err := entc.OpenSQLite(dbPath, entc.PoolConfig{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("entc.OpenSQLite: %v", err)
	}
	if err := entc.AutoMigrate(ctx, entClient); err != nil {
		t.Fatalf("entc.AutoMigrate: %v", err)
	}
	st := entadapter.NewCompositeStore(entClient)
	t.Cleanup(func() { _ = st.Close() })

	hubSrv, err := hub.New(hub.ServerConfig{Host: "127.0.0.1", Port: 0}, st)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	hubSrv.SetDispatcher(&noopMessageDispatcher{})

	chatDB, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("sql.Open chatDB: %v", err)
	}
	chatDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = chatDB.Close() })
	wcs := hub.NewWebChatStore(chatDB, "sqlite")
	if err := wcs.Init(); err != nil {
		t.Fatalf("wcs.Init: %v", err)
	}
	hubSrv.SetWebChatStore(wcs)

	alice := &store.User{
		ID:          uuid.NewString(),
		Email:       "alice-grp@example.com",
		DisplayName: "Alice Group",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	if err := st.CreateUser(ctx, alice); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	hubMemberRole, err := st.GetRoleDefinitionByName(ctx, "hub-member", store.RoleScopeSystem)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName: %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		PrincipalType:    "user",
		PrincipalID:      alice.ID,
		RoleDefinitionID: hubMemberRole.ID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        alice.ID,
	}); err != nil {
		t.Fatalf("CreateRoleBinding: %v", err)
	}

	userJWT, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
		UserID:      alice.ID,
		Email:       alice.Email,
		DisplayName: alice.DisplayName,
		Role:        alice.Role,
		ClientType:  hub.ClientTypeCLI,
	})
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	hc := &http.Client{Transport: &mcpserver.InProcessTransport{Handler: hubSrv.Handler()}}
	aliceHub, err := hubclient.New("http://in-process",
		hubclient.WithHTTPClient(hc),
		hubclient.WithBearerToken(userJWT),
		hubclient.WithTransportAuth(nil, 0),
	)
	if err != nil {
		t.Fatalf("hubclient.New: %v", err)
	}

	proj, err := aliceHub.Projects().Create(ctx, &hubclient.CreateProjectRequest{
		Name: "Group Test Project",
		Slug: "grp-test",
	})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}

	// Create two group conversations (so limit=1 triggers truncation for R2).
	conv1, err := aliceHub.Conversations().Create(ctx, &hubclient.CreateConversationRequest{
		DisplayName: "group-alpha",
		ProjectID:   proj.ID,
		Kind:        "group",
	})
	if err != nil {
		t.Fatalf("Conversations.Create(group-alpha): %v", err)
	}
	if _, err := aliceHub.Conversations().Create(ctx, &hubclient.CreateConversationRequest{
		DisplayName: "group-beta",
		ProjectID:   proj.ID,
		Kind:        "group",
	}); err != nil {
		t.Fatalf("Conversations.Create(group-beta): %v", err)
	}

	// Create two agents and an agent<->agent DM conversation for O1.
	ag1 := &store.Agent{
		ID: uuid.NewString(), Slug: "ag-one", Name: "ag-one",
		ProjectID: proj.ID, OwnerID: alice.ID, CreatedBy: alice.ID,
		RuntimeBrokerID: uuid.NewString(), Phase: "running", MessageMode: "project", RunID: "r1",
	}
	ag2 := &store.Agent{
		ID: uuid.NewString(), Slug: "ag-two", Name: "ag-two",
		ProjectID: proj.ID, OwnerID: alice.ID, CreatedBy: alice.ID,
		RuntimeBrokerID: uuid.NewString(), Phase: "running", MessageMode: "project", RunID: "r2",
	}
	for _, a := range []*store.Agent{ag1, ag2} {
		if err := st.CreateAgent(ctx, a); err != nil {
			t.Fatalf("CreateAgent(%s): %v", a.Slug, err)
		}
	}
	a1, a2 := ag1.ID, ag2.ID
	if a1 > a2 {
		a1, a2 = a2, a1
	}
	agentAgentDM := &store.Conversation{
		ID:          uuid.NewString(),
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: fmt.Sprintf("dm:agent:%s:agent:%s", a1, a2),
		DriftState:  "active",
	}
	if err := st.CreateConversation(ctx, agentAgentDM); err != nil {
		t.Fatalf("CreateConversation(agent-agent DM): %v", err)
	}

	uat, err := aliceHub.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "grp-uat",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "agent:list", "agent:read", "agent:message", "inbox:read", "inbox:write"},
	})
	if err != nil {
		t.Fatalf("Tokens.Create: %v", err)
	}

	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		Version:    "grp-test",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	mcpSrv, err := mcpserver.New(mcpserver.Options{Version: "grp-test", Backend: backend})
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
			t.Errorf("mcpSrv.Serve shutdown error: %v", err)
		}
	}()

	connect := func(token string) *mcp.ClientSession {
		cl := mcp.NewClient(&mcp.Implementation{Name: "grp-client", Version: "1.0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: "http://" + ln.Addr().String() + "/mcp",
			HTTPClient: &http.Client{
				Transport: &headerRoundTripper{
					base:    &http.Transport{DisableKeepAlives: true},
					headers: map[string]string{"Authorization": "Bearer " + token},
				},
			},
		}, nil)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}

	// R1: send_message{conversation_id} to a group conversation succeeds for both JWT and UAT.
	uatSess := connect(uat.Token)
	sendRes, err := uatSess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"conversation_id": conv1.ID,
			"text":            "hello-group-first",
		},
	})
	if err != nil || sendRes.IsError {
		t.Fatalf("UAT group send_message failed: err=%v res=%+v", err, sendRes)
	}
	var firstOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(sendRes.Content[0].(*mcp.TextContent).Text), &firstOut); err != nil {
		t.Fatalf("unmarshal firstOut: %v", err)
	}
	if firstOut.MessageID == "" || firstOut.ConversationID != conv1.ID || firstOut.Cursor == "" {
		t.Fatalf("unexpected group send result: %+v", firstOut)
	}

	// Verify read_messages sees "hello-group-first".
	readRes, err := uatSess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_messages",
		Arguments: map[string]any{"conversation_id": conv1.ID},
	})
	if err != nil || readRes.IsError {
		t.Fatalf("read_messages(group) failed: err=%v res=%+v", err, readRes)
	}
	var readOut mcpserver.ReadMessagesOutput
	if err := json.Unmarshal([]byte(readRes.Content[0].(*mcp.TextContent).Text), &readOut); err != nil {
		t.Fatalf("unmarshal readOut: %v", err)
	}
	if len(readOut.Messages) != 1 || readOut.Messages[0].Text != "hello-group-first" {
		t.Fatalf("read_messages(group) = %+v, want [hello-group-first]", readOut.Messages)
	}

	// Verify wait_for_messages from firstOut.Cursor returns ONLY a subsequent message.
	jwtSess := connect(userJWT)
	type waitRes struct {
		res *mcp.CallToolResult
		err error
	}
	waitCh := make(chan waitRes, 1)
	go func() {
		r, e := uatSess.CallTool(ctx, &mcp.CallToolParams{
			Name: "wait_for_messages",
			Arguments: map[string]any{
				"conversation_id": conv1.ID,
				"cursor":          firstOut.Cursor,
				"timeout_seconds": 10,
				"filter":          "all",
			},
		})
		waitCh <- waitRes{r, e}
	}()
	time.Sleep(150 * time.Millisecond)

	secondRes, err := jwtSess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"conversation_id": conv1.ID,
			"text":            "hello-group-second",
		},
	})
	if err != nil || secondRes.IsError {
		t.Fatalf("JWT group send_message failed: err=%v res=%+v", err, secondRes)
	}

	wr := <-waitCh
	if wr.err != nil || wr.res.IsError {
		t.Fatalf("wait_for_messages(group) failed: err=%v res=%+v", wr.err, wr.res)
	}
	var waitOut mcpserver.WaitForMessagesOutput
	if err := json.Unmarshal([]byte(wr.res.Content[0].(*mcp.TextContent).Text), &waitOut); err != nil {
		t.Fatalf("unmarshal waitOut: %v", err)
	}
	if len(waitOut.Messages) != 1 || waitOut.Messages[0].Text != "hello-group-second" {
		t.Fatalf("wait_for_messages(group) = %+v, want [hello-group-second]", waitOut.Messages)
	}

	// R2: list_conversations with limit=1 reports truncated=true and total >= 2.
	listRes, err := uatSess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_conversations",
		Arguments: map[string]any{"limit": 1},
	})
	if err != nil || listRes.IsError {
		t.Fatalf("list_conversations(limit=1) failed: err=%v res=%+v", err, listRes)
	}
	var listOut mcpserver.ListConversationsOutput
	if err := json.Unmarshal([]byte(listRes.Content[0].(*mcp.TextContent).Text), &listOut); err != nil {
		t.Fatalf("unmarshal listOut: %v", err)
	}
	if len(listOut.Conversations) != 1 || !listOut.Truncated || listOut.Total < 2 {
		t.Fatalf("list_conversations(limit=1) = %+v, want len=1 truncated=true total>=2", listOut)
	}

	// O1: sending to an agent<->agent DM must fail and must NOT create a message in alice's own DM.
	badDMRes, err := jwtSess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"conversation_id": agentAgentDM.ID,
			"text":            "must-not-reroute",
		},
	})
	if err != nil {
		t.Fatalf("send_message(agent-agent DM) protocol error: %v", err)
	}
	if !badDMRes.IsError {
		t.Fatalf("expected tool error sending to agent<->agent DM, got success: %+v", badDMRes.Content)
	}
	// Confirm alice has no DM conversation with ag1 or ag2.
	aliceConvs, err := st.GetConversationsForPrincipal(ctx, "user", alice.ID)
	if err != nil {
		t.Fatalf("GetConversationsForPrincipal: %v", err)
	}
	for _, c := range aliceConvs {
		if c.Kind == "direct" {
			t.Fatalf("O1 regression: agent<->agent DM send silently created/landed in alice's DM %q (%s)", c.ID, c.ExternalRef)
		}
	}
}

// groupRaceTransport intercepts a 201 response from POST
// /api/v1/chat/conversations/{threadID}/messages and immediately commits a
// concurrent message from another user before returning the response to
// SendToConversation. On the old Watch(AfterMessage, Limit 1) implementation,
// the returned cursor jumped past the injected message and wait_for_messages
// silently skipped it (R1).
type groupRaceTransport struct {
	base    http.RoundTripper
	onReply func() error
	fired   bool
	mu      sync.Mutex
}

func (r *groupRaceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err != nil || req.Method != http.MethodPost || !strings.Contains(req.URL.Path, "/api/v1/chat/conversations/") || !strings.HasSuffix(req.URL.Path, "/messages") {
		return resp, err
	}
	if resp.StatusCode == http.StatusCreated {
		r.mu.Lock()
		already := r.fired
		r.fired = true
		r.mu.Unlock()
		if !already {
			if cbErr := r.onReply(); cbErr != nil {
				_ = resp.Body.Close()
				return nil, cbErr
			}
		}
	}
	return resp, nil
}

// TestIntegration_P2_GroupSendCursorDoesNotSkipConcurrentMessage is the R1
// regression test: a message from another user committed immediately after the
// group chat POST returns 201 must still be delivered by
// wait_for_messages(send.Cursor, filter=others|all).
func TestIntegration_P2_GroupSendCursorDoesNotSkipConcurrentMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "hub-group-race.db")
	entClient, err := entc.OpenSQLite(dbPath, entc.PoolConfig{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("entc.OpenSQLite: %v", err)
	}
	if err := entc.AutoMigrate(ctx, entClient); err != nil {
		t.Fatalf("entc.AutoMigrate: %v", err)
	}
	st := entadapter.NewCompositeStore(entClient)
	t.Cleanup(func() { _ = st.Close() })

	hubSrv, err := hub.New(hub.ServerConfig{Host: "127.0.0.1", Port: 0}, st)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	hubSrv.SetDispatcher(&noopMessageDispatcher{})

	chatDB, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("sql.Open chatDB: %v", err)
	}
	chatDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = chatDB.Close() })
	wcs := hub.NewWebChatStore(chatDB, "sqlite")
	if err := wcs.Init(); err != nil {
		t.Fatalf("wcs.Init: %v", err)
	}
	hubSrv.SetWebChatStore(wcs)

	alice := &store.User{ID: uuid.NewString(), Email: "alice-gr@example.com", DisplayName: "Alice GR", Role: store.UserRoleMember, Status: "active"}
	bob := &store.User{ID: uuid.NewString(), Email: "bob-gr@example.com", DisplayName: "Bob GR", Role: store.UserRoleMember, Status: "active"}
	for _, u := range []*store.User{alice, bob} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser(%s): %v", u.Email, err)
		}
	}
	hubMemberRole, err := st.GetRoleDefinitionByName(ctx, "hub-member", store.RoleScopeSystem)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName: %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID: uuid.NewString(), PrincipalType: "user", PrincipalID: alice.ID,
		RoleDefinitionID: hubMemberRole.ID, ScopeType: store.RoleScopeSystem, CreatedBy: alice.ID,
	}); err != nil {
		t.Fatalf("CreateRoleBinding(alice): %v", err)
	}

	mint := func(u *store.User) string {
		tok, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
			UserID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role, ClientType: hub.ClientTypeCLI,
		})
		if err != nil {
			t.Fatalf("MintAccessToken: %v", err)
		}
		return tok
	}
	aliceJWT := mint(alice)
	bobJWT := mint(bob)

	hc := &http.Client{Transport: &mcpserver.InProcessTransport{Handler: hubSrv.Handler()}}
	aliceHub, err := hubclient.New("http://in-process", hubclient.WithHTTPClient(hc), hubclient.WithBearerToken(aliceJWT), hubclient.WithTransportAuth(nil, 0))
	if err != nil {
		t.Fatalf("hubclient.New: %v", err)
	}
	proj, err := aliceHub.Projects().Create(ctx, &hubclient.CreateProjectRequest{Name: "Group Race", Slug: "grp-race"})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}
	// Grant bob project-member so bob can post to the group thread.
	projMemberRole, err := st.GetRoleDefinitionByName(ctx, "project-member", store.RoleScopeProject)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName(project-member): %v", err)
	}
	if _, err := st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID: uuid.NewString(), PrincipalType: "user", PrincipalID: bob.ID,
		RoleDefinitionID: projMemberRole.ID, ScopeType: store.RoleScopeProject, ScopeID: proj.ID, CreatedBy: alice.ID,
	}); err != nil {
		t.Fatalf("CreateRoleBinding(bob project-member): %v", err)
	}

	conv, err := aliceHub.Conversations().Create(ctx, &hubclient.CreateConversationRequest{
		DisplayName: "race-group", ProjectID: proj.ID, Kind: "group",
	})
	if err != nil {
		t.Fatalf("Conversations.Create: %v", err)
	}
	parts := strings.SplitN(conv.ExternalRef, ":", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected group ExternalRef %q", conv.ExternalRef)
	}
	threadID := parts[2]

	raceRT := &groupRaceTransport{
		base: &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		onReply: func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				"http://in-process/api/v1/chat/conversations/"+threadID+"/messages",
				strings.NewReader(`{"content":"injected-race-from-bob"}`))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+bobJWT)
			req.Header.Set("Content-Type", "application/json")
			resp, err := hc.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusCreated {
				return fmt.Errorf("bob inject status = %d, want 201", resp.StatusCode)
			}
			return nil
		},
	}

	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  raceRT,
		Version:    "grp-race-test",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	mcpSrv, err := mcpserver.New(mcpserver.Options{Version: "grp-race-test", Backend: backend})
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
			t.Errorf("mcpSrv.Serve shutdown error: %v", err)
		}
	}()

	cl := mcp.NewClient(&mcp.Implementation{Name: "grp-race-client", Version: "1.0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: "http://" + ln.Addr().String() + "/mcp",
		HTTPClient: &http.Client{
			Transport: &headerRoundTripper{
				base:    &http.Transport{DisableKeepAlives: true},
				headers: map[string]string{"Authorization": "Bearer " + aliceJWT},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	sendRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"conversation_id": conv.ID,
			"text":            "alice-trigger",
		},
	})
	if err != nil || sendRes.IsError {
		t.Fatalf("send_message failed: err=%v res=%+v", err, sendRes)
	}
	var sendOut mcpserver.SendResult
	if err := json.Unmarshal([]byte(sendRes.Content[0].(*mcp.TextContent).Text), &sendOut); err != nil {
		t.Fatalf("unmarshal sendOut: %v", err)
	}
	if sendOut.Cursor == "" {
		t.Fatalf("expected non-empty atomic cursor on group send, got %+v", sendOut)
	}

	// wait_for_messages(sendOut.Cursor, filter=others) MUST return bob's injected message.
	waitRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "wait_for_messages",
		Arguments: map[string]any{
			"conversation_id": conv.ID,
			"cursor":          sendOut.Cursor,
			"timeout_seconds": 3,
			"filter":          "others",
		},
	})
	if err != nil || waitRes.IsError {
		t.Fatalf("wait_for_messages failed: err=%v res=%+v", err, waitRes)
	}
	var waitOut mcpserver.WaitForMessagesOutput
	if err := json.Unmarshal([]byte(waitRes.Content[0].(*mcp.TextContent).Text), &waitOut); err != nil {
		t.Fatalf("unmarshal waitOut: %v", err)
	}
	if waitOut.TimedOut || len(waitOut.Messages) != 1 || waitOut.Messages[0].Text != "injected-race-from-bob" {
		t.Fatalf("R1 regression: wait_for_messages missed concurrent group message: %+v", waitOut)
	}
}
