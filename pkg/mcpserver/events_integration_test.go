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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/eventfeed"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/mcpserver"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/knadh/koanf/v2"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type intSSEFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

func startStreamReader(body io.Reader) <-chan intSSEFrame {
	ch := make(chan intSSEFrame, 32)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var dataLines []string
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if len(dataLines) > 0 {
					var f intSSEFrame
					if json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &f) == nil {
						ch <- f
					}
					dataLines = nil
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(dataLines) > 0 {
			var f intSSEFrame
			if json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &f) == nil {
				ch <- f
			}
		}
	}()
	return ch
}

type eventsFixture struct {
	hubSrv        *hub.Server
	st            store.Store
	user          *store.User
	projID        string
	agent         *store.Agent
	userHubClient hubclient.Client
	agentClient   hubclient.Client
	fullUAT       *hubclient.CreateTokenResponse
	mcpSrv        *mcpserver.Server
	endpoint      string
	stopMCP       func()
}

func setupEventsFixture(t *testing.T, streamWait time.Duration) *eventsFixture {
	t.Helper()
	ctx := context.Background()
	hubSrv, st := newIntegrationHub(t)
	rawExp, err := json.Marshal(map[string]map[string]bool{"overrides": {experiments.MCPServer: true}})
	if err != nil {
		t.Fatalf("marshal experiments: %v", err)
	}
	if _, err := st.UpsertHubSetting(ctx, "experiments", rawExp, "test", 0, "managed"); err != nil {
		t.Fatalf("UpsertHubSetting(experiments): %v", err)
	}
	ops := hub.NewOperationalSettings(st, koanf.New("."), koanf.New("."))
	if _, err := ops.Refresh(ctx); err != nil {
		t.Fatalf("ops.Refresh: %v", err)
	}
	hubSrv.SetOperationalSettings(ops)

	user := &store.User{
		ID:          uuid.NewString(),
		Email:       "events-alice@example.com",
		DisplayName: "Alice Events",
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
		t.Fatalf("CreateRoleBinding: %v", err)
	}

	inProcClient := func(opt hubclient.Option) hubclient.Client {
		hc := &http.Client{Transport: &mcpserver.InProcessTransport{Handler: hubSrv.Handler()}}
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

	userJWT, _, err := hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
		UserID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Role: user.Role, ClientType: hub.ClientTypeCLI,
	})
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	userHubClient := inProcClient(hubclient.WithBearerToken(userJWT))

	proj, err := userHubClient.Projects().Create(ctx, &hubclient.CreateProjectRequest{
		Name: "Events Test Project",
		Slug: "events-test-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}

	agent := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            "echo-ev",
		Name:            "echo-ev",
		ProjectID:       proj.ID,
		OwnerID:         user.ID,
		CreatedBy:       user.ID,
		RuntimeBrokerID: uuid.NewString(),
		Phase:           "running",
		MessageMode:     "project",
		RunID:           "run-ev-1",
	}
	if err := st.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	agentToken, agentCred, err := hubSrv.SignAgentToken(hub.AgentTokenGrant{
		AgentID:   agent.ID,
		ProjectID: proj.ID,
		Ancestry:  []string{user.ID},
		Scopes:    []hub.AgentTokenScope{hub.ScopeAgentStatusUpdate, hub.ScopeProjectRead},
	}, "run-ev-1")
	if err != nil {
		t.Fatalf("SignAgentToken: %v", err)
	}
	if err := st.CreateAgentCredential(ctx, agentCred); err != nil {
		t.Fatalf("CreateAgentCredential: %v", err)
	}

	fullUAT, err := userHubClient.Tokens().Create(ctx, &hubclient.CreateTokenRequest{
		Name:      "mcp-p3-uat",
		ProjectID: proj.ID,
		Scopes:    []string{"project:read", "agent:read", "agent:message", "inbox:read", "inbox:write"},
	})
	if err != nil {
		t.Fatalf("Tokens.Create: %v", err)
	}

	backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &mcpserver.InProcessTransport{Handler: hubSrv.Handler()},
		Version:    "p3-test",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, name string) (bool, error) {
			return hubSrv.ExperimentEnabled(name), nil
		},
		DisableTransportAuth: true,
	})

	mcpSrv, err := mcpserver.New(mcpserver.Options{
		Version:    "p3-test",
		Backend:    backend,
		StreamWait: streamWait,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvDone := make(chan error, 1)
	go func() { srvDone <- mcpSrv.Serve(srvCtx, ln) }()

	stopped := false
	stopFn := func() {
		if stopped {
			return
		}
		stopped = true
		srvCancel()
		if err := <-srvDone; err != nil {
			t.Errorf("mcpSrv.Serve shutdown error: %v", err)
		}
	}
	t.Cleanup(stopFn)

	return &eventsFixture{
		hubSrv:        hubSrv,
		st:            st,
		user:          user,
		projID:        proj.ID,
		agent:         agent,
		userHubClient: userHubClient,
		agentClient:   inProcClient(hubclient.WithAgentToken(agentToken)),
		fullUAT:       fullUAT,
		mcpSrv:        mcpSrv,
		endpoint:      "http://" + ln.Addr().String() + "/mcp",
		stopMCP:       stopFn,
	}
}

func (f *eventsFixture) connectSDKClient(t *testing.T, ctx context.Context, token string) *mcp.ClientSession {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "p3-int-client", Version: "1.0"}, nil)
	if err := mcp.AddSendingCustomMethod[*mcpserver.EventsListParams, *mcpserver.EventsListResult](cl, "events/list"); err != nil {
		t.Fatalf("AddSendingCustomMethod(events/list): %v", err)
	}
	if err := mcp.AddSendingCustomMethod[*mcpserver.EventsPollParams, *mcpserver.EventsPollResult](cl, "events/poll"); err != nil {
		t.Fatalf("AddSendingCustomMethod(events/poll): %v", err)
	}
	hc := &http.Client{
		Transport: &headerRoundTripper{
			base:    &http.Transport{DisableKeepAlives: true},
			headers: map[string]string{"Authorization": "Bearer " + token},
		},
	}
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   f.endpoint,
		HTTPClient: hc,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func (f *eventsFixture) openStream(t *testing.T, ctx context.Context, endpoint, token, convID, cursor string) (*http.Response, <-chan intSSEFrame) {
	t.Helper()
	params := map[string]any{
		"name":      mcpserver.EventNameConversationMessageCreated,
		"arguments": map[string]any{"conversation_id": convID, "filter": "others"},
	}
	if cursor != "" {
		params["cursor"] = cursor
	}
	bodyBytes, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "events/stream",
		"params":  params,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Method", "events/stream")
	req.Header.Set("Mcp-Name", mcpserver.EventNameConversationMessageCreated)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST events/stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("events/stream status = %d, body = %s", resp.StatusCode, string(b))
	}
	return resp, startStreamReader(resp.Body)
}

func (f *eventsFixture) agentReply(t *testing.T, ctx context.Context, convID, text string) {
	t.Helper()
	if _, err := f.agentClient.Agents().SendOutboundMessage(ctx, f.agent.ID, &hubclient.OutboundMessageRequest{
		Recipient:      "user:" + f.user.Email,
		ConversationID: convID,
		Msg:            text,
		Type:           messages.TypeReply,
	}); err != nil {
		t.Fatalf("agent SendOutboundMessage: %v", err)
	}
}

func (f *eventsFixture) sendInitialMessage(t *testing.T, ctx context.Context, sess *mcp.ClientSession, text string) mcpserver.SendResult {
	t.Helper()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": f.projID,
			"agent":   f.agent.Slug,
			"text":    text,
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("send_message failed: err=%v res=%+v", err, res)
	}
	var out mcpserver.SendResult
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatalf("unmarshal send_message: %v", err)
	}
	return out
}

// Scenarios B2 (active -> event within 2s -> heartbeat), B3 (resume after kill),
// B4 (cross-surface cursor: poll <-> stream <-> read_messages), B5 (cross-replica),
// and B6 (truncation + malformed cursor).
func TestIntegration_Events_B2_B3_B4_B5_B6(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f := setupEventsFixture(t, 600*time.Millisecond)
	sess := f.connectSDKClient(t, ctx, f.fullUAT.Token)

	// Send initial message to create the DM conversation and get the send cursor.
	sendOut := f.sendInitialMessage(t, ctx, sess, "ping-b2")

	// --- B2: active -> event within 2s -> heartbeat ---
	b2Ctx, b2Cancel := context.WithCancel(ctx)
	resp, frames := f.openStream(t, b2Ctx, f.endpoint, f.fullUAT.Token, sendOut.ConversationID, sendOut.Cursor)

	var activeFrame intSSEFrame
	select {
	case activeFrame = <-frames:
	case <-time.After(3 * time.Second):
		t.Fatal("B2: timed out waiting for active frame")
	}
	if activeFrame.Method != "notifications/events/active" {
		t.Fatalf("B2: first frame = %q, want notifications/events/active", activeFrame.Method)
	}

	replyStart := time.Now()
	f.agentReply(t, ctx, sendOut.ConversationID, "PONG-b2")

	var evFrame intSSEFrame
	select {
	case evFrame = <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("B2: timed out waiting for event within 2s")
	}
	if evFrame.Method != "notifications/events/event" {
		t.Fatalf("B2: second frame = %q, want notifications/events/event", evFrame.Method)
	}
	if time.Since(replyStart) > 2*time.Second {
		t.Fatalf("B2: event latency %v exceeded 2s", time.Since(replyStart))
	}
	var evOcc mcpserver.EventOccurrence
	if err := json.Unmarshal(evFrame.Params, &evOcc); err != nil {
		t.Fatalf("B2: unmarshal event occurrence: %v", err)
	}
	if evOcc.Data.Text != "PONG-b2" || evOcc.Cursor == "" || evOcc.EventID == "" {
		t.Fatalf("B2: unexpected event occurrence: %+v", evOcc)
	}

	var hbFrame intSSEFrame
	select {
	case hbFrame = <-frames:
	case <-time.After(3 * time.Second):
		t.Fatal("B2: timed out waiting for heartbeat")
	}
	if hbFrame.Method != "notifications/events/heartbeat" {
		t.Fatalf("B2: third frame = %q, want notifications/events/heartbeat", hbFrame.Method)
	}
	b2Cancel()
	_ = resp.Body.Close()

	// --- B3: Resume after kill (3 messages, kill after 1, reconnect and get remaining 2) ---
	lastCursor := evOcc.Cursor
	f.agentReply(t, ctx, sendOut.ConversationID, "b3-msg-1")
	f.agentReply(t, ctx, sendOut.ConversationID, "b3-msg-2")
	f.agentReply(t, ctx, sendOut.ConversationID, "b3-msg-3")

	b3Ctx1, b3Cancel1 := context.WithCancel(ctx)
	r1, ch1 := f.openStream(t, b3Ctx1, f.endpoint, f.fullUAT.Token, sendOut.ConversationID, lastCursor)
	<-ch1 // active
	f1 := <-ch1
	if f1.Method != "notifications/events/event" {
		t.Fatalf("B3: expected event 1, got %q", f1.Method)
	}
	var occ1 mcpserver.EventOccurrence
	_ = json.Unmarshal(f1.Params, &occ1)
	if occ1.Data.Text != "b3-msg-1" {
		t.Fatalf("B3: first text = %q, want b3-msg-1", occ1.Data.Text)
	}
	// Kill client connection mid-stream.
	b3Cancel1()
	_ = r1.Body.Close()

	// Reconnect with occ1.Cursor and verify remaining 2 arrive without loss.
	b3Ctx2, b3Cancel2 := context.WithCancel(ctx)
	r2, ch2 := f.openStream(t, b3Ctx2, f.endpoint, f.fullUAT.Token, sendOut.ConversationID, occ1.Cursor)
	<-ch2 // active
	seen := make(map[string]bool)
	var gotTexts []string
	var afterB3Cursor string
	for len(gotTexts) < 2 {
		select {
		case fr := <-ch2:
			if fr.Method != "notifications/events/event" {
				continue
			}
			var o mcpserver.EventOccurrence
			_ = json.Unmarshal(fr.Params, &o)
			if !seen[o.EventID] {
				seen[o.EventID] = true
				gotTexts = append(gotTexts, o.Data.Text)
				afterB3Cursor = o.Cursor
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("B3: timed out waiting for remaining messages, got %v", gotTexts)
		}
	}
	b3Cancel2()
	_ = r2.Body.Close()
	if gotTexts[0] != "b3-msg-2" || gotTexts[1] != "b3-msg-3" {
		t.Fatalf("B3: got %v, want [b3-msg-2 b3-msg-3]", gotTexts)
	}

	// --- B4: Cross-surface cursor (poll <-> stream <-> read_messages) ---
	f.agentReply(t, ctx, sendOut.ConversationID, "b4-via-poll")
	pollRes, err := mcp.CallCustomMethod[*mcpserver.EventsPollParams, *mcpserver.EventsPollResult](ctx, sess, "events/poll", &mcpserver.EventsPollParams{
		Name:      mcpserver.EventNameConversationMessageCreated,
		Arguments: map[string]any{"conversation_id": sendOut.ConversationID, "filter": "others"},
		Cursor:    &afterB3Cursor,
	})
	if err != nil {
		t.Fatalf("B4 events/poll: %v", err)
	}
	if len(pollRes.Events) != 1 || pollRes.Events[0].Data.Text != "b4-via-poll" {
		t.Fatalf("B4 events/poll got %+v, want [b4-via-poll]", pollRes.Events)
	}
	if pollRes.Events[0].Cursor != "" {
		t.Fatalf("B4 events/poll occurrence should omit per-event cursor per SEP-3415, got %q", pollRes.Events[0].Cursor)
	}

	// Resume stream from events/poll cursor.
	f.agentReply(t, ctx, sendOut.ConversationID, "b4-via-stream")
	b4Ctx, b4Cancel := context.WithCancel(ctx)
	r4, ch4 := f.openStream(t, b4Ctx, f.endpoint, f.fullUAT.Token, sendOut.ConversationID, pollRes.Cursor)
	<-ch4 // active
	f4 := <-ch4
	var o4 mcpserver.EventOccurrence
	_ = json.Unmarshal(f4.Params, &o4)
	b4Cancel()
	_ = r4.Body.Close()
	if o4.Data.Text != "b4-via-stream" {
		t.Fatalf("B4 stream from poll cursor got %q, want b4-via-stream", o4.Data.Text)
	}

	// Resume read_messages from events/stream cursor.
	f.agentReply(t, ctx, sendOut.ConversationID, "b4-via-read")
	readRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": sendOut.ConversationID,
			"cursor":          o4.Cursor,
		},
	})
	if err != nil || readRes.IsError {
		t.Fatalf("B4 read_messages: err=%v res=%+v", err, readRes)
	}
	var readOut mcpserver.ReadMessagesOutput
	_ = json.Unmarshal([]byte(readRes.Content[0].(*mcp.TextContent).Text), &readOut)
	if len(readOut.Messages) != 1 || readOut.Messages[0].Text != "b4-via-read" {
		t.Fatalf("B4 read_messages from stream cursor got %+v, want [b4-via-read]", readOut.Messages)
	}

	// --- B5: Cross-replica resume (second MCP server against the same Hub) ---
	hubTS := httptest.NewServer(f.hubSrv.Handler())
	defer hubTS.Close()
	replica2Backend := mcpserver.NewBackend(mcpserver.BackendOptions{
		HubBaseURL: hubTS.URL,
		Transport:  hubTS.Client().Transport,
		Version:    "p3-replica2",
		ExperimentChecker: func(_ context.Context, _ mcpserver.Credential, name string) (bool, error) {
			return f.hubSrv.ExperimentEnabled(name), nil
		},
		DisableTransportAuth: true,
	})
	replica2Srv, err := mcpserver.New(mcpserver.Options{
		Version:    "p3-replica2",
		Backend:    replica2Backend,
		StreamWait: 600 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("B5 replica2 New: %v", err)
	}
	repTS := httptest.NewServer(replica2Srv.Handler())
	defer repTS.Close()

	f.agentReply(t, ctx, sendOut.ConversationID, "b5-cross-replica")
	b5Ctx, b5Cancel := context.WithCancel(ctx)
	r5, ch5 := f.openStream(t, b5Ctx, repTS.URL+"/mcp", f.fullUAT.Token, sendOut.ConversationID, readOut.NextCursor)
	<-ch5 // active
	f5 := <-ch5
	var o5 mcpserver.EventOccurrence
	_ = json.Unmarshal(f5.Params, &o5)
	b5Cancel()
	_ = r5.Body.Close()
	if o5.Data.Text != "b5-cross-replica" {
		t.Fatalf("B5 cross-replica got %q, want b5-cross-replica", o5.Data.Text)
	}

	// --- B6: Truncation (ahead-of-head cursor) and malformed cursor (-32602) ---
	aheadCursor := string(eventfeed.EncodeCursor(sendOut.ConversationID, 99999))
	pollAhead, err := mcp.CallCustomMethod[*mcpserver.EventsPollParams, *mcpserver.EventsPollResult](ctx, sess, "events/poll", &mcpserver.EventsPollParams{
		Name:      mcpserver.EventNameConversationMessageCreated,
		Arguments: map[string]any{"conversation_id": sendOut.ConversationID},
		Cursor:    &aheadCursor,
	})
	if err != nil {
		t.Fatalf("B6 poll ahead cursor: %v", err)
	}
	if !pollAhead.Truncated || pollAhead.Cursor == "" || pollAhead.Cursor == aheadCursor {
		t.Fatalf("B6 poll ahead cursor expected truncated=true and fresh head cursor, got %+v", pollAhead)
	}

	// Ahead-of-head cursor on events/stream -> active{truncated:true} directly (no error notification, per SEP-3415).
	b6Ctx, b6Cancel := context.WithCancel(ctx)
	r6, ch6 := f.openStream(t, b6Ctx, f.endpoint, f.fullUAT.Token, sendOut.ConversationID, aheadCursor)
	actTrunc := <-ch6
	if actTrunc.Method != "notifications/events/active" {
		t.Fatalf("B6 stream ahead cursor first frame = %q, want notifications/events/active (no error frame per SEP-3415)", actTrunc.Method)
	}
	var actPayload struct {
		Cursor    string `json:"cursor"`
		Truncated bool   `json:"truncated"`
	}
	_ = json.Unmarshal(actTrunc.Params, &actPayload)
	if !actPayload.Truncated || actPayload.Cursor == "" {
		t.Fatalf("B6 stream ahead cursor expected truncated=true and fresh cursor, got %+v", actPayload)
	}
	b6Cancel()
	_ = r6.Body.Close()

	// Malformed cursor -> -32602 on events/poll and events/stream.
	malformed := "not-valid-base64!!!"
	_, pollMalErr := mcp.CallCustomMethod[*mcpserver.EventsPollParams, *mcpserver.EventsPollResult](ctx, sess, "events/poll", &mcpserver.EventsPollParams{
		Name:      mcpserver.EventNameConversationMessageCreated,
		Arguments: map[string]any{"conversation_id": sendOut.ConversationID},
		Cursor:    &malformed,
	})
	var rpcErr *jsonrpc.Error
	if pollMalErr == nil || !errors.As(pollMalErr, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("B6 poll malformed cursor expected JSON-RPC -32602, got %v (rpcErr=%+v)", pollMalErr, rpcErr)
	}

	badReqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "events/stream",
		"params": map[string]any{
			"name":      mcpserver.EventNameConversationMessageCreated,
			"arguments": map[string]any{"conversation_id": sendOut.ConversationID},
			"cursor":    malformed,
		},
	})
	badReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, strings.NewReader(string(badReqBody)))
	badReq.Header.Set("Authorization", "Bearer "+f.fullUAT.Token)
	badReq.Header.Set("Content-Type", "application/json")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatalf("B6 stream malformed cursor request: %v", err)
	}
	badBody, _ := io.ReadAll(badResp.Body)
	_ = badResp.Body.Close()
	if !strings.Contains(string(badBody), "-32602") {
		t.Fatalf("B6 stream malformed cursor expected -32602, got: %s", string(badBody))
	}
}

// Scenario B7: Revocation mid-stream -> notifications/events/terminated.
// Covers both UAT revocation (unauthorized) and project membership removal (forbidden).
func TestIntegration_Events_B7_Revocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f := setupEventsFixture(t, 500*time.Millisecond)
	sess := f.connectSDKClient(t, ctx, f.fullUAT.Token)
	sendOut := f.sendInitialMessage(t, ctx, sess, "ping-b7")

	// 1. Revoke UAT mid-stream -> terminated{unauthorized} within stream_wait + 5s.
	b7aCtx, b7aCancel := context.WithCancel(ctx)
	defer b7aCancel()
	r1, ch1 := f.openStream(t, b7aCtx, f.endpoint, f.fullUAT.Token, sendOut.ConversationID, sendOut.Cursor)
	defer func() { _ = r1.Body.Close() }()
	<-ch1 // active

	if err := f.userHubClient.Tokens().Revoke(ctx, f.fullUAT.AccessToken.ID); err != nil {
		t.Fatalf("B7a revoke UAT: %v", err)
	}

	var term1 intSSEFrame
	deadline1 := time.After(5500 * time.Millisecond)
	for {
		select {
		case fr, ok := <-ch1:
			if !ok {
				t.Fatal("B7a: stream closed without notifications/events/terminated")
			}
			if fr.Method == "notifications/events/terminated" {
				term1 = fr
			}
		case <-deadline1:
			t.Fatal("B7a: timed out waiting for terminated{unauthorized}")
		}
		if term1.Method == "notifications/events/terminated" {
			break
		}
	}
	if !strings.Contains(string(term1.Params), `"unauthorized"`) {
		t.Fatalf("B7a: expected unauthorized reason, got %s", string(term1.Params))
	}

	// 2. Remove user from project mid-stream (on a group conversation) -> terminated{forbidden}.
	now := time.Now().UTC()
	groupConv := &store.Conversation{
		ID:             uuid.NewString(),
		Kind:           "group",
		Surface:        "native",
		DisplayName:    "B7 Group",
		ProjectID:      &f.projID,
		DriftState:     "active",
		LastActivityAt: now,
		CreatedAt:      now,
	}
	if err := f.st.CreateConversation(ctx, groupConv); err != nil {
		t.Fatalf("B7b create group conv: %v", err)
	}

	// Seed a second user as project member and mint a UAT for them.
	member2 := &store.User{
		ID:          uuid.NewString(),
		Email:       "b7-member2@example.com",
		DisplayName: "Member 2",
		Role:        store.UserRoleMember,
		Status:      "active",
	}
	if err := f.st.CreateUser(ctx, member2); err != nil {
		t.Fatalf("CreateUser(member2): %v", err)
	}
	projMemberRole, err := f.st.GetRoleDefinitionByName(ctx, store.ProjectRoleMember, store.RoleScopeProject)
	if err != nil {
		t.Fatalf("GetRoleDefinitionByName(project member): %v", err)
	}
	binding, err := f.st.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		PrincipalType:    "user",
		PrincipalID:      member2.ID,
		RoleDefinitionID: projMemberRole.ID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          f.projID,
		CreatedBy:        f.user.ID,
	})
	if err != nil {
		t.Fatalf("CreateRoleBinding(member2): %v", err)
	}

	m2JWT, _, err := f.hubSrv.GetUserTokenService().MintAccessToken(hub.UserTokenSubject{
		UserID: member2.ID, Email: member2.Email, DisplayName: member2.DisplayName, Role: member2.Role, ClientType: hub.ClientTypeCLI,
	})
	if err != nil {
		t.Fatalf("MintAccessToken(member2): %v", err)
	}

	b7bCtx, b7bCancel := context.WithCancel(ctx)
	defer b7bCancel()
	r2, ch2 := f.openStream(t, b7bCtx, f.endpoint, m2JWT, groupConv.ID, "")
	defer func() { _ = r2.Body.Close() }()
	<-ch2 // active

	// Delete member2's project role binding and publish membership-changed event.
	if err := f.st.DeleteRoleBinding(ctx, binding.ID); err != nil {
		t.Fatalf("DeleteRoleBinding: %v", err)
	}
	// Trigger membership change via Hub API or direct revocation: wait for next poll/recheck.
	var term2 intSSEFrame
	deadline2 := time.After(5500 * time.Millisecond)
	for {
		select {
		case fr, ok := <-ch2:
			if !ok {
				t.Fatal("B7b: stream closed without notifications/events/terminated")
			}
			if fr.Method == "notifications/events/terminated" {
				term2 = fr
			}
		case <-deadline2:
			t.Fatal("B7b: timed out waiting for terminated{forbidden}")
		}
		if term2.Method == "notifications/events/terminated" {
			break
		}
	}
	if !strings.Contains(string(term2.Params), `"forbidden"`) {
		t.Fatalf("B7b: expected forbidden reason, got %s", string(term2.Params))
	}
}

// Scenario B8: Clean shutdown drain with active stream and goroutine leak check (§10 AC7).
func TestIntegration_Events_B8_ShutdownAndNoGoroutineLeak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	f := setupEventsFixture(t, 10*time.Second)
	sess := f.connectSDKClient(t, ctx, f.fullUAT.Token)
	sendOut := f.sendInitialMessage(t, ctx, sess, "ping-b8")

	runtime.GC()
	time.Sleep(30 * time.Millisecond)
	before := runtime.NumGoroutine()

	b8Ctx, b8Cancel := context.WithCancel(ctx)
	defer b8Cancel()
	resp, ch := f.openStream(t, b8Ctx, f.endpoint, f.fullUAT.Token, sendOut.ConversationID, sendOut.Cursor)
	defer func() { _ = resp.Body.Close() }()

	// Wait for active frame so stream is parked inside Hub Watch.
	act := <-ch
	if act.Method != "notifications/events/active" {
		t.Fatalf("B8: expected active, got %q", act.Method)
	}

	// Begin shutdown on both Hub and MCP server (as shutdownPhaseMark does).
	f.hubSrv.BeginShutdown()
	f.mcpSrv.BeginShutdown()

	// Stream should close cleanly within budget (and emit final StreamEventsResult).
	var sawFinalResult bool
	drainDeadline := time.After(5 * time.Second)
	for {
		select {
		case fr, ok := <-ch:
			if !ok {
				goto drained
			}
			if len(fr.Result) > 0 {
				sawFinalResult = true
			}
		case <-drainDeadline:
			t.Fatal("B8: events/stream did not drain on BeginShutdown")
		}
	}
drained:
	if !sawFinalResult {
		t.Errorf("B8: expected final StreamEventsResult frame on server-initiated shutdown")
	}

	// New requests immediately get 503 Service Unavailable.
	postReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"events/list","params":{}}`))
	postReq.Header.Set("Authorization", "Bearer "+f.fullUAT.Token)
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("B8 post after shutdown: %v", err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("B8 post after shutdown status = %d, want 503", postResp.StatusCode)
	}

	// Stop MCP server and verify no goroutine leak.
	f.stopMCP()
	_ = resp.Body.Close()
	b8Cancel()

	var after int
	for i := 0; i < 30; i++ {
		runtime.GC()
		time.Sleep(25 * time.Millisecond)
		after = runtime.NumGoroutine()
		if after <= before+4 {
			break
		}
	}
	if after > before+4 {
		t.Fatalf("B8 goroutine leak: before=%d after=%d", before, after)
	}
}
