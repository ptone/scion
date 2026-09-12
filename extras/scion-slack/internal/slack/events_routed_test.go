package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/projectcompat"
)

// routedTestFixture sets up a real SQLite store seeded with a channel link and
// user mapping, two httptest.Server instances acting as the hub's legacy and
// routed inbound endpoints, and a SlackBroker wired to those endpoints. Each
// test can flip routedInboundEnabled and call deliverUserMessage directly on
// the eventServer to observe which endpoint receives the request and what
// payload shape is sent.
type routedTestFixture struct {
	store  Store
	broker *SlackBroker

	// Captured payloads from the two hub endpoints.
	mu             sync.Mutex
	legacyCalls    []inboundPayload
	routedCalls    []routedInboundPayload
	legacyRawBodies []json.RawMessage
	routedRawBodies []json.RawMessage

	// Servers.
	hubServer *httptest.Server

	// Response overrides (default: 200 OK).
	routedStatus int
	routedBody   string
}

func newRoutedTestFixture(t *testing.T) *routedTestFixture {
	t.Helper()
	f := &routedTestFixture{
		store:        newTestStore(t),
		routedStatus: http.StatusOK,
	}

	// Seed channel link.
	require.NoError(t, f.store.CreateChannelLink(context.Background(), &ChannelLink{
		ChannelID:    "C-TEST",
		TeamID:       "T-TEAM",
		ProjectID:    "proj-001",
		DefaultAgent: "alpha",
		LinkedBy:     "test",
		LinkedAt:     time.Now(),
		Active:       true,
	}))

	// Seed user mapping.
	require.NoError(t, f.store.CreateUserMapping(context.Background(), &SlackUserMapping{
		SlackUserID:   "U-SENDER",
		SlackUsername: "testuser",
		ScionEmail:    "testuser@example.com",
		LinkedAt:      time.Now(),
	}))

	// Hub server that handles both legacy and routed endpoints.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/broker/inbound", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p inboundPayload
		json.Unmarshal(body, &p)
		f.mu.Lock()
		f.legacyCalls = append(f.legacyCalls, p)
		f.legacyRawBodies = append(f.legacyRawBodies, json.RawMessage(body))
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /api/v1/broker/inbound/routed", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p routedInboundPayload
		json.Unmarshal(body, &p)
		f.mu.Lock()
		f.routedCalls = append(f.routedCalls, p)
		f.routedRawBodies = append(f.routedRawBodies, json.RawMessage(body))
		status := f.routedStatus
		respBody := f.routedBody
		f.mu.Unlock()
		w.WriteHeader(status)
		if respBody != "" {
			w.Write([]byte(respBody))
		} else {
			w.Write([]byte(`{"delivered":true}`))
		}
	})
	f.hubServer = httptest.NewServer(mux)
	t.Cleanup(f.hubServer.Close)

	// Build broker using the real Configure path so we exercise config wiring.
	f.broker = NewBroker(slog.Default())
	// Phase 1: bot_token + routed_inbound_enabled. We set socket_mode=false with
	// a signing_secret so Configure doesn't complain, and provide a temp db path.
	require.NoError(t, f.broker.Configure(map[string]string{
		"bot_token":      "xoxb-fake-token",
		"signing_secret": "fake-signing-secret",
		"db_path":        t.TempDir() + "/broker.db",
	}))

	// Inject the test store in place of the auto-created one.
	f.broker.mu.Lock()
	f.broker.store = f.store
	f.broker.mu.Unlock()

	// Phase 2: hub_url triggers event server creation with delivery functions.
	require.NoError(t, f.broker.Configure(map[string]string{
		"hub_url":   f.hubServer.URL,
		"hmac_key":  "",
		"broker_id": "",
	}))

	return f
}

// events returns the eventServer from the broker. Must be called after Configure.
func (f *routedTestFixture) events() *eventServer {
	f.broker.mu.RLock()
	defer f.broker.mu.RUnlock()
	return f.broker.events
}

// enableRouted reconfigures the eventServer for routed inbound delivery.
func (f *routedTestFixture) enableRouted() {
	es := f.events()
	es.routedInboundEnabled = true
}

// disableRouted reconfigures the eventServer for legacy inbound delivery.
func (f *routedTestFixture) disableRouted() {
	es := f.events()
	es.routedInboundEnabled = false
}

// -------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------

func TestDeliverUserMessage_RoutedEnabled_CorrectPayload(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	f.events().deliverUserMessage("C-TEST", "1726099200.000100", "U-SENDER", "hello @beta what's up")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1, "exactly one routed request expected")
	require.Len(t, f.legacyCalls, 0, "no legacy request expected when routed is enabled")

	p := f.routedCalls[0]
	assert.Equal(t, "proj-001", p.ProjectID)
	assert.Equal(t, "alpha", p.DefaultAgent)
	assert.NotNil(t, p.Message)
	assert.Equal(t, "hello @beta what's up", p.Message.Msg, "body must be raw — mentions not stripped by adapter")
	assert.Equal(t, "user:testuser@example.com", p.Message.Sender)
	assert.Equal(t, "U-SENDER", p.Message.SenderID)
	assert.Equal(t, "1726099200.000100", p.Message.ThreadID)
	assert.Equal(t, "slack", p.Message.Channel)
	assert.Equal(t, messages.TypeInstruction, p.Message.Type)
	assert.Equal(t, "C-TEST", p.Message.Metadata["slack_channel_id"])
	assert.Equal(t, "1726099200.000100", p.Message.Metadata["slack_thread_ts"])

	// Routed messages must NOT include project_id in metadata (it's a top-level field).
	_, hasProjectID := p.Message.Metadata["project_id"]
	assert.False(t, hasProjectID, "project_id must not appear in routed message metadata")

	// Routed messages must NOT set Recipient (hub resolves routing).
	assert.Empty(t, p.Message.Recipient, "recipient must not be set by adapter on routed path")
}

func TestDeliverUserMessage_RoutedDisabled_LegacyPayload(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.disableRouted()

	f.events().deliverUserMessage("C-TEST", "1726099200.000200", "U-SENDER", "hello legacy")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.legacyCalls, 1, "exactly one legacy request expected")
	require.Len(t, f.routedCalls, 0, "no routed request expected when routed is disabled")

	p := f.legacyCalls[0]
	expectedTopic := projectcompat.AgentTopic("proj-001", "alpha")
	assert.Equal(t, expectedTopic, p.Topic)
	assert.NotNil(t, p.Message)
	assert.Equal(t, "hello legacy", p.Message.Msg)
	assert.Equal(t, "agent:alpha", p.Message.Recipient)
	assert.Equal(t, "proj-001", p.Message.Metadata["project_id"])
}

func TestDeliverUserMessage_RoutedEnabled_MentionsRemainRaw(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Leading mention pattern: "@gamma do something" — adapter must NOT strip or
	// resolve mentions; the hub planner does that.
	f.events().deliverUserMessage("C-TEST", "1726099200.000300", "U-SENDER", "@gamma do something")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1)
	assert.Equal(t, "@gamma do something", f.routedCalls[0].Message.Msg,
		"leading mention must remain raw for hub planner")
}

func TestDeliverUserMessage_RoutedEnabled_MultipleMentionsRaw(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Multiple @-mentions: adapter must forward verbatim.
	text := "hey @alpha and @beta please review this @gamma"
	f.events().deliverUserMessage("C-TEST", "1726099200.000350", "U-SENDER", text)

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1)
	assert.Equal(t, text, f.routedCalls[0].Message.Msg,
		"all mentions must be forwarded raw to hub")
}

func TestDeliverUserMessage_RoutedError_NoLegacyFallback(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Make the routed endpoint return an error.
	f.mu.Lock()
	f.routedStatus = http.StatusInternalServerError
	f.routedBody = `{"error":{"code":"internal","message":"boom"}}`
	f.mu.Unlock()

	f.events().deliverUserMessage("C-TEST", "1726099200.000400", "U-SENDER", "test error path")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1, "routed endpoint must be called")
	require.Len(t, f.legacyCalls, 0,
		"must NOT fall back to legacy on routed error — routed path returns early")
}

func TestDeliverUserMessage_RoutedPartialError_NoLegacyFallback(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// 409 Conflict (e.g. agent stopped) — must NOT retry via legacy.
	f.mu.Lock()
	f.routedStatus = http.StatusConflict
	f.routedBody = `{"error":{"code":"agent_not_running","message":"primary agent is stopped"}}`
	f.mu.Unlock()

	f.events().deliverUserMessage("C-TEST", "1726099200.000450", "U-SENDER", "test partial error")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1)
	require.Len(t, f.legacyCalls, 0,
		"must NOT fall back to legacy on partial error")
}

func TestDeliverUserMessage_RoutedEnabled_NoDefaultAgent_StillDelivers(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Create a channel link with no default agent — routed path can derive from
	// mentions, so it should still send the request.
	require.NoError(t, f.store.CreateChannelLink(context.Background(), &ChannelLink{
		ChannelID:    "C-NODEFAULT",
		TeamID:       "T-TEAM",
		ProjectID:    "proj-002",
		DefaultAgent: "", // no default
		LinkedBy:     "test",
		LinkedAt:     time.Now(),
		Active:       true,
	}))

	f.events().deliverUserMessage("C-NODEFAULT", "1726099200.000500", "U-SENDER", "@gamma help me")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1, "routed path must proceed even without default agent")
	assert.Equal(t, "proj-002", f.routedCalls[0].ProjectID)
	assert.Empty(t, f.routedCalls[0].DefaultAgent)
	assert.Equal(t, "@gamma help me", f.routedCalls[0].Message.Msg)
}

func TestDeliverUserMessage_LegacyPath_NoDefaultAgent_Drops(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.disableRouted()

	// Create a channel link with no default agent — legacy path requires one.
	require.NoError(t, f.store.CreateChannelLink(context.Background(), &ChannelLink{
		ChannelID:    "C-NODEFAULT2",
		TeamID:       "T-TEAM",
		ProjectID:    "proj-003",
		DefaultAgent: "",
		LinkedBy:     "test",
		LinkedAt:     time.Now(),
		Active:       true,
	}))

	f.events().deliverUserMessage("C-NODEFAULT2", "1726099200.000600", "U-SENDER", "hello")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.legacyCalls, 0, "legacy path must drop message when no default agent")
	require.Len(t, f.routedCalls, 0)
}

func TestDeliverUserMessage_RoutedEnabled_ConversationContextSaved(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	f.events().deliverUserMessage("C-TEST", "1726099200.000700", "U-SENDER", "context test")

	// Verify conversation context was persisted.
	ctx := context.Background()
	cc, err := f.store.GetConversationContext(ctx, "U-SENDER", "proj-001", "alpha")
	require.NoError(t, err)
	require.NotNil(t, cc)
	assert.Equal(t, "C-TEST", cc.LastChannelID)
	assert.Equal(t, "1726099200.000700", cc.LastThreadTS)
}

func TestDeliverUserMessage_UnknownChannel_NoCalls(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Channel not linked — should silently return.
	f.events().deliverUserMessage("C-UNKNOWN", "1726099200.000800", "U-SENDER", "hello")

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Len(t, f.routedCalls, 0)
	assert.Len(t, f.legacyCalls, 0)
}

func TestDeliverUserMessage_UnregisteredUser_NoCalls(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// User not mapped — should silently return (and try to post ephemeral, which
	// will fail silently since we have a fake Slack client).
	f.events().deliverUserMessage("C-TEST", "1726099200.000900", "U-UNKNOWN", "hello")

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Len(t, f.routedCalls, 0)
	assert.Len(t, f.legacyCalls, 0)
}

func TestDeliverUserMessage_InactiveChannel_NoCalls(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	require.NoError(t, f.store.CreateChannelLink(context.Background(), &ChannelLink{
		ChannelID:    "C-INACTIVE",
		TeamID:       "T-TEAM",
		ProjectID:    "proj-004",
		DefaultAgent: "alpha",
		LinkedBy:     "test",
		LinkedAt:     time.Now(),
		Active:       false,
	}))

	f.events().deliverUserMessage("C-INACTIVE", "1726099200.001000", "U-SENDER", "hello")

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Len(t, f.routedCalls, 0)
	assert.Len(t, f.legacyCalls, 0)
}

// TestConfigureRoutedInboundEnabled verifies the config path: the
// routed_inbound_enabled setting is correctly plumbed from Configure into the
// eventServer.
func TestConfigureRoutedInboundEnabled(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{"true", "true", true},
		{"one", "1", true},
		{"false", "false", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBroker(slog.Default())
			cfg := map[string]string{
				"bot_token":              "xoxb-test",
				"signing_secret":         "secret",
				"db_path":               t.TempDir() + "/test.db",
				"routed_inbound_enabled": tt.value,
			}
			require.NoError(t, b.Configure(cfg))

			// Phase 2 — triggers eventServer creation.
			require.NoError(t, b.Configure(map[string]string{
				"hub_url": "http://localhost:9999",
			}))

			b.mu.RLock()
			es := b.events
			b.mu.RUnlock()
			require.NotNil(t, es, "eventServer must be created after phase 2")
			assert.Equal(t, tt.expected, es.routedInboundEnabled)

			b.Close()
		})
	}
}

// TestDeliverUserMessage_RoutedEnabled_HTTPPayloadShape validates the raw JSON
// body shape sent to the routed endpoint — ensuring the adapter does not inject
// fields that belong to the legacy path (e.g. "topic") or leak metadata that
// the hub should set (e.g. "recipient").
func TestDeliverUserMessage_RoutedEnabled_HTTPPayloadShape(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	f.events().deliverUserMessage("C-TEST", "1726099200.001100", "U-SENDER", "payload shape test")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedRawBodies, 1)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(f.routedRawBodies[0], &raw))

	// Must have these top-level fields.
	assert.Contains(t, raw, "project_id")
	assert.Contains(t, raw, "default_agent")
	assert.Contains(t, raw, "message")

	// Must NOT have legacy "topic" field.
	assert.NotContains(t, raw, "topic", "routed payload must not contain legacy 'topic' field")

	// Inspect inner message.
	var msg map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["message"], &msg))

	// Message must not have a "recipient" set.
	if recipientRaw, ok := msg["recipient"]; ok {
		var recipient string
		json.Unmarshal(recipientRaw, &recipient)
		assert.Empty(t, recipient, "recipient must be empty on routed path")
	}
}

// TestDeliverUserMessage_LegacyPath_HTTPPayloadShape validates that the legacy
// path sends the expected "topic" + "message" shape.
func TestDeliverUserMessage_LegacyPath_HTTPPayloadShape(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.disableRouted()

	f.events().deliverUserMessage("C-TEST", "1726099200.001200", "U-SENDER", "legacy shape test")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.legacyRawBodies, 1)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(f.legacyRawBodies[0], &raw))

	// Must have legacy fields.
	assert.Contains(t, raw, "topic")
	assert.Contains(t, raw, "message")

	// Must NOT have routed fields.
	assert.NotContains(t, raw, "project_id",
		"legacy payload must not contain 'project_id' top-level field")
	assert.NotContains(t, raw, "default_agent",
		"legacy payload must not contain 'default_agent' top-level field")
}

// TestDeliverUserMessage_SenderFallback verifies that when the user mapping has
// no scion_email, the sender field falls back to "slack:<username>".
//
// NOTE: This sender format is NOT accepted by the hub's routed endpoint, which
// requires "user:<email>" and rejects non-"user:" prefixed senders with 400.
// The adapter sends it anyway and the hub returns an error (tested by
// TestDeliverUserMessage_RoutedSenderRejected_NoLegacyFallback below). The
// email-empty case is expected to be rare (registration normally provides an
// email), but if it occurs the message is silently lost on the routed path.
func TestDeliverUserMessage_SenderFallback(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Create a user mapping without email.
	require.NoError(t, f.store.CreateUserMapping(context.Background(), &SlackUserMapping{
		SlackUserID:   "U-NOEMAIL",
		SlackUsername: "slackonly",
		ScionEmail:    "",
		LinkedAt:      time.Now(),
	}))

	f.events().deliverUserMessage("C-TEST", "1726099200.001300", "U-NOEMAIL", "no email")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1)
	assert.Equal(t, "slack:slackonly", f.routedCalls[0].Message.Sender)
}

// TestDeliverUserMessage_RoutedSenderRejected_NoLegacyFallback proves that when
// the hub rejects a "slack:<username>" sender (400 validation_error), the
// adapter does NOT fall back to legacy delivery. The "slack:" prefix is a
// legacy-era identity format; the routed endpoint requires "user:<email>".
func TestDeliverUserMessage_RoutedSenderRejected_NoLegacyFallback(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Hub rejects "slack:" sender with 400.
	f.mu.Lock()
	f.routedStatus = http.StatusBadRequest
	f.routedBody = `{"error":{"code":"validation_error","message":"sender must use user: prefix for mapped identity"}}`
	f.mu.Unlock()

	require.NoError(t, f.store.CreateUserMapping(context.Background(), &SlackUserMapping{
		SlackUserID:   "U-NOEMAIL2",
		SlackUsername: "slackonly2",
		ScionEmail:    "",
		LinkedAt:      time.Now(),
	}))

	f.events().deliverUserMessage("C-TEST", "1726099200.001400", "U-NOEMAIL2", "rejected sender")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1, "routed endpoint must be called exactly once")
	require.Len(t, f.legacyCalls, 0,
		"must NOT fall back to legacy when hub rejects slack: sender on routed path")
}

// TestDeliverUserMessage_RoutedMixedResults_NoRetry verifies that when the hub
// returns 200 with delivered=true and a response body containing a delivered
// primary and an unauthorized secondary, the adapter treats it as success
// (single request, no retry, no legacy fallback).
func TestDeliverUserMessage_RoutedMixedResults_NoRetry(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Hub returns 200 with mixed results: primary delivered, secondary unauthorized.
	f.mu.Lock()
	f.routedStatus = http.StatusOK
	f.routedBody = `{
		"delivered": true,
		"primary_agent": "alpha",
		"results": [
			{"agent_slug": "alpha", "type": "message", "status": "delivered", "message_id": "msg-001"},
			{"agent_slug": "beta", "type": "mention", "status": "unauthorized", "error": "not a project member"}
		],
		"unresolved_mentions": []
	}`
	f.mu.Unlock()

	f.events().deliverUserMessage("C-TEST", "1726099200.001500", "U-SENDER", "hello @beta review this")

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.routedCalls, 1, "exactly one routed request — no retry on mixed results")
	require.Len(t, f.legacyCalls, 0, "no legacy fallback on mixed results")

	// Verify the adapter sent the raw message including the mention.
	assert.Equal(t, "hello @beta review this", f.routedCalls[0].Message.Msg)
	assert.Equal(t, "alpha", f.routedCalls[0].DefaultAgent)
}

// TestDeliverUserMessage_RoutedTransportFailure_NoLegacyFallback proves that
// when the routed hub endpoint is unreachable (transport error), the adapter
// makes exactly one attempt and does NOT fall back to legacy delivery.
// Uses a closed server for instant connection-refused — no 6-minute wait.
func TestDeliverUserMessage_RoutedTransportFailure_NoLegacyFallback(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Close the hub server to force transport error (connection refused).
	f.hubServer.Close()

	f.events().deliverUserMessage("C-TEST", "1726099200.001600", "U-SENDER", "transport fail")

	f.mu.Lock()
	defer f.mu.Unlock()

	// The routed call never reaches the handler (server is closed), so no
	// routedCalls are captured. The key assertion: no legacy fallback either.
	assert.Len(t, f.routedCalls, 0, "routed handler never reached (server closed)")
	assert.Len(t, f.legacyCalls, 0,
		"must NOT fall back to legacy on transport failure")
}

// TestDeliverUserMessage_RoutedTimeout_NoLegacyFallback proves that when the
// routed hub endpoint hangs and a bounded transport times out, the adapter makes
// one attempt and does NOT fall back to legacy. Uses a custom deliverRoutedInbound
// with a short-timeout transport to avoid the production 6-minute wait.
func TestDeliverUserMessage_RoutedTimeout_NoLegacyFallback(t *testing.T) {
	f := newRoutedTestFixture(t)
	f.enableRouted()

	// Track whether the custom delivery function was called.
	var called int
	var calledMu sync.Mutex

	// Create a hub that hangs on routed requests. The handler blocks until the
	// client gives up (200ms) or until cleanupDone fires during test cleanup.
	cleanupDone := make(chan struct{})
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-cleanupDone:
		}
	}))
	t.Cleanup(func() {
		close(cleanupDone)
		slowServer.Close()
	})

	// Override the eventServer's deliverRoutedInbound with a version that uses
	// a short timeout — consistent with the production code path but bounded for
	// testing.
	es := f.events()
	es.deliverRoutedInbound = func(projectID, defaultAgent string, msg *messages.StructuredMessage) *hubError {
		calledMu.Lock()
		called++
		calledMu.Unlock()

		payload := routedInboundPayload{
			ProjectID:    projectID,
			DefaultAgent: defaultAgent,
			Message:      msg,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil
		}

		// Short timeout: 200ms instead of 6 minutes.
		client := &http.Client{Timeout: 200 * time.Millisecond}
		req, _ := http.NewRequest("POST", slowServer.URL+"/api/v1/broker/inbound/routed",
			bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			// Transport/timeout error — same handling as production code.
			return nil
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return parseHubError(resp)
		}
		io.Copy(io.Discard, resp.Body)
		return nil
	}

	f.events().deliverUserMessage("C-TEST", "1726099200.001700", "U-SENDER", "timeout test")

	calledMu.Lock()
	defer calledMu.Unlock()

	assert.Equal(t, 1, called, "routed delivery must be attempted exactly once")

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Len(t, f.legacyCalls, 0,
		"must NOT fall back to legacy on timeout")
}
