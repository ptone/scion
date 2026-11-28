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
	noAgentReadUAT, err := aliceHub.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "p2-no-agent-read",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "inbox:read"},
	})
	if err != nil {
		t.Fatalf("create no-agent-read UAT: %v", err)
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
	noAgentSession := connectClient(noAgentReadUAT.Token)
	deniedList, err := noAgentSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_agents",
		Arguments: map[string]any{"project": proj.ID},
	})
	if err != nil || !deniedList.IsError {
		t.Fatalf("list_agents without agent:read: err=%v res=%+v, want tool error", err, deniedList)
	}
	if txt := deniedList.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "403") && !strings.Contains(txt, "404") {
		t.Fatalf("expected Hub authz denial (403/404) for list_agents without agent:list, got: %q", txt)
	}

	noInboxSession := connectClient(noInboxReadUAT.Token)
	deniedConvs, err := noInboxSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_conversations",
		Arguments: map[string]any{},
	})
	if err != nil || !deniedConvs.IsError {
		t.Fatalf("list_conversations without inbox:read: err=%v res=%+v, want tool error", err, deniedConvs)
	}
	if txt := deniedConvs.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "403") {
		t.Fatalf("expected Hub 403 reason for list_conversations without inbox:read, got: %q", txt)
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
