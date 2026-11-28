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

// Evidence tests (no fix) for the H2 two-agent DM residual: does a
// conversation-addressed agent send into an existing two-agent DM
// conversation, with no recipient id, reach the H2 gate's uncovered agent_dm
// branch (deriveH2FactSnapshot -> uncoveredPath=agent_dm -> admitted with zero
// evaluation and zero copy planning)?
//
// Every case goes through the real HTTP entry point
// (handleAgentOutboundMessage) with hub.message_policies ON. Each case
// observes, separately: (a) HTTP status, (b) persistence in the DM
// conversation, (c) peer dispatch, (d) policy loads and decision rows, (e)
// mention copy planning/delivery to a third agent, and (f) the uncovered
// metric, counter and log.
//
// The tests pin CURRENT behaviour. The internal pin
// TestMessagePolicyGate_TwoAgentDirectConvUncovered_HumanDMStillRejected
// (which calls evaluateH2MessagePolicies directly with a constructed routing
// result) is intentionally left unchanged.

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/GoogleCloudPlatform/scion/pkg/messagepolicy"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

const (
	h2ResidualDMKeyword   = "ZZDM"
	h2ResidualCopyKeyword = "ZZCOPY"
)

// h2ResidualEnv is one isolated fixture per case: sender A (f.sender), peer B
// (f.peerAgent), mentionable third agent C, an existing A<->B DM conversation
// created through the production outbound path, and the two enforce policies.
type h2ResidualEnv struct {
	f       *mpgFixture
	c       *store.Agent
	dmConv  *store.Conversation
	reader  *sdkmetric.ManualReader
	logBuf  *bytes.Buffer
	logMu   *sync.Mutex
	listAt0 int64
	decAt0  int64
	rowsAt0 int
}

func newH2ResidualEnv(t *testing.T) *h2ResidualEnv {
	t.Helper()
	ctx := context.Background()
	f := newMpgFixture(t, true)
	c := sliceBMakeAgent(t, f, "c")

	logBuf := &bytes.Buffer{}
	logMu := &sync.Mutex{}
	f.srv.messageLog = slog.New(slog.NewJSONHandler(&mpgLockedWriter{mu: logMu, w: logBuf}, &slog.HandlerOptions{Level: slog.LevelInfo}))

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	otelMetrics, err := NewOTelMessagePolicyMetrics(mp)
	require.NoError(t, err)
	f.srv.SetMessagePolicyMetrics(otelMetrics)

	// Create the A<->B DM conversation the way production does: the first
	// agent send addressed by conversation_ref "@<peer-slug>" (before any
	// policy exists, so it is admitted and creates the conversation).
	seed := f.sendOutbound(t, OutboundMessageRequest{
		ConversationRef: "@" + f.peerAgent.Slug,
		Msg:             "seed hello",
		Type:            messages.TypeInstruction,
	})
	require.Equal(t, http.StatusOK, seed.Code, "seed send must create the DM: %s", seed.Body.String())
	dmKey, err := messages.DMConversationKey("agent", f.sender.ID, "agent", f.peerAgent.ID)
	require.NoError(t, err)
	dmConv, err := f.s.GetConversationByExternalRef(ctx, "native", dmKey)
	require.NoError(t, err, "seed send must have created the two-agent DM conversation")
	require.Equal(t, "direct", dmConv.Kind)

	require.NoError(t, f.s.CreateMessagePolicy(ctx, &store.MessagePolicy{
		ProjectID:     f.project.ID,
		Name:          "h2res-reject-agent-dm",
		Enabled:       true,
		Mode:          store.MessagePolicyModeEnforce,
		Scope:         store.MessagePolicyScope{Surfaces: []string{store.MessagePolicySurfaceAgentDM}},
		ViolationRule: `message.body.contains("` + h2ResidualDMKeyword + `")`,
		OnFailed:      store.MessagePolicyOutcomeReject,
	}))
	require.NoError(t, f.s.CreateMessagePolicy(ctx, &store.MessagePolicy{
		ProjectID:     f.project.ID,
		Name:          "h2res-reject-mention-copy",
		Enabled:       true,
		Mode:          store.MessagePolicyModeEnforce,
		Scope:         store.MessagePolicyScope{Surfaces: []string{store.MessagePolicySurfaceMentionDelivery}},
		ViolationRule: `message.body.contains("` + h2ResidualCopyKeyword + `")`,
		OnFailed:      store.MessagePolicyOutcomeReject,
	}))

	env := &h2ResidualEnv{f: f, c: c, dmConv: dmConv, reader: reader, logBuf: logBuf, logMu: logMu}
	sliceBResetDispatcher(f.dispatcher)
	env.listAt0 = f.faultWrapper.listCalls.Load()
	env.decAt0 = f.faultWrapper.decisionCalls.Load()
	env.rowsAt0 = len(env.dmRows(t))
	require.Equal(t, 1, env.rowsAt0, "only the seed row exists in the DM before the case")
	require.Zero(t, f.srv.messagePolicyUncovered.Load())
	return env
}

func (e *h2ResidualEnv) dmRows(t *testing.T) []store.Message {
	t.Helper()
	res, err := e.f.s.ListMessages(context.Background(), store.MessageFilter{ConversationID: e.dmConv.ID}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	return res.Items
}

func (e *h2ResidualEnv) dispatchesTo(agentID string) int {
	n := 0
	for _, c := range e.f.dispatcher.getCalls() {
		if c.Agent != nil && c.Agent.ID == agentID {
			n++
		}
	}
	return n
}

func (e *h2ResidualEnv) decisionsBySurface(t *testing.T) map[string][]store.MessagePolicyDecision {
	t.Helper()
	res, err := e.f.s.ListMessagePolicyDecisions(context.Background(), store.MessagePolicyDecisionFilter{ProjectID: e.f.project.ID}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	out := map[string][]store.MessagePolicyDecision{}
	for _, d := range res.Items {
		out[d.Surface] = append(out[d.Surface], d)
	}
	return out
}

func (e *h2ResidualEnv) collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, e.reader.Collect(context.Background(), &rm))
	return rm
}

func (e *h2ResidualEnv) logs() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return e.logBuf.String()
}

// copyToC reports whether any mention copy reached C: a persisted message
// addressed to C, a dispatch to C, or a created A<->C DM conversation.
func (e *h2ResidualEnv) copyToC(t *testing.T) (rows int, dispatches int, convCreated bool) {
	t.Helper()
	ctx := context.Background()
	res, err := e.f.s.ListMessages(ctx, store.MessageFilter{RecipientID: e.c.ID}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	key, err := messages.DMConversationKey("agent", e.f.sender.ID, "agent", e.c.ID)
	require.NoError(t, err)
	_, convErr := e.f.s.GetConversationByExternalRef(ctx, "native", key)
	if convErr != nil {
		require.ErrorIs(t, convErr, store.ErrNotFound)
	}
	return len(res.Items), e.dispatchesTo(e.c.ID), convErr == nil
}

// logObservations records the observed (a)-(f) values in verbose test output
// so the evidence report can quote exact counts.
func (e *h2ResidualEnv) logObservations(t *testing.T, status int, body string) {
	t.Helper()
	rows, disp, conv := e.copyToC(t)
	surf := map[string][]string{}
	for s, ds := range e.decisionsBySurface(t) {
		for _, d := range ds {
			surf[s] = append(surf[s], d.PolicyName+"/"+d.Action)
		}
	}
	rm := e.collect(t)
	t.Logf("OBS status=%d body=%s", status, strings.TrimSpace(body))
	t.Logf("OBS dm_rows_added=%d peer_dispatches=%d total_dispatches=%d", len(e.dmRows(t))-e.rowsAt0, e.dispatchesTo(e.f.peerAgent.ID), len(e.f.dispatcher.getCalls()))
	t.Logf("OBS policy_loads=%d decision_row_writes=%d evaluations_metric=%d decisions=%v",
		e.f.faultWrapper.listCalls.Load()-e.listAt0, e.f.faultWrapper.decisionCalls.Load()-e.decAt0,
		mpgSumInt64Metric(t, rm, "scion.message_policy.evaluations"), surf)
	t.Logf("OBS copy_to_c rows=%d dispatches=%d conv_created=%t", rows, disp, conv)
	t.Logf("OBS uncovered_counter=%d uncovered_metric=%d uncovered_log=%t", e.f.srv.messagePolicyUncovered.Load(),
		mpgSumInt64Metric(t, rm, "scion.message_policy.uncovered"), strings.Contains(e.logs(), "admitting send on uncovered path"))
}

// assertNotUncovered checks (f): the H2 uncovered agent_dm branch never ran.
func (e *h2ResidualEnv) assertNotUncovered(t *testing.T) {
	t.Helper()
	assert.Zero(t, e.f.srv.messagePolicyUncovered.Load(), "H2 uncovered counter must stay 0")
	assert.Zero(t, mpgSumInt64Metric(t, e.collect(t), "scion.message_policy.uncovered"), "uncovered metric must stay 0")
	assert.NotContains(t, e.logs(), "admitting send on uncovered path", "uncovered log must not be emitted")
}

// TestH2Residual_ConvAddressedTwoAgentDM_NoRecipientID_AgentDMKeywordRejectedPreEffect
// is case 1: A posts conversation_ref "conv:<A<->B DM>" with NO recipient id
// and body "hi ZZDM". Routing stage S5 derives the peer agent from the DM key
// (targetAgent, deliveryAgentDM), so the handler delegates to ExecuteAgentDM
// and the agent_dm policy is evaluated and rejects pre-effect. The H2
// uncovered branch is not reached.
func TestH2Residual_ConvAddressedTwoAgentDM_NoRecipientID_AgentDMKeywordRejectedPreEffect(t *testing.T) {
	e := newH2ResidualEnv(t)

	rr := e.f.sendOutbound(t, OutboundMessageRequest{
		ConversationRef: "conv:" + e.dmConv.ID,
		Msg:             "hi " + h2ResidualDMKeyword,
		Type:            messages.TypeInstruction,
	})
	e.logObservations(t, rr.Code, rr.Body.String())

	// (a) status
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	// (b) persistence
	assert.Len(t, e.dmRows(t), e.rowsAt0, "rejected send must persist no DM row")
	// (c) peer dispatch
	assert.Zero(t, e.dispatchesTo(e.f.peerAgent.ID), "rejected send must not dispatch to the peer")
	// (d) evaluation
	assert.Equal(t, int64(1), e.f.faultWrapper.listCalls.Load()-e.listAt0, "policies loaded once")
	assert.Equal(t, int64(1), mpgSumInt64Metric(t, e.collect(t), "scion.message_policy.evaluations"), "one agent_dm evaluation")
	assert.Equal(t, int64(1), e.f.faultWrapper.decisionCalls.Load()-e.decAt0, "one agent_dm reject row")
	decs := e.decisionsBySurface(t)
	require.NotEmpty(t, decs[messagepolicy.SurfaceAgentDM], "an agent_dm decision row must be written")
	for _, d := range decs[messagepolicy.SurfaceAgentDM] {
		assert.Equal(t, "h2res-reject-agent-dm", d.PolicyName)
		assert.Equal(t, store.MessagePolicyActionRejected, d.Action)
		assert.Equal(t, e.f.peerAgent.ID, d.RecipientID)
	}
	assert.Empty(t, decs[messagepolicy.SurfaceMentionDelivery], "no mention in body: no mention_delivery decision")
	assert.Empty(t, decs[messagepolicy.SurfaceAgentToUser], "must not be misclassified as agent_to_user")
	// (e) copy planning: no mention, so no copy to C
	rows, disp, conv := e.copyToC(t)
	assert.Zero(t, rows)
	assert.Zero(t, disp)
	assert.False(t, conv)
	// (f) uncovered
	e.assertNotUncovered(t)
}

// TestH2Residual_ConvAddressedTwoAgentDM_NoRecipientID_MentionCopyRejectedWholeSend
// is case 2: same addressing, body "hi @c ZZCOPY". The agent_dm policy
// admits the primary, the mention_delivery policy rejects the copy to C, and
// Slice B whole-send admission rejects the entire send pre-effect: no DM row,
// no peer dispatch, no copy conversation, row or dispatch for C.
func TestH2Residual_ConvAddressedTwoAgentDM_NoRecipientID_MentionCopyRejectedWholeSend(t *testing.T) {
	e := newH2ResidualEnv(t)

	rr := e.f.sendOutbound(t, OutboundMessageRequest{
		ConversationRef: "conv:" + e.dmConv.ID,
		Msg:             "hi @" + e.c.Slug + " " + h2ResidualCopyKeyword,
		Type:            messages.TypeInstruction,
	})
	e.logObservations(t, rr.Code, rr.Body.String())

	// (a)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	// (b)
	assert.Len(t, e.dmRows(t), e.rowsAt0, "whole-send reject must persist no DM row")
	// (c)
	assert.Zero(t, e.dispatchesTo(e.f.peerAgent.ID), "whole-send reject must not dispatch to the peer")
	// (d)
	assert.Equal(t, int64(1), e.f.faultWrapper.listCalls.Load()-e.listAt0, "policies loaded once")
	assert.Equal(t, int64(2), mpgSumInt64Metric(t, e.collect(t), "scion.message_policy.evaluations"), "agent_dm primary plus one mention_delivery copy evaluation")
	assert.Equal(t, int64(2), e.f.faultWrapper.decisionCalls.Load()-e.decAt0, "agent_dm admitted row plus mention_delivery reject row")
	decs := e.decisionsBySurface(t)
	require.NotEmpty(t, decs[messagepolicy.SurfaceMentionDelivery], "the copy to C must be evaluated on mention_delivery")
	for _, d := range decs[messagepolicy.SurfaceMentionDelivery] {
		assert.Equal(t, "h2res-reject-mention-copy", d.PolicyName)
		assert.Equal(t, store.MessagePolicyActionRejected, d.Action)
		assert.Equal(t, e.c.ID, d.RecipientID)
	}
	for _, d := range decs[messagepolicy.SurfaceAgentDM] {
		assert.NotEqual(t, store.MessagePolicyActionRejected, d.Action, "the agent_dm policy must not reject a body without ZZDM")
	}
	// (e) copy planned and evaluated, but never delivered
	rows, disp, conv := e.copyToC(t)
	assert.Zero(t, rows, "no copy row for C")
	assert.Zero(t, disp, "no copy dispatch to C")
	assert.False(t, conv, "no A<->C DM conversation created")
	assert.Empty(t, e.f.dispatcher.getCalls(), "whole-send reject: zero dispatches overall")
	// (f)
	e.assertNotUncovered(t)
}

// TestH2Residual_ConvAddressedTwoAgentDM_AdmittedSendReachesPeerViaAgentDMPath
// is the positive companion to cases 1-2: an admitted conversation-addressed
// send with no recipient id is persisted, dispatched to the peer, evaluated
// on agent_dm, and still never counted as uncovered.
func TestH2Residual_ConvAddressedTwoAgentDM_AdmittedSendReachesPeerViaAgentDMPath(t *testing.T) {
	e := newH2ResidualEnv(t)

	rr := e.f.sendOutbound(t, OutboundMessageRequest{
		ConversationRef: "conv:" + e.dmConv.ID,
		Msg:             "hi plain",
		Type:            messages.TypeInstruction,
	})
	e.logObservations(t, rr.Code, rr.Body.String())

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Len(t, e.dmRows(t), e.rowsAt0+1, "admitted send persists one DM row")
	assert.Equal(t, 1, e.dispatchesTo(e.f.peerAgent.ID), "admitted send dispatches to the peer once")
	assert.Equal(t, int64(1), e.f.faultWrapper.listCalls.Load()-e.listAt0, "policies loaded once")
	assert.Equal(t, int64(1), mpgSumInt64Metric(t, e.collect(t), "scion.message_policy.evaluations"), "one agent_dm evaluation")
	assert.Equal(t, int64(1), e.f.faultWrapper.decisionCalls.Load()-e.decAt0, "one agent_dm admitted row")
	e.assertNotUncovered(t)
}

// TestH2Residual_DirectToPeer_Control_AgentDMKeywordRejected is case 3, the
// control: A addresses B explicitly (conversation_id of the DM plus
// recipient_id = B, the handler's Case (b)). Expect 422 via ExecuteAgentDM.
func TestH2Residual_DirectToPeer_Control_AgentDMKeywordRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(e *h2ResidualEnv) OutboundMessageRequest
	}{
		{name: "conversation_id_plus_peer_recipient_id", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationID: e.dmConv.ID, RecipientID: e.f.peerAgent.ID}
		}},
		{name: "conversation_ref_plus_peer_recipient_id", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationRef: "conv:" + e.dmConv.ID, RecipientID: e.f.peerAgent.ID, Recipient: "agent:" + e.f.peerAgent.Slug}
		}},
		{name: "conversation_ref_at_peer_slug", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationRef: "@" + e.f.peerAgent.Slug}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newH2ResidualEnv(t)
			req := tc.req(e)
			req.Msg = "hi " + h2ResidualDMKeyword
			req.Type = messages.TypeInstruction

			rr := e.f.sendOutbound(t, req)
			e.logObservations(t, rr.Code, rr.Body.String())

			require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
			assert.Len(t, e.dmRows(t), e.rowsAt0)
			assert.Zero(t, e.dispatchesTo(e.f.peerAgent.ID))
			decs := e.decisionsBySurface(t)
			require.NotEmpty(t, decs[messagepolicy.SurfaceAgentDM])
			assert.Equal(t, store.MessagePolicyActionRejected, decs[messagepolicy.SurfaceAgentDM][0].Action)
			e.assertNotUncovered(t)
		})
	}
}

// TestH2Residual_TwoAgentDM_OtherAddressingShapesNeverReachUncoveredBranch
// probes the remaining request shapes that name the A<->B DM conversation
// without the peer as recipient. Each is refused at routing (400) before any
// gate, persistence or dispatch; none reaches the H2 uncovered branch.
func TestH2Residual_TwoAgentDM_OtherAddressingShapesNeverReachUncoveredBranch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     func(e *h2ResidualEnv) OutboundMessageRequest
		errFrag string
	}{
		{name: "conversation_id_without_recipient", errFrag: "recipient is required", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationID: e.dmConv.ID}
		}},
		{name: "conversation_ref_recipient_id_third_agent", errFrag: "recipient may not be supplied", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationRef: "conv:" + e.dmConv.ID, RecipientID: e.c.ID}
		}},
		{name: "conversation_ref_recipient_id_self", errFrag: "recipient may not be supplied", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationRef: "conv:" + e.dmConv.ID, RecipientID: e.f.sender.ID}
		}},
		{name: "conversation_id_recipient_id_third_agent", errFrag: "recipient may not be supplied", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationID: e.dmConv.ID, RecipientID: e.c.ID}
		}},
		{name: "conversation_ref_agent_recipient_string_without_id", errFrag: "not a valid addressee", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{ConversationRef: "conv:" + e.dmConv.ID, Recipient: "agent:" + e.f.peerAgent.Slug}
		}},
		{name: "peer_recipient_id_without_conversation", errFrag: "not a valid addressee", req: func(e *h2ResidualEnv) OutboundMessageRequest {
			return OutboundMessageRequest{RecipientID: e.f.peerAgent.ID}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newH2ResidualEnv(t)
			req := tc.req(e)
			req.Msg = "hi " + h2ResidualDMKeyword
			req.Type = messages.TypeInstruction

			rr := e.f.sendOutbound(t, req)
			e.logObservations(t, rr.Code, rr.Body.String())

			require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
			assert.True(t, strings.Contains(rr.Body.String(), tc.errFrag), "body %q should contain %q", rr.Body.String(), tc.errFrag)
			assert.Len(t, e.dmRows(t), e.rowsAt0)
			assert.Empty(t, e.f.dispatcher.getCalls())
			assert.Equal(t, e.listAt0, e.f.faultWrapper.listCalls.Load(), "refused at routing: zero policy loads")
			assert.Equal(t, e.decAt0, e.f.faultWrapper.decisionCalls.Load(), "refused at routing: zero decision rows")
			e.assertNotUncovered(t)
		})
	}
}
