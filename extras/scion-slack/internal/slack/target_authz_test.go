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

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) Entries(t *testing.T) []map[string]any {
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

// faultStore wraps Store to inject lookup errors on selected methods.
type faultStore struct {
	Store
	errGetChannelLink           error
	errGetUserMappingByEmail    error
	errGetConversationContext   error
	errGetLatestConversationCtx error
}

func (s faultStore) GetChannelLink(ctx context.Context, channelID string) (*ChannelLink, error) {
	if s.errGetChannelLink != nil {
		return nil, s.errGetChannelLink
	}
	return s.Store.GetChannelLink(ctx, channelID)
}

func (s faultStore) GetUserMappingByEmail(ctx context.Context, email string) (*SlackUserMapping, error) {
	if s.errGetUserMappingByEmail != nil {
		return nil, s.errGetUserMappingByEmail
	}
	return s.Store.GetUserMappingByEmail(ctx, email)
}

func (s faultStore) GetConversationContext(ctx context.Context, slackUserID, projectID, agentSlug string) (*ConversationContext, error) {
	if s.errGetConversationContext != nil {
		return nil, s.errGetConversationContext
	}
	return s.Store.GetConversationContext(ctx, slackUserID, projectID, agentSlug)
}

func (s faultStore) GetLatestConversationContext(ctx context.Context, slackUserID, projectID string) (*ConversationContext, error) {
	if s.errGetLatestConversationCtx != nil {
		return nil, s.errGetLatestConversationCtx
	}
	return s.Store.GetLatestConversationContext(ctx, slackUserID, projectID)
}

// recordingAskStore wraps Store and records every CreatePendingAskUser call.
type recordingAskStore struct {
	Store
	mu      sync.Mutex
	created []*PendingAskUser
}

func (s *recordingAskStore) CreatePendingAskUser(ctx context.Context, req *PendingAskUser) error {
	s.mu.Lock()
	cp := *req
	s.created = append(s.created, &cp)
	s.mu.Unlock()
	return s.Store.CreatePendingAskUser(ctx, req)
}

func (s *recordingAskStore) Created() []*PendingAskUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PendingAskUser, len(s.created))
	copy(out, s.created)
	return out
}

// A4: Cross-project explicit target is denied, logs outbound_target_denied
// with reason=foreign_project, and falls through to the topic project's routing.
func TestSlackBroker_TargetAuthz_CrossProjectDeniedAndFallsThrough(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wantSrc  string
		buildMsg func() *messages.StructuredMessage
	}{
		{
			name:    "legacy ThreadID with thread_ts",
			wantSrc: string(plugin.TargetSourceThreadID),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "cross-project thread_id",
					Type:      messages.TypeInstruction,
					Channel:   "slack",
					ThreadID:  "C_PROJ_B:1700000000.000100",
				}
			},
		},
		{
			name:    "Conversation.ExternalRef with thread_ts",
			wantSrc: string(plugin.TargetSourceConversation),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "cross-project external_ref",
					Type:      messages.TypeInstruction,
					Channel:   "slack",
					Conversation: &messages.Conversation{
						Surface:     "slack",
						ExternalRef: "C_PROJ_B:1700000000.000100",
						ParentRef:   "C_PROJ_B",
					},
				}
			},
		},
		{
			name:    "Conversation.ExternalRef bare channel",
			wantSrc: string(plugin.TargetSourceConversation),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "cross-project bare channel",
					Type:      messages.TypeInstruction,
					Channel:   "slack",
					Conversation: &messages.Conversation{
						Surface:     "slack",
						ExternalRef: "C_PROJ_B",
					},
				}
			},
		},
		{
			name:    "Conversation.ParentRef with bare timestamp ExternalRef",
			wantSrc: string(plugin.TargetSourceConversation),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "cross-project parent_ref",
					Type:      messages.TypeInstruction,
					Channel:   "slack",
					Conversation: &messages.Conversation{
						Surface:     "slack",
						ExternalRef: "1700000000.000100",
						ParentRef:   "C_PROJ_B",
					},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, getPosted := newTestSlackServer(t)
			logBuf := &lockedLogBuffer{}
			b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
			b.store = newTestStore(t)

			ctx := context.Background()
			require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
				ChannelID: "C_PROJ_A",
				ProjectID: "proj-a",
				Active:    true,
			}))
			require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
				ChannelID: "C_PROJ_B",
				ProjectID: "proj-b",
				Active:    true,
			}))

			require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", tc.buildMsg()))

			posted := getPosted()
			require.Len(t, posted, 1)
			assert.Equal(t, "C_PROJ_A", posted[0].Channel)
			assert.Equal(t, "", posted[0].ThreadTS)

			denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
			require.Len(t, denied, 1)
			assert.Equal(t, "slack", denied[0]["surface"])
			assert.Equal(t, "proj-a", denied[0]["project_id"])
			assert.Equal(t, tc.wantSrc, denied[0]["source"])
			assert.Equal(t, string(plugin.DenyForeignProject), denied[0]["reason"])
		})
	}
}

// A5 + Audit I1: Unlinked, inactive link, lookup error, nil store, unknown
// container format, and non-canonical / missing project fail closed.
func TestSlackBroker_TargetAuthz_FailClosedReasons(t *testing.T) {
	t.Run("not_linked", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		logBuf := &lockedLogBuffer{}
		b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "hello",
			Type:      messages.TypeInstruction,
			Channel:   "slack",
			ThreadID:  "C_UNLINKED:1700000000.000100",
		}))

		assert.Empty(t, getPosted())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNotLinked), denied[0]["reason"])
	})

	t.Run("inactive_link", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		logBuf := &lockedLogBuffer{}
		b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		ctx := context.Background()
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID: "C_INACTIVE",
			ProjectID: "proj-a",
			Active:    false,
		}))

		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "slack",
			ThreadID: "C_INACTIVE",
		}))

		assert.Empty(t, getPosted())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyInactiveLink), denied[0]["reason"])
	})

	t.Run("lookup_error_channel_link", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		logBuf := &lockedLogBuffer{}
		b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = faultStore{
			Store:             newTestStore(t),
			errGetChannelLink: errors.New("simulated link error"),
		}

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "slack",
			ThreadID: "C_ANY:1700000000.000100",
		}))

		assert.Empty(t, getPosted())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyLookupError), denied[0]["reason"])
	})

	t.Run("lookup_error_dm_paths", func(t *testing.T) {
		ctx := context.Background()
		msg := &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "hello",
			Type:      messages.TypeInstruction,
		}

		for _, tc := range []struct {
			name  string
			store func(Store) Store
		}{
			{
				name: "user mapping lookup error",
				store: func(base Store) Store {
					return faultStore{Store: base, errGetUserMappingByEmail: errors.New("mapping error")}
				},
			},
			{
				name: "conversation context lookup error",
				store: func(base Store) Store {
					return faultStore{Store: base, errGetConversationContext: errors.New("cc error")}
				},
			},
			{
				name: "latest conversation context lookup error",
				store: func(base Store) Store {
					return faultStore{Store: base, errGetLatestConversationCtx: errors.New("latest cc error")}
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				base := newTestStore(t)
				require.NoError(t, base.CreateUserMapping(ctx, &SlackUserMapping{
					SlackUserID: "U_ALICE",
					ScionEmail:  "alice@example.com",
					LinkedAt:    time.Now().UTC(),
				}))
				b := NewBroker(nil)
				ok, reason := b.authorizeExplicitTarget(ctx, tc.store(base), "proj-a", "coder", msg, "D_ALICE")
				assert.False(t, ok)
				assert.Equal(t, plugin.DenyLookupError, reason)
			})
		}
	})

	t.Run("no_store", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		logBuf := &lockedLogBuffer{}
		b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = nil

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "slack",
			ThreadID: "C_ANY:1700000000.000100",
		}))

		assert.Empty(t, getPosted())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNoStore), denied[0]["reason"])
	})

	t.Run("no_project_direct", func(t *testing.T) {
		b := NewBroker(nil)
		store := newTestStore(t)
		ok, reason := b.authorizeExplicitTarget(context.Background(), store, "", "coder", &messages.StructuredMessage{}, "C_ANY")
		assert.False(t, ok)
		assert.Equal(t, plugin.DenyNoProject, reason)
	})

	t.Run("unknown_container_format", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		logBuf := &lockedLogBuffer{}
		b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "slack",
			ThreadID: "X_UNKNOWN_FORMAT",
		}))

		assert.Empty(t, getPosted())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNotLinked), denied[0]["reason"])
	})

	// Audit I1: non-canonical topics yield empty projectID in explicit-target
	// authorization (no_project) rather than a fallback-parsed segment, while
	// canonical project broadcasts still deliver through the implicit chain.
	t.Run("non_canonical_topic_no_project_I1", func(t *testing.T) {
		for _, topic := range []string{
			"scion.global.broadcast",
			"scion.grove.proj-a.agent.coder.messages",
			"custom.project.proj-a.messages",
		} {
			t.Run(topic, func(t *testing.T) {
				srv, getPosted := newTestSlackServer(t)
				logBuf := &lockedLogBuffer{}
				b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
				b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
				b.store = newTestStore(t)

				ctx := context.Background()
				require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
					ChannelID: "C_PROJ_A",
					ProjectID: "proj-a",
					Active:    true,
				}))
				require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
					ChannelID: "C_SCION",
					ProjectID: "scion",
					Active:    true,
				}))

				require.NoError(t, b.Publish(ctx, topic, &messages.StructuredMessage{
					Version:  messages.Version,
					Sender:   "agent:coder",
					Msg:      "non-canonical topic explicit target",
					Type:     messages.TypeInstruction,
					Channel:  "slack",
					ThreadID: "C_SCION:1700000000.000100",
				}))

				// Explicit target C_SCION must be denied with no_project.
				denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
				require.Len(t, denied, 1)
				assert.Equal(t, "", denied[0]["project_id"])
				assert.Equal(t, string(plugin.DenyNoProject), denied[0]["reason"])
				for _, p := range getPosted() {
					assert.NotEqual(t, "C_SCION", p.Channel)
				}
			})
		}

		// Control: canonical project broadcast still delivers via implicit chain.
		srv, getPosted := newTestSlackServer(t)
		b := NewBroker(nil)
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)
		require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID: "C_PROJ_A",
			ProjectID: "proj-a",
			Active:    true,
		}))
		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.broadcast", &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "project broadcast",
			Type:    messages.TypeInstruction,
		}))
		posted := getPosted()
		require.Len(t, posted, 1)
		assert.Equal(t, "C_PROJ_A", posted[0].Channel)
	})
}

// A6: Active link for topic project (including C...:ts thread and ParentRef+ts)
// delivers to the exact channel and thread_ts.
func TestSlackBroker_TargetAuthz_AllowedLinkedChannelAndThread(t *testing.T) {
	srv, getPosted := newTestSlackServer(t)
	logBuf := &lockedLogBuffer{}
	b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
	b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
	b.store = newTestStore(t)

	ctx := context.Background()
	require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
		ChannelID: "C_PROJ_A",
		ProjectID: "proj-a",
		Active:    true,
	}))

	// 1. Conversation.ExternalRef C...:ts
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "thread reply via conversation",
		Type:      messages.TypeInstruction,
		Channel:   "slack",
		Conversation: &messages.Conversation{
			Surface:     "slack",
			ExternalRef: "C_PROJ_A:1700000000.000100",
			ParentRef:   "C_PROJ_A",
		},
	}))

	// 2. Legacy ThreadID C...:ts
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "thread reply via thread_id",
		Type:      messages.TypeInstruction,
		Channel:   "slack",
		ThreadID:  "C_PROJ_A:1700000000.000200",
	}))

	// 3. Bare channel ID
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "channel message",
		Type:      messages.TypeInstruction,
		Channel:   "slack",
		Conversation: &messages.Conversation{
			Surface:     "slack",
			ExternalRef: "C_PROJ_A",
		},
	}))

	// 4. ParentRef + bare timestamp ExternalRef
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "parent ref thread reply",
		Type:      messages.TypeInstruction,
		Channel:   "slack",
		Conversation: &messages.Conversation{
			Surface:     "slack",
			ExternalRef: "1700000000.000300",
			ParentRef:   "C_PROJ_A",
		},
	}))

	posted := getPosted()
	require.Len(t, posted, 4)
	assert.Equal(t, postedSlackMessage{Channel: "C_PROJ_A", ThreadTS: "1700000000.000100"}, posted[0])
	assert.Equal(t, postedSlackMessage{Channel: "C_PROJ_A", ThreadTS: "1700000000.000200"}, posted[1])
	assert.Equal(t, postedSlackMessage{Channel: "C_PROJ_A", ThreadTS: ""}, posted[2])
	assert.Equal(t, postedSlackMessage{Channel: "C_PROJ_A", ThreadTS: "1700000000.000300"}, posted[3])
	assert.Empty(t, findLogEvent(logBuf.Entries(t), "outbound_target_denied"))
}

// A7: Recipient DM allowed via Rule 2 (slug or latest ConversationContext);
// another user's DM is denied with not_recipient_dm.
func TestSlackBroker_TargetAuthz_RecipientDM(t *testing.T) {
	srv, getPosted := newTestSlackServer(t)
	logBuf := &lockedLogBuffer{}
	b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
	b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
	b.store = newTestStore(t)

	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, b.store.CreateUserMapping(ctx, &SlackUserMapping{
		SlackUserID: "U_ALICE",
		ScionEmail:  "alice@example.com",
		LinkedAt:    now,
	}))
	require.NoError(t, b.store.CreateUserMapping(ctx, &SlackUserMapping{
		SlackUserID: "U_BOB",
		ScionEmail:  "bob@example.com",
		LinkedAt:    now,
	}))
	require.NoError(t, b.store.SetConversationContext(ctx, &ConversationContext{
		SlackUserID:   "U_ALICE",
		ProjectID:     "proj-a",
		AgentSlug:     "coder",
		LastChannelID: "D_ALICE",
		LastMessageAt: now,
	}))
	require.NoError(t, b.store.SetConversationContext(ctx, &ConversationContext{
		SlackUserID:   "U_BOB",
		ProjectID:     "proj-a",
		AgentSlug:     "coder",
		LastChannelID: "D_BOB",
		LastMessageAt: now,
	}))

	// 1. Alice's own DM thread (matching per-slug context) -> allowed.
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "reply in alice dm thread",
		Type:      messages.TypeInstruction,
		Channel:   "slack",
		Conversation: &messages.Conversation{
			Surface:     "slack",
			ExternalRef: "D_ALICE:1700000000.000100",
			ParentRef:   "D_ALICE",
		},
	}))

	// 2. Alice's own DM via latest project context (different agent slug, no per-slug row) -> allowed.
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:reviewer",
		Recipient: "user:alice@example.com",
		Msg:       "reply via latest context",
		Type:      messages.TypeInstruction,
		Channel:   "slack",
		ThreadID:  "D_ALICE",
	}))

	posted := getPosted()
	require.Len(t, posted, 2)
	assert.Equal(t, postedSlackMessage{Channel: "D_ALICE", ThreadTS: "1700000000.000100"}, posted[0])
	assert.Equal(t, postedSlackMessage{Channel: "D_ALICE", ThreadTS: ""}, posted[1])
	assert.Empty(t, findLogEvent(logBuf.Entries(t), "outbound_target_denied"))

	// 3. Message addressed to Alice targeting Bob's DM -> denied with not_recipient_dm,
	// falls through to Alice's own DM (never posts to D_BOB).
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "wrong dm target",
		Type:      messages.TypeInstruction,
		Channel:   "slack",
		ThreadID:  "D_BOB:1700000000.000999",
	}))

	posted = getPosted()
	require.Len(t, posted, 3)
	assert.Equal(t, postedSlackMessage{Channel: "D_ALICE", ThreadTS: ""}, posted[2])

	denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
	require.Len(t, denied, 1)
	assert.Equal(t, string(plugin.DenyNotRecipientDM), denied[0]["reason"])

	// 4. Non-user recipient targeting a DM channel -> denied with not_linked.
	ok, reason := b.authorizeExplicitTarget(ctx, b.store, "proj-a", "coder", &messages.StructuredMessage{
		Sender:    "agent:coder",
		Recipient: "agent:reviewer",
	}, "D_ALICE")
	assert.False(t, ok)
	assert.Equal(t, plugin.DenyNotLinked, reason)

	// 5. Unmapped user targeting a DM channel -> denied with not_recipient_dm.
	ok, reason = b.authorizeExplicitTarget(ctx, b.store, "proj-a", "coder", &messages.StructuredMessage{
		Sender:    "agent:coder",
		Recipient: "user:unknown@example.com",
	}, "D_ALICE")
	assert.False(t, ok)
	assert.Equal(t, plugin.DenyNotRecipientDM, reason)
}

// A8: slack_channel_id and slack_thread_ts in msg.Metadata are ignored;
// routing equals the no-metadata case.
func TestSlackBroker_Publish_MetadataTargetsIgnored(t *testing.T) {
	t.Run("no links yields zero posts", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		b := NewBroker(nil)
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "metadata target only",
			Type:    messages.TypeInstruction,
			Metadata: map[string]string{
				"slack_channel_id": "C_ANY",
				"slack_thread_ts":  "1700000000.000100",
			},
		}))

		assert.Empty(t, getPosted())
	})

	t.Run("metadata pointing to foreign channel falls through to project link", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		b := NewBroker(nil)
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		ctx := context.Background()
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID: "C_PROJ_A",
			ProjectID: "proj-a",
			Active:    true,
		}))
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID: "C_PROJ_B",
			ProjectID: "proj-b",
			Active:    true,
		}))

		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "metadata points to B",
			Type:    messages.TypeInstruction,
			Metadata: map[string]string{
				"slack_channel_id": "C_PROJ_B",
				"slack_thread_ts":  "1700000000.000999",
			},
		}))

		posted := getPosted()
		require.Len(t, posted, 1)
		assert.Equal(t, postedSlackMessage{Channel: "C_PROJ_A", ThreadTS: ""}, posted[0])
	})
}

// A13: Both ShowAgentToAgent and ShowStateChanges filters fail closed on
// missing link, inactive link, lookup error, or flag disabled.
func TestSlackBroker_ObserverAndStateChangeFiltersFailClosed(t *testing.T) {
	t.Run("agent_to_agent_flag_false_skips", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		b := NewBroker(nil)
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID:        "C_PROJ_A",
			ProjectID:        "proj-a",
			Active:           true,
			ShowAgentToAgent: false,
		}))

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.reviewer.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "agent:reviewer",
			Msg:       "a2a message",
			Type:      messages.TypeInstruction,
		}))

		assert.Empty(t, getPosted())
	})

	t.Run("agent_to_agent_lookup_error_skips", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		b := NewBroker(nil)
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		base := newTestStore(t)
		require.NoError(t, base.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID:        "C_PROJ_A",
			ProjectID:        "proj-a",
			Active:           true,
			ShowAgentToAgent: true,
		}))
		b.store = faultStore{Store: base, errGetChannelLink: errors.New("link lookup failed")}

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.reviewer.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "agent:reviewer",
			Msg:       "a2a lookup error",
			Type:      messages.TypeInstruction,
		}))

		assert.Empty(t, getPosted())
	})

	t.Run("agent_to_agent_flag_true_delivers", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		b := NewBroker(nil)
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID:        "C_PROJ_A",
			ProjectID:        "proj-a",
			Active:           true,
			ShowAgentToAgent: true,
		}))

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.reviewer.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "agent:reviewer",
			Msg:       "a2a allowed",
			Type:      messages.TypeInstruction,
		}))

		posted := getPosted()
		require.Len(t, posted, 1)
		assert.Equal(t, "C_PROJ_A", posted[0].Channel)
	})

	t.Run("state_change_unlinked_dm_skips", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		b := NewBroker(nil)
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		b.store = newTestStore(t)

		ctx := context.Background()
		now := time.Now().UTC()
		require.NoError(t, b.store.CreateUserMapping(ctx, &SlackUserMapping{
			SlackUserID: "U_ALICE",
			ScionEmail:  "alice@example.com",
			LinkedAt:    now,
		}))
		require.NoError(t, b.store.SetConversationContext(ctx, &ConversationContext{
			SlackUserID:   "U_ALICE",
			ProjectID:     "proj-a",
			AgentSlug:     "coder",
			LastChannelID: "D_ALICE",
			LastMessageAt: now,
		}))

		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Status:    "idle",
			Msg:       "agent idle",
			Type:      messages.TypeStateChange,
		}))

		assert.Empty(t, getPosted())
	})

	t.Run("state_change_flag_false_and_lookup_error_skip", func(t *testing.T) {
		ctx := context.Background()

		// ShowStateChanges == false -> skip.
		srv1, getPosted1 := newTestSlackServer(t)
		b1 := NewBroker(nil)
		b1.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv1.URL+"/"))
		b1.store = newTestStore(t)
		require.NoError(t, b1.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:        "C_PROJ_A",
			ProjectID:        "proj-a",
			Active:           true,
			ShowStateChanges: false,
		}))
		require.NoError(t, b1.Publish(ctx, "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Status:  "idle",
			Msg:     "agent idle",
			Type:    messages.TypeStateChange,
		}))
		assert.Empty(t, getPosted1())

		// Lookup error -> skip.
		srv2, getPosted2 := newTestSlackServer(t)
		b2 := NewBroker(nil)
		b2.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv2.URL+"/"))
		base := newTestStore(t)
		require.NoError(t, base.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:        "C_PROJ_A",
			ProjectID:        "proj-a",
			Active:           true,
			ShowStateChanges: true,
		}))
		b2.store = faultStore{Store: base, errGetChannelLink: errors.New("link lookup failed")}
		require.NoError(t, b2.Publish(ctx, "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Status:  "idle",
			Msg:     "agent idle",
			Type:    messages.TypeStateChange,
		}))
		assert.Empty(t, getPosted2())

		// ShowStateChanges == true -> deliver.
		srv3, getPosted3 := newTestSlackServer(t)
		b3 := NewBroker(nil)
		b3.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv3.URL+"/"))
		b3.store = newTestStore(t)
		require.NoError(t, b3.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:        "C_PROJ_A",
			ProjectID:        "proj-a",
			Active:           true,
			ShowStateChanges: true,
		}))
		require.NoError(t, b3.Publish(ctx, "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Status:  "idle",
			Msg:     "agent idle",
			Type:    messages.TypeStateChange,
		}))
		posted := getPosted3()
		require.Len(t, posted, 1)
		assert.Equal(t, "C_PROJ_A", posted[0].Channel)
	})
}

// A13b: Input-needed with a cross-project explicit target posts no card and
// creates no PendingAskUser row for the foreign project's channel.
func TestSlackBroker_InputNeeded_CrossProjectTargetDenied(t *testing.T) {
	t.Run("no project A link yields zero cards and zero pending asks", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		logBuf := &lockedLogBuffer{}
		b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		askStore := &recordingAskStore{Store: newTestStore(t)}
		b.store = askStore

		ctx := context.Background()
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID: "C_PROJ_B",
			ProjectID: "proj-b",
			Active:    true,
		}))

		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "Please confirm",
			Type:      messages.TypeInputNeeded,
			Channel:   "slack",
			ThreadID:  "C_PROJ_B:1700000000.000100",
		}))

		assert.Empty(t, getPosted())
		assert.Empty(t, askStore.Created())
		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyForeignProject), denied[0]["reason"])
	})

	t.Run("falls through to project A link and never creates pending ask for project B", func(t *testing.T) {
		srv, getPosted := newTestSlackServer(t)
		logBuf := &lockedLogBuffer{}
		b := NewBroker(slog.New(slog.NewJSONHandler(logBuf, nil)))
		b.client = slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
		askStore := &recordingAskStore{Store: newTestStore(t)}
		b.store = askStore

		ctx := context.Background()
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID: "C_PROJ_A",
			ProjectID: "proj-a",
			Active:    true,
		}))
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID: "C_PROJ_B",
			ProjectID: "proj-b",
			Active:    true,
		}))

		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "Please confirm",
			Type:      messages.TypeInputNeeded,
			Channel:   "slack",
			Conversation: &messages.Conversation{
				Surface:     "slack",
				ExternalRef: "C_PROJ_B:1700000000.000100",
				ParentRef:   "C_PROJ_B",
			},
		}))

		posted := getPosted()
		require.Len(t, posted, 1)
		assert.Equal(t, "C_PROJ_A", posted[0].Channel)

		created := askStore.Created()
		require.Len(t, created, 1)
		assert.Equal(t, "C_PROJ_A", created[0].ChannelID)
		assert.Equal(t, "proj-a", created[0].ProjectID)

		denied := findLogEvent(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyForeignProject), denied[0]["reason"])
	})
}
