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

package teams

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin/teamshosts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Entries(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	raw := b.buf.String()
	b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		out = append(out, m)
	}
	return out
}

func findLogEvent(entries []map[string]any, event string) []map[string]any {
	var matched []map[string]any
	for _, e := range entries {
		if e["event"] == event {
			matched = append(matched, e)
		}
	}
	return matched
}

// lookupErrorStore wraps Store and fails GetChannelLink calls.
type lookupErrorStore struct {
	Store
}

func (lookupErrorStore) GetChannelLink(context.Context, string) (*ChannelLink, error) {
	return nil, errors.New("simulated store error")
}

// A4: Cross-project link denial via ThreadID and Conversation.ExternalRef.
func TestTeamsBroker_TargetAuthz_CrossProjectDeniedAndFallsThrough(t *testing.T) {
	for _, tc := range []struct {
		name     string
		buildMsg func() *messages.StructuredMessage
		wantSrc  string
	}{
		{
			name:    "legacy ThreadID",
			wantSrc: string(plugin.TargetSourceThreadID),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "cross-project attempt",
					Type:      messages.TypeInstruction,
					Channel:   "teams",
					ThreadID:  "19:proj-b@thread.tacv2;messageid=99",
				}
			},
		},
		{
			name:    "Conversation.ExternalRef",
			wantSrc: string(plugin.TargetSourceConversation),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "cross-project attempt",
					Type:      messages.TypeInstruction,
					Channel:   "teams",
					Conversation: &messages.Conversation{
						Surface:     "teams",
						ExternalRef: "19:proj-b@thread.tacv2;messageid=99",
						ParentRef:   "19:proj-b@thread.tacv2",
					},
				}
			},
		},
		{
			name:    "Conversation.ParentRef fallback to foreign project",
			wantSrc: string(plugin.TargetSourceConversation),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "parent ref fallback attempt",
					Type:      messages.TypeInstruction,
					Channel:   "teams",
					Conversation: &messages.Conversation{
						Surface:     "teams",
						ExternalRef: "19:proj-a@thread.tacv2;messageid=unseen",
						ParentRef:   "19:proj-b@thread.tacv2;messageid=99",
					},
				}
			},
		},
		{
			name:    "ThreadID ExternalRef with foreign Conversation.ParentRef logs conversation source",
			wantSrc: string(plugin.TargetSourceConversation),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "mixed source parent ref attempt",
					Type:      messages.TypeInstruction,
					Channel:   "teams",
					ThreadID:  "19:proj-a@thread.tacv2;messageid=unseen",
					Conversation: &messages.Conversation{
						Surface:   "teams",
						ParentRef: "19:proj-b@thread.tacv2;messageid=99",
					},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var deliveredPaths []string
			serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				deliveredPaths = append(deliveredPaths, r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
			}))

			logBuf := &lockedBuffer{}
			log := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			broker := NewBroker(log)
			configureBrokerWithBotClient(t, broker, client)

			ctx := context.Background()
			// Link proj-a and proj-b to separate Teams channels.
			require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
				ConversationID: "19:proj-a@thread.tacv2",
				ProjectID:      "proj-a",
				Active:         true,
			}))
			require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
				ConversationID: "19:proj-b@thread.tacv2",
				ProjectID:      "proj-b",
				Active:         true,
			}))
			require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
				ConversationID: "19:proj-a@thread.tacv2",
				ServiceURL:     serviceURL,
				UpdatedAt:      time.Now(),
			}))
			require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
				ConversationID: "19:proj-b@thread.tacv2;messageid=99",
				ServiceURL:     serviceURL,
				UpdatedAt:      time.Now(),
			}))

			require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", tc.buildMsg()))

			broker.mu.Lock()
			sq := broker.sendQueue
			broker.mu.Unlock()
			sq.Close()

			// Must fall through to proj-a and never deliver to proj-b.
			mu.Lock()
			require.Equal(t, []string{"/amer/v3/conversations/19:proj-a@thread.tacv2/activities"}, deliveredPaths)
			mu.Unlock()

			// Exactly one outbound_target_denied event with reason=foreign_project.
			denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
			require.Len(t, denied, 1)
			assert.Equal(t, "teams", denied[0]["surface"])
			assert.Equal(t, "proj-a", denied[0]["project_id"])
			assert.Equal(t, tc.wantSrc, denied[0]["source"])
			assert.Equal(t, string(plugin.DenyForeignProject), denied[0]["reason"])
		})
	}
}

// A5: Unlinked, inactive link, lookup error, nil store, and missing project fail closed.
func TestTeamsBroker_TargetAuthz_FailClosedReasons(t *testing.T) {
	t.Run("not_linked", func(t *testing.T) {
		var calls atomic.Int32
		serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		logBuf := &lockedBuffer{}
		broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		configureBrokerWithBotClient(t, broker, client)

		ctx := context.Background()
		require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
			ConversationID: "19:unlinked@thread.tacv2",
			ServiceURL:     serviceURL,
			UpdatedAt:      time.Now(),
		}))

		require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "hello",
			Type:      messages.TypeInstruction,
			Channel:   "teams",
			ThreadID:  "19:unlinked@thread.tacv2",
		}))

		assert.Equal(t, int32(0), calls.Load())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNotLinked), denied[0]["reason"])
	})

	t.Run("inactive_link", func(t *testing.T) {
		var calls atomic.Int32
		serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		logBuf := &lockedBuffer{}
		broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		configureBrokerWithBotClient(t, broker, client)

		ctx := context.Background()
		require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
			ConversationID: "19:inactive@thread.tacv2",
			ProjectID:      "proj-a",
			Active:         false,
		}))
		require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
			ConversationID: "19:inactive@thread.tacv2",
			ServiceURL:     serviceURL,
			UpdatedAt:      time.Now(),
		}))

		require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "teams",
			ThreadID: "19:inactive@thread.tacv2",
		}))

		assert.Equal(t, int32(0), calls.Load())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyInactiveLink), denied[0]["reason"])
	})

	t.Run("lookup_error", func(t *testing.T) {
		var calls atomic.Int32
		serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		logBuf := &lockedBuffer{}
		broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		configureBrokerWithBotClient(t, broker, client)

		ctx := context.Background()
		require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
			ConversationID: "19:err@thread.tacv2",
			ServiceURL:     serviceURL,
			UpdatedAt:      time.Now(),
		}))
		broker.mu.Lock()
		broker.store = lookupErrorStore{Store: broker.store}
		broker.mu.Unlock()

		require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "teams",
			ThreadID: "19:err@thread.tacv2",
		}))

		assert.Equal(t, int32(0), calls.Load())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyLookupError), denied[0]["reason"])
	})

	t.Run("no_store", func(t *testing.T) {
		var calls atomic.Int32
		_, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		logBuf := &lockedBuffer{}
		broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		configureBrokerWithBotClient(t, broker, client)
		broker.mu.Lock()
		origStore := broker.store
		broker.store = nil
		broker.mu.Unlock()
		t.Cleanup(func() { _ = origStore.Close() })

		require.NoError(t, broker.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "teams",
			ThreadID: "19:any@thread.tacv2",
		}))

		assert.Equal(t, int32(0), calls.Load())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNoStore), denied[0]["reason"])
	})

	t.Run("no_project", func(t *testing.T) {
		logBuf := &lockedBuffer{}
		broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		configureBrokerForPublish(t, broker)

		ok, reason := broker.authorizeExplicitTarget(context.Background(), broker.store, "", "coder", &messages.StructuredMessage{}, "19:any@thread.tacv2")
		assert.False(t, ok)
		assert.Equal(t, plugin.DenyNoProject, reason)
	})

	t.Run("no_project_non_canonical_topic", func(t *testing.T) {
		var mu sync.Mutex
		var deliveredPaths []string
		serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			deliveredPaths = append(deliveredPaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
		}))

		logBuf := &lockedBuffer{}
		broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		configureBrokerWithBotClient(t, broker, client)

		ctx := context.Background()
		// Even if a link exists under the fallback-parsed "scion" prefix,
		// a non-canonical topic like scion.global.broadcast must yield
		// projectID="" for explicit-target authorization (DenyNoProject).
		require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
			ConversationID: "19:scion@thread.tacv2",
			ProjectID:      "scion",
			Active:         true,
		}))
		require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
			ConversationID: "19:scion@thread.tacv2",
			ServiceURL:     serviceURL,
			UpdatedAt:      time.Now(),
		}))
		require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
			ConversationID: "19:scion@thread.tacv2;messageid=99",
			ServiceURL:     serviceURL,
			UpdatedAt:      time.Now(),
		}))

		require.NoError(t, broker.Publish(ctx, "scion.global.broadcast", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "broadcast message",
			Type:     messages.TypeInstruction,
			Channel:  "teams",
			ThreadID: "19:scion@thread.tacv2;messageid=99",
		}))

		broker.mu.Lock()
		sq := broker.sendQueue
		broker.mu.Unlock()
		sq.Close()

		// Explicit thread target must be denied with no_project and never sent to;
		// implicit routing remains unaffected.
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, "", denied[0]["project_id"])
		assert.Equal(t, string(plugin.DenyNoProject), denied[0]["reason"])

		mu.Lock()
		assert.Equal(t, []string{"/amer/v3/conversations/19:scion@thread.tacv2/activities"}, deliveredPaths)
		mu.Unlock()
	})
}

// A6: Active link for proj-a with ";messageid=" thread suffix delivers to exact thread.
func TestTeamsBroker_TargetAuthz_AllowedThreadSuffix(t *testing.T) {
	var mu sync.Mutex
	var deliveredPaths []string
	serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveredPaths = append(deliveredPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
	}))

	broker := NewBroker(slog.Default())
	configureBrokerWithBotClient(t, broker, client)

	ctx := context.Background()
	require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
		ConversationID: "19:proj-a@thread.tacv2",
		ProjectID:      "proj-a",
		Active:         true,
	}))
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID: "19:proj-a@thread.tacv2;messageid=170000000042",
		ServiceURL:     serviceURL,
		UpdatedAt:      time.Now(),
	}))

	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "thread reply",
		Type:      messages.TypeInstruction,
		Channel:   "teams",
		Conversation: &messages.Conversation{
			Surface:     "teams",
			ExternalRef: "19:proj-a@thread.tacv2;messageid=170000000042",
			ParentRef:   "19:proj-a@thread.tacv2",
		},
	}))

	broker.mu.Lock()
	sq := broker.sendQueue
	broker.mu.Unlock()
	sq.Close()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/amer/v3/conversations/19:proj-a@thread.tacv2;messageid=170000000042/activities"}, deliveredPaths)
}

// A7: Recipient personal DM allowed for owner, denied for another user.
func TestTeamsBroker_TargetAuthz_PersonalDM(t *testing.T) {
	var mu sync.Mutex
	var deliveredPaths []string
	serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveredPaths = append(deliveredPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
	}))

	logBuf := &lockedBuffer{}
	broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
	configureBrokerWithBotClient(t, broker, client)

	ctx := context.Background()
	// Alice and Bob each have a personal DM conversation.
	require.NoError(t, broker.store.CreateUserMapping(ctx, &TeamsUserMapping{
		TeamsUserID: "teams-alice",
		ScionUserID: "u-alice",
		ScionEmail:  "alice@example.com",
		LinkedAt:    time.Now(),
	}))
	require.NoError(t, broker.store.CreateUserMapping(ctx, &TeamsUserMapping{
		TeamsUserID: "teams-bob",
		ScionUserID: "u-bob",
		ScionEmail:  "bob@example.com",
		LinkedAt:    time.Now(),
	}))
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID:   "a:dm-alice",
		ServiceURL:       serviceURL,
		ConversationType: "personal",
		UpdatedAt:        time.Now(),
	}))
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID:   "a:dm-bob",
		ServiceURL:       serviceURL,
		ConversationType: "personal",
		UpdatedAt:        time.Now(),
	}))
	require.NoError(t, broker.store.SetConversationContext(ctx, &ConversationContext{
		TeamsUserID:        "teams-alice",
		ProjectID:          "proj-a",
		AgentSlug:          "coder",
		LastConversationID: "a:dm-alice",
		LastMessageAt:      time.Now(),
	}))
	require.NoError(t, broker.store.SetConversationContext(ctx, &ConversationContext{
		TeamsUserID:        "teams-bob",
		ProjectID:          "proj-a",
		AgentSlug:          "coder",
		LastConversationID: "a:dm-bob",
		LastMessageAt:      time.Now(),
	}))

	// 1. Alice addressed with her own personal DM (per-slug context match) -> allowed.
	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u-alice.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "for alice",
		Type:      messages.TypeInstruction,
		Channel:   "teams",
		ThreadID:  "a:dm-alice",
	}))

	// 1b. Different agent slug with no per-slug context falls back to Alice's
	// latest project ConversationContext -> allowed without denial.
	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u-alice.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:reviewer",
		Recipient: "user:alice@example.com",
		Msg:       "for alice via latest context",
		Type:      messages.TypeInstruction,
		Channel:   "teams",
		ThreadID:  "a:dm-alice",
	}))

	// 2. Alice addressed with Bob's personal DM -> denied (not_recipient_dm),
	// falls through to Alice's own ConversationContext (a:dm-alice), never reaches Bob.
	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u-alice.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "wrong dm target",
		Type:      messages.TypeInstruction,
		Channel:   "teams",
		ThreadID:  "a:dm-bob",
	}))

	broker.mu.Lock()
	sq := broker.sendQueue
	broker.mu.Unlock()
	sq.Close()

	mu.Lock()
	assert.Equal(t, []string{
		"/amer/v3/conversations/a:dm-alice/activities",
		"/amer/v3/conversations/a:dm-alice/activities",
		"/amer/v3/conversations/a:dm-alice/activities",
	}, deliveredPaths)
	mu.Unlock()

	denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
	require.Len(t, denied, 1)
	assert.Equal(t, string(plugin.DenyNotRecipientDM), denied[0]["reason"])
}

// A8 + A9: Metadata targets and teams_service_url are completely ignored and never contacted.
func TestTeamsBroker_MetadataServiceURLNeverContacted(t *testing.T) {
	var untrustedHits atomic.Int32
	var untrustedAuth atomic.Value
	untrustedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		untrustedHits.Add(1)
		untrustedAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer untrustedSrv.Close()

	var mu sync.Mutex
	var deliveredPaths []string
	serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveredPaths = append(deliveredPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
	}))

	broker := NewBroker(slog.Default())
	configureBrokerWithBotClient(t, broker, client)

	ctx := context.Background()
	require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
		ConversationID: "19:proj-a@thread.tacv2",
		ProjectID:      "proj-a",
		Active:         true,
	}))
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID: "19:proj-a@thread.tacv2",
		ServiceURL:     serviceURL,
		UpdatedAt:      time.Now(),
	}))

	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "hello",
		Type:      messages.TypeInstruction,
		Channel:   "teams",
		Metadata: map[string]string{
			"teams_conversation_id": "19:other@thread.tacv2",
			"teams_service_url":     untrustedSrv.URL,
			"teams_reply_to_id":     "act-99",
		},
	}))

	broker.mu.Lock()
	sq := broker.sendQueue
	broker.mu.Unlock()
	sq.Close()

	assert.Equal(t, int32(0), untrustedHits.Load(), "untrusted teams_service_url must receive zero requests")
	assert.Nil(t, untrustedAuth.Load())

	mu.Lock()
	assert.Equal(t, []string{"/amer/v3/conversations/19:proj-a@thread.tacv2/activities"}, deliveredPaths)
	mu.Unlock()
}

// A10: Sender.doRequest enforces scheme, host allowlist, userinfo, port 443, and refuses redirects without calling token provider on URL refusal.
func TestSender_ServiceURLAllowlistAndNoRedirects(t *testing.T) {
	var tokenFetches atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenFetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "secret-bot-token", ExpiresIn: 3600})
	}))
	defer tokenServer.Close()

	tp := NewTokenProvider("app", "secret", "tenant")
	tp.tokenEndpoint = tokenServer.URL
	tp.httpClient = tokenServer.Client()

	logBuf := &lockedBuffer{}
	sender := NewSender(tp, slog.New(slog.NewJSONHandler(logBuf, nil)))

	badURLs := []struct {
		name string
		url  string
	}{
		{"http scheme", "http://smba.trafficmanager.net/amer"},
		{"non-allowlisted host", "https://unlisted.example/amer"},
		{"userinfo", "https://user:pass@smba.trafficmanager.net/amer"},
		{"non-443 port", "https://smba.trafficmanager.net:8443/amer"},
	}
	for _, tc := range badURLs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sender.sendActivity(context.Background(), tc.url, "conv-1", &Activity{Type: "message", Text: "hi"})
			require.Error(t, err)
			var denied *teamshosts.ServiceURLDeniedError
			assert.ErrorAs(t, err, &denied)
		})
	}
	assert.Equal(t, int32(0), tokenFetches.Load(), "token provider must not be called when service URL is refused")

	t.Run("redirect refused and target never contacted", func(t *testing.T) {
		var redirectTargetHits atomic.Int32
		redirectTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirectTargetHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer redirectTarget.Close()

		redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL+"/redirected", http.StatusFound)
		}))
		defer redirector.Close()

		redirAddr := redirector.Listener.Addr().String()
		tr := redirector.Client().Transport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			if strings.HasPrefix(addr, "smba.trafficmanager.net:") {
				return d.DialContext(ctx, network, redirAddr)
			}
			return d.DialContext(ctx, network, addr)
		}

		// Even if CheckRedirect is nil on a custom client, Sender must enforce no redirects.
		s := NewSender(tp, slog.New(slog.NewJSONHandler(logBuf, nil)))
		s.httpClient = &http.Client{Transport: tr}

		_, err := s.sendActivity(context.Background(), "https://smba.trafficmanager.net/amer", "conv-1", &Activity{Type: "message", Text: "hi"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRedirectNotAllowed)
		assert.Equal(t, int32(0), redirectTargetHits.Load(), "redirect target must never be contacted")
	})

	t.Run("extra service_url_hosts extends allowlist without emptying default", func(t *testing.T) {
		var gotHost string
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHost = r.Host
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "ok"})
		}))
		defer ts.Close()

		s := NewSender(tp, slog.Default())
		s.SetExtraServiceHosts([]string{"emulator.internal"})
		s.httpClient = botConnectorClientForAddr(ts.Client(), ts.Listener.Addr().String())

		_, err := s.sendActivity(context.Background(), "https://emulator.internal/amer", "conv-1", &Activity{Type: "message", Text: "hi"})
		require.NoError(t, err)
		assert.Equal(t, "emulator.internal", gotHost)

		// Default allowlist host still works.
		_, err = s.sendActivity(context.Background(), "https://smba.trafficmanager.net/amer", "conv-1", &Activity{Type: "message", Text: "hi"})
		require.NoError(t, err)
	})
}

// A11: Inbound JWT serviceurl binding and allowlist enforcement before storing ConversationReference.
func TestWebhookServer_ServiceURLClaimBinding(t *testing.T) {
	newServerWithBroker := func(t *testing.T) (*httptest.Server, *testJWKS, *TeamsBroker) {
		t.Helper()
		tj := newTestJWKS(t)
		t.Cleanup(tj.close)

		broker := NewBroker(slog.Default())
		configureBrokerForPublish(t, broker)

		validator := NewJWTValidator("bot-id")
		validator.openIDMetadataURL = tj.server.URL + "/.well-known/openidconfiguration"
		validator.httpClient = tj.server.Client()

		ws := NewWebhookServer(":0", validator, broker, slog.Default())
		ts := httptest.NewServer(ws.server.Handler)
		t.Cleanup(ts.Close)
		return ts, tj, broker
	}

	postActivity := func(t *testing.T, tsURL, token string, act Activity) *http.Response {
		t.Helper()
		body, err := json.Marshal(act)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, tsURL+"/api/messages", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}

	t.Run("mismatched serviceurl claim rejected 401 and no ref stored", func(t *testing.T) {
		ts, tj, broker := newServerWithBroker(t)
		claims := tj.validClaims("bot-id")
		claims["serviceurl"] = "https://smba.trafficmanager.net/amer/"
		token := tj.signToken(claims)

		resp := postActivity(t, ts.URL, token, Activity{
			Type:         "conversationUpdate",
			ID:           "act-mismatch",
			Conversation: ConversationAccount{ID: "conv-mismatch"},
			ServiceURL:   "https://smba.trafficmanager.net/emea/",
		})
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		ref, err := broker.store.GetConversationReference(context.Background(), "conv-mismatch")
		require.NoError(t, err)
		assert.Nil(t, ref)
	})

	t.Run("missing serviceurl claim rejected 401", func(t *testing.T) {
		ts, tj, broker := newServerWithBroker(t)
		claims := tj.validClaims("bot-id")
		delete(claims, "serviceurl")
		token := tj.signToken(claims)

		resp := postActivity(t, ts.URL, token, Activity{
			Type:         "conversationUpdate",
			ID:           "act-noclaim",
			Conversation: ConversationAccount{ID: "conv-noclaim"},
			ServiceURL:   "https://smba.trafficmanager.net/amer/",
		})
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		ref, err := broker.store.GetConversationReference(context.Background(), "conv-noclaim")
		require.NoError(t, err)
		assert.Nil(t, ref)
	})

	t.Run("non-allowlisted serviceUrl even when matching claim rejected 401", func(t *testing.T) {
		ts, tj, broker := newServerWithBroker(t)
		claims := tj.validClaims("bot-id")
		claims["serviceurl"] = "https://unlisted.example/amer/"
		token := tj.signToken(claims)

		resp := postActivity(t, ts.URL, token, Activity{
			Type:         "conversationUpdate",
			ID:           "act-unlisted",
			Conversation: ConversationAccount{ID: "conv-unlisted"},
			ServiceURL:   "https://unlisted.example/amer/",
		})
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		ref, err := broker.store.GetConversationReference(context.Background(), "conv-unlisted")
		require.NoError(t, err)
		assert.Nil(t, ref)
	})

	t.Run("matching allowlisted serviceUrl accepted and stored", func(t *testing.T) {
		ts, tj, broker := newServerWithBroker(t)
		claims := tj.validClaims("bot-id")
		claims["serviceurl"] = "https://smba.trafficmanager.net/amer/"
		token := tj.signToken(claims)

		resp := postActivity(t, ts.URL, token, Activity{
			Type:         "conversationUpdate",
			ID:           "act-valid",
			Conversation: ConversationAccount{ID: "conv-valid"},
			ServiceURL:   "https://smba.trafficmanager.net/amer",
		})
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		require.Eventually(t, func() bool {
			ref, err := broker.store.GetConversationReference(context.Background(), "conv-valid")
			return err == nil && ref != nil && ref.ServiceURL == "https://smba.trafficmanager.net/amer"
		}, 2*time.Second, 10*time.Millisecond)
	})
}

// A12: Pre-existing stored ConversationReference with bad ServiceURL is refused at egress and logged.
func TestTeamsBroker_PreExistingBadStoredServiceURLRefused(t *testing.T) {
	var tokenFetches atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenFetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "secret", ExpiresIn: 3600})
	}))
	defer tokenServer.Close()

	logBuf := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, nil))
	broker := NewBroker(log)
	configureBrokerForPublish(t, broker)

	broker.mu.Lock()
	broker.tokenProvider.tokenEndpoint = tokenServer.URL
	broker.tokenProvider.httpClient = tokenServer.Client()
	broker.sendQueue.Close()
	broker.sender = NewSender(broker.tokenProvider, log)
	broker.sendQueue = NewSendQueue(broker.sender, 10, 1*time.Millisecond, log)
	broker.mu.Unlock()

	ctx := context.Background()
	require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
		ConversationID: "19:legacy-bad@thread.tacv2",
		ProjectID:      "proj-a",
		Active:         true,
	}))
	// Simulate a pre-existing row in SQLite with a non-allowlisted ServiceURL.
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID: "19:legacy-bad@thread.tacv2",
		ServiceURL:     "https://unlisted.example/amer/",
		UpdatedAt:      time.Now(),
	}))

	err := broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version: messages.Version,
		Sender:  "agent:coder",
		Msg:     "hello",
		Type:    messages.TypeInstruction,
		Channel: "teams",
	})
	require.Error(t, err)
	assert.Equal(t, int32(0), tokenFetches.Load())

	denied := findLogEvent(logBuf.Entries(t), "teams_service_url_denied")
	require.NotEmpty(t, denied)
}

// A13: Observer filter (agent-to-agent) fails closed across no-link,
// ShowAgentToAgent=false, and store lookup errors.
func TestTeamsBroker_ObserverFilterFailsClosed(t *testing.T) {
	var mu sync.Mutex
	var deliveredPaths []string
	serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveredPaths = append(deliveredPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
	}))

	broker := NewBroker(slog.Default())
	configureBrokerWithBotClient(t, broker, client)

	ctx := context.Background()
	// Two linked channels: one with ShowAgentToAgent=false, one with ShowAgentToAgent=true.
	require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
		ConversationID:   "19:quiet@thread.tacv2",
		ProjectID:        "proj-a",
		Active:           true,
		ShowAgentToAgent: false,
	}))
	require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
		ConversationID:   "19:verbose@thread.tacv2",
		ProjectID:        "proj-a",
		Active:           true,
		ShowAgentToAgent: true,
	}))
	for _, id := range []string{"19:quiet@thread.tacv2", "19:verbose@thread.tacv2"} {
		require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
			ConversationID: id,
			ServiceURL:     serviceURL,
			UpdatedAt:      time.Now(),
		}))
	}

	// Agent-to-agent observer message must only reach verbose channel.
	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.agent.reviewer.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "agent:reviewer",
		Msg:       "please review",
		Type:      messages.TypeInstruction,
	}))

	// Unlinked container (e.g. via ConversationContext) must not receive agent-to-agent.
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID: "19:unlinked-ctx@thread.tacv2",
		ServiceURL:     serviceURL,
		UpdatedAt:      time.Now(),
	}))
	require.NoError(t, broker.store.SetConversationContext(ctx, &ConversationContext{
		TeamsUserID:        "agent:reviewer",
		ProjectID:          "proj-a",
		AgentSlug:          "reviewer",
		LastConversationID: "19:unlinked-ctx@thread.tacv2",
		LastMessageAt:      time.Now(),
	}))
	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.agent.reviewer.messages", &messages.StructuredMessage{
		Version:     messages.Version,
		Sender:      "agent:coder",
		Recipient:   "agent:reviewer",
		RecipientID: "agent:reviewer",
		Msg:         "context-routed observer copy",
		Type:        messages.TypeInstruction,
	}))

	// Store lookup error during observer filter must fail closed (no delivery).
	broker.mu.Lock()
	broker.store = lookupErrorStore{Store: broker.store}
	broker.mu.Unlock()
	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.agent.reviewer.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "agent:reviewer",
		Msg:       "lookup-error observer copy",
		Type:      messages.TypeInstruction,
	}))

	broker.mu.Lock()
	sq := broker.sendQueue
	broker.mu.Unlock()
	sq.Close()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{
		"/amer/v3/conversations/19:verbose@thread.tacv2/activities",
	}, deliveredPaths)
}

// Regression test (R1): TypeStateChange user subscription notifications from
// messages.NewNotification continue to deliver to active Teams channel links
// with default flags (ShowStateChanges=false, ShowAgentToAgent=false).
func TestTeamsBroker_Publish_StateChangeNotificationDeliveredWithDefaultFlags(t *testing.T) {
	var mu sync.Mutex
	var deliveredPaths []string
	serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveredPaths = append(deliveredPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
	}))

	broker := NewBroker(slog.Default())
	configureBrokerWithBotClient(t, broker, client)

	ctx := context.Background()
	require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
		ConversationID: "19:default-flags@thread.tacv2",
		ProjectID:      "proj-a",
		Active:         true,
	}))
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID: "19:default-flags@thread.tacv2",
		ServiceURL:     serviceURL,
		UpdatedAt:      time.Now(),
	}))

	notif := messages.NewNotification("agent:coder", "user:u1", "Agent coder completed", messages.TypeStateChange)
	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.user.u1.messages", notif))

	broker.mu.Lock()
	sq := broker.sendQueue
	broker.mu.Unlock()
	sq.Close()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{
		"/amer/v3/conversations/19:default-flags@thread.tacv2/activities",
	}, deliveredPaths)
}

// O1: Direct HandleActivity / upsertConversationRef with a non-allowlisted
// ServiceURL must not write a ConversationReference row.
func TestTeamsBroker_HandleActivity_DisallowedServiceURLNotStored(t *testing.T) {
	logBuf := &lockedBuffer{}
	broker := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
	configureBrokerForPublish(t, broker)

	_, err := broker.HandleActivity(context.Background(), &Activity{
		Type:         "conversationUpdate",
		ID:           "act-direct-bad-url",
		Conversation: ConversationAccount{ID: "19:direct-bad@thread.tacv2"},
		ServiceURL:   "https://unlisted.example/amer/",
	})
	require.NoError(t, err)

	ref, err := broker.store.GetConversationReference(context.Background(), "19:direct-bad@thread.tacv2")
	require.NoError(t, err)
	assert.Nil(t, ref, "disallowed ServiceURL must not be stored by upsertConversationRef")

	denied := findLogEvent(logBuf.Entries(t), "teams_service_url_denied")
	require.Len(t, denied, 1)
	assert.Equal(t, "host_not_allowed", denied[0]["reason"])
}

// A13b: Input-needed with cross-project explicit target never posts card or stores PendingAskUser for proj-b.
func TestTeamsBroker_InputNeeded_CrossProjectTargetDenied(t *testing.T) {
	var calls atomic.Int32
	serviceURL, client := newTestBotConnectorServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ActivityResponse{ID: "resp-1"})
	}))

	broker := NewBroker(slog.Default())
	configureBrokerWithBotClient(t, broker, client)

	ctx := context.Background()
	// Only proj-b has a linked channel; proj-a has none.
	require.NoError(t, broker.store.CreateChannelLink(ctx, &ChannelLink{
		ConversationID: "19:proj-b@thread.tacv2",
		ProjectID:      "proj-b",
		Active:         true,
	}))
	require.NoError(t, broker.store.UpsertConversationReference(ctx, &ConversationReference{
		ConversationID: "19:proj-b@thread.tacv2",
		ServiceURL:     serviceURL,
		UpdatedAt:      time.Now(),
	}))

	require.NoError(t, broker.Publish(ctx, "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "Approve action?",
		Type:      messages.TypeInputNeeded,
		Channel:   "teams",
		ThreadID:  "19:proj-b@thread.tacv2",
		Metadata: map[string]string{
			"request_id": "req-cross-proj",
			"choices":    `["Yes","No"]`,
		},
	}))

	broker.mu.Lock()
	sq := broker.sendQueue
	broker.mu.Unlock()
	sq.Close()

	assert.Equal(t, int32(0), calls.Load(), "no card may be sent to proj-b")
	pending, err := broker.store.GetPendingAskUser(ctx, "req-cross-proj")
	require.NoError(t, err)
	assert.Nil(t, pending, "no PendingAskUser row may be created for proj-b")
}
