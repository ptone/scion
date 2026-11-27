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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/eventbus"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSpokeBus struct {
	mu   sync.Mutex
	msgs []*messages.StructuredMessage
}

func (b *recordingSpokeBus) Publish(_ context.Context, _ string, msg *messages.StructuredMessage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := *msg
	if msg.Metadata != nil {
		cp.Metadata = make(map[string]string, len(msg.Metadata))
		for k, v := range msg.Metadata {
			cp.Metadata[k] = v
		}
	}
	b.msgs = append(b.msgs, &cp)
	return nil
}

func (b *recordingSpokeBus) Subscribe(string, eventbus.EventHandler) (eventbus.Subscription, error) {
	return nullSub{}, nil
}

func (b *recordingSpokeBus) Close() error { return nil }

func (b *recordingSpokeBus) snapshot() []*messages.StructuredMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*messages.StructuredMessage, len(b.msgs))
	copy(out, b.msgs)
	return out
}

func (b *recordingSpokeBus) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = nil
}

func TestMessageBrokerProxy_StripsChatSurfaceMetadata(t *testing.T) {
	s := newBrokerTestStore(t)
	projectID := setupBrokerTestProject(t, s)
	setupBrokerTestAgent(t, s, projectID, "test-agent", "running")

	ctx := context.Background()
	user := &store.User{ID: api.NewUUID(), Email: "alice@example.com", DisplayName: "Alice"}
	require.NoError(t, s.CreateUser(ctx, user))

	events := NewChannelEventPublisher()
	t.Cleanup(events.Close)

	inproc := eventbus.NewInProcessEventBus(slog.Default())
	t.Cleanup(func() { _ = inproc.Close() })

	var inprocMu sync.Mutex
	var inprocMsgs []*messages.StructuredMessage
	sub, err := inproc.Subscribe("scion.project."+projectID+".>", func(_ context.Context, _ string, m *messages.StructuredMessage) {
		inprocMu.Lock()
		cp := *m
		if m.Metadata != nil {
			cp.Metadata = make(map[string]string, len(m.Metadata))
			for k, v := range m.Metadata {
				cp.Metadata[k] = v
			}
		}
		inprocMsgs = append(inprocMsgs, &cp)
		inprocMu.Unlock()
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	spoke := &recordingSpokeBus{}
	fanout := eventbus.NewFanOutEventBus([]eventbus.NamedEventBus{
		{Name: eventbus.InProcessBusName, Bus: inproc},
		{Name: "fake-chat-spoke", Bus: spoke},
	}, slog.Default())

	dispatcher := &brokerMockDispatcher{}
	proxy := NewMessageBrokerProxy(fanout, s, events, func() AgentDispatcher { return dispatcher }, slog.Default())
	proxy.Start()
	t.Cleanup(proxy.Stop)

	proxy.subscribeAgent(projectID, "test-agent")
	proxy.subscribeProjectUserMessages(projectID)
	proxy.subscribeProjectBroadcast(projectID)

	newDirtyMetadata := func() map[string]string {
		return map[string]string{
			"slack_channel_id":      "C_OTHER",
			"teams_service_url":     "https://untrusted.example",
			"teams_conversation_id": "19:other@thread.tacv2",
			"discord_channel_id":    "999888777",
			"telegram_chat_id":      "-100999",
			"custom_keep":           "preserved",
		}
	}

	assertStripped := func(t *testing.T, md map[string]string) {
		t.Helper()
		for _, k := range []string{"slack_channel_id", "teams_service_url", "teams_conversation_id", "discord_channel_id", "telegram_chat_id"} {
			_, exists := md[k]
			assert.False(t, exists, "metadata key %q should have been stripped", k)
		}
		assert.Equal(t, "preserved", md["custom_keep"])
	}

	t.Run("PublishUserMessage", func(t *testing.T) {
		spoke.reset()
		origMD := newDirtyMetadata()
		msg := messages.NewInstruction("agent:test-agent", "user:"+user.Email, "hello user")
		msg.RecipientID = user.ID
		msg.Metadata = origMD

		require.NoError(t, proxy.PublishUserMessage(ctx, projectID, user.ID, msg))

		got := spoke.snapshot()
		require.Len(t, got, 1)
		assertStripped(t, got[0].Metadata)

		// Caller's original metadata must be unchanged.
		assert.Equal(t, "C_OTHER", origMD["slack_channel_id"])
		assert.Equal(t, "https://untrusted.example", origMD["teams_service_url"])

		// In-process bus subscriber and persistence must also receive stripped metadata.
		require.Eventually(t, func() bool {
			inprocMu.Lock()
			defer inprocMu.Unlock()
			for _, m := range inprocMsgs {
				if m.Msg == "hello user" {
					assertStripped(t, m.Metadata)
					return true
				}
			}
			return false
		}, 2*time.Second, 20*time.Millisecond)
		require.Eventually(t, func() bool {
			res, err := s.ListMessages(ctx, store.MessageFilter{
				ProjectID:   projectID,
				RecipientID: user.ID,
			}, store.ListOptions{Limit: 10})
			if err != nil || len(res.Items) == 0 {
				return false
			}
			for _, item := range res.Items {
				if item.Msg == "hello user" {
					return true
				}
			}
			return false
		}, 2*time.Second, 20*time.Millisecond)
	})

	t.Run("PublishMessage", func(t *testing.T) {
		spoke.reset()
		beforeDispatch := len(dispatcher.getMessages())
		origMD := newDirtyMetadata()
		msg := messages.NewInstruction("user:alice", "agent:test-agent", "hello agent")
		msg.Metadata = origMD

		require.NoError(t, proxy.PublishMessage(ctx, projectID, msg))

		got := spoke.snapshot()
		require.Len(t, got, 1)
		assertStripped(t, got[0].Metadata)
		assert.Equal(t, "https://untrusted.example", origMD["teams_service_url"])

		require.Eventually(t, func() bool {
			return len(dispatcher.getMessages()) > beforeDispatch
		}, 2*time.Second, 20*time.Millisecond)
	})

	t.Run("PublishBroadcast", func(t *testing.T) {
		spoke.reset()
		beforeDispatch := len(dispatcher.getMessages())
		origMD := newDirtyMetadata()
		msg := messages.NewInstruction("user:alice", "broadcast", "hello all")
		msg.Metadata = origMD

		require.NoError(t, proxy.PublishBroadcast(ctx, projectID, msg))

		got := spoke.snapshot()
		require.Len(t, got, 1)
		assertStripped(t, got[0].Metadata)
		assert.Equal(t, "-100999", origMD["telegram_chat_id"])

		require.Eventually(t, func() bool {
			return len(dispatcher.getMessages()) > beforeDispatch
		}, 2*time.Second, 20*time.Millisecond)
	})

	t.Run("PublishToGroup", func(t *testing.T) {
		spoke.reset()
		beforeDispatch := len(dispatcher.getMessages())
		origMD := newDirtyMetadata()
		msg := messages.NewInstruction("user:alice", "", "hello group")
		msg.Metadata = origMD

		errs := proxy.PublishToGroup(ctx, projectID, []messages.GroupRecipient{
			{Kind: messages.RecipientAgent, Name: "test-agent"},
			{Kind: messages.RecipientUser, Name: user.ID},
		}, msg)
		require.Empty(t, errs)

		got := spoke.snapshot()
		require.Len(t, got, 2)
		for _, m := range got {
			assertStripped(t, m.Metadata)
		}
		assert.Equal(t, "999888777", origMD["discord_channel_id"])

		require.Eventually(t, func() bool {
			return len(dispatcher.getMessages()) > beforeDispatch
		}, 2*time.Second, 20*time.Millisecond)
	})
}

func TestHandleAgentOutboundMessage_StripsTeamsMetadata(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	project := &store.Project{ID: api.NewUUID(), Name: "strip-http-project", Slug: "strip-http-project"}
	require.NoError(t, s.CreateProject(ctx, project))
	user := &store.User{ID: api.NewUUID(), Email: "strip-http@example.com", DisplayName: "Strip HTTP"}
	require.NoError(t, s.CreateUser(ctx, user))
	agent := &store.Agent{
		ID:              api.NewUUID(),
		Name:            "strip-http-agent",
		Slug:            "strip-http-agent",
		ProjectID:       project.ID,
		Phase:           "running",
		RuntimeBrokerID: "test-broker",
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	inproc := eventbus.NewInProcessEventBus(slog.Default())
	t.Cleanup(func() { _ = inproc.Close() })

	teamsSpoke := &recordingSpokeBus{}
	fanout := eventbus.NewFanOutEventBus([]eventbus.NamedEventBus{
		{Name: eventbus.InProcessBusName, Bus: inproc},
		{Name: "teams", ChannelID: "teams", Bus: teamsSpoke},
	}, slog.Default())

	events := NewChannelEventPublisher()
	t.Cleanup(events.Close)
	proxy := NewMessageBrokerProxy(fanout, s, events,
		func() AgentDispatcher { return noopDispatcher{} }, slog.Default())
	proxy.Start()
	t.Cleanup(proxy.Stop)
	proxy.subscribeProjectUserMessages(project.ID)
	srv.SetMessageBrokerProxy(proxy)

	outReq := OutboundMessageRequest{
		Recipient: "user:" + user.Email,
		Msg:       "outbound teams message",
		Channel:   "teams",
		Metadata: map[string]string{
			"teams_service_url":     "https://untrusted.example",
			"teams_conversation_id": "19:other@thread.tacv2",
			"teams_reply_to_id":     "act-99",
			"custom_key":            "keep",
		},
	}
	body, err := json.Marshal(outReq)
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(ctx, http.MethodPost,
		"/api/v1/agents/"+agent.ID+"/outbound-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agent.ID},
		ProjectID: project.ID,
	}}))
	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agent.ID)
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	got := teamsSpoke.snapshot()
	require.Len(t, got, 1)
	for k := range got[0].Metadata {
		assert.NotContains(t, k, "teams_", "published message must not contain teams_* key %q", k)
	}
	assert.Equal(t, "keep", got[0].Metadata["custom_key"])
}
