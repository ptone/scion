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

package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

type authzLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *authzLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *authzLogBuffer) Entries(t *testing.T) []map[string]any {
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

func findAuthzLogEvents(entries []map[string]any, event string) []map[string]any {
	var matched []map[string]any
	for _, e := range entries {
		if e["event"] == event {
			matched = append(matched, e)
		}
	}
	return matched
}

type failingLinkStore struct {
	Store
}

func (failingLinkStore) GetChannelLink(context.Context, string) (*ChannelLink, error) {
	return nil, errors.New("simulated store error")
}

type recordingAskStore struct {
	Store
	mu      sync.Mutex
	created []*PendingAskUser
}

func (s *recordingAskStore) CreatePendingAskUser(ctx context.Context, req *PendingAskUser) error {
	s.mu.Lock()
	s.created = append(s.created, req)
	s.mu.Unlock()
	return s.Store.CreatePendingAskUser(ctx, req)
}

func (s *recordingAskStore) createdAsks() []*PendingAskUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*PendingAskUser(nil), s.created...)
}

// channelGetFailTransport fails GET /channels/<failID> while recording POSTs.
type channelGetFailTransport struct {
	mu     sync.Mutex
	failID string
	posts  []string
}

func (rt *channelGetFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/channels/"+rt.failID) {
		return nil, errors.New("simulated discord REST error")
	}
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/messages") {
		i := strings.Index(req.URL.Path, "/channels/")
		if i >= 0 {
			chID := strings.TrimSuffix(req.URL.Path[i+len("/channels/"):], "/messages")
			rt.mu.Lock()
			rt.posts = append(rt.posts, chID)
			rt.mu.Unlock()
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"msg-1"}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

func (rt *channelGetFailTransport) postedChannels() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.posts...)
}

// A4: Cross-project explicit targets (direct link and thread of foreign parent)
// are denied with foreign_project and fall through to the topic project's routing.
func TestDiscordBroker_TargetAuthz_CrossProjectDeniedAndFallsThrough(t *testing.T) {
	const (
		projAChannel   = "110000000000000001"
		projBChannel   = "110000000000000002"
		projBThread    = "110000000000000003"
		canonicalTopic = "scion.project.proj-a.user.u1.messages"
	)

	t.Cleanup(func() {
		threadParentsMu.Lock()
		delete(threadParents, projBThread)
		threadParentsMu.Unlock()
	})

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
					Msg:       "cross-project thread id",
					Type:      messages.TypeInstruction,
					Channel:   "discord",
					ThreadID:  projBChannel,
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
					Msg:       "cross-project conversation ref",
					Type:      messages.TypeInstruction,
					Channel:   "discord",
					Conversation: &messages.Conversation{
						Surface:     "discord",
						ExternalRef: projBChannel,
					},
				}
			},
		},
		{
			name:    "thread whose parent is linked to foreign project",
			wantSrc: string(plugin.TargetSourceThreadID),
			buildMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice@example.com",
					Msg:       "cross-project thread parent",
					Type:      messages.TypeInstruction,
					Channel:   "discord",
					ThreadID:  projBThread,
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, rt := newRecordingSession(t, []*discordgo.Channel{
				{ID: projAChannel, Type: discordgo.ChannelTypeGuildText},
				{ID: projBChannel, Type: discordgo.ChannelTypeGuildText},
				{ID: projBThread, Type: discordgo.ChannelTypeGuildPublicThread, ParentID: projBChannel},
			})

			logBuf := &authzLogBuffer{}
			b := testBroker(session)
			b.log = slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			b.store = newTestBrokerStore(t)

			ctx := context.Background()
			require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
				ChannelID:   projAChannel,
				GuildID:     testGuildID,
				ProjectID:   "proj-a",
				ProjectSlug: "proj-a",
				Active:      true,
				LinkedAt:    time.Now(),
			}))
			require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
				ChannelID:   projBChannel,
				GuildID:     testGuildID,
				ProjectID:   "proj-b",
				ProjectSlug: "proj-b",
				Active:      true,
				LinkedAt:    time.Now(),
			}))

			require.NoError(t, b.Publish(ctx, canonicalTopic, tc.buildMsg()))

			// Must fall through to proj-a and never deliver to proj-b.
			assert.Equal(t, []string{projAChannel}, sentChannelIDs(rt))

			denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
			require.Len(t, denied, 1)
			assert.Equal(t, "discord", denied[0]["surface"])
			assert.Equal(t, "proj-a", denied[0]["project_id"])
			assert.Equal(t, tc.wantSrc, denied[0]["source"])
			assert.Equal(t, string(plugin.DenyForeignProject), denied[0]["reason"])
		})
	}
}

// A5 & I1: Unlinked channel, inactive link, store lookup error, Discord API
// thread-parent lookup error, nil store, and non-canonical topics fail closed.
func TestDiscordBroker_TargetAuthz_FailClosedReasons(t *testing.T) {
	t.Run("not_linked", func(t *testing.T) {
		const unlinkedCh = "120000000000000001"
		t.Cleanup(func() {
			threadParentsMu.Lock()
			delete(threadParents, unlinkedCh)
			threadParentsMu.Unlock()
		})

		session, rt := newRecordingSession(t, []*discordgo.Channel{
			{ID: unlinkedCh, Type: discordgo.ChannelTypeGuildText},
		})
		logBuf := &authzLogBuffer{}
		b := testBroker(session)
		b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
		b.store = newTestBrokerStore(t)

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "unlinked channel",
			Type:     messages.TypeInstruction,
			Channel:  "discord",
			ThreadID: unlinkedCh,
		}))

		assert.Empty(t, sentChannelIDs(rt))
		denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNotLinked), denied[0]["reason"])
	})

	t.Run("inactive_link", func(t *testing.T) {
		const inactiveCh = "120000000000000002"
		t.Cleanup(func() {
			threadParentsMu.Lock()
			delete(threadParents, inactiveCh)
			threadParentsMu.Unlock()
		})

		session, rt := newRecordingSession(t, []*discordgo.Channel{
			{ID: inactiveCh, Type: discordgo.ChannelTypeGuildText},
		})
		logBuf := &authzLogBuffer{}
		b := testBroker(session)
		b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
		b.store = newTestBrokerStore(t)
		require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID:   inactiveCh,
			GuildID:     testGuildID,
			ProjectID:   "proj-a",
			ProjectSlug: "proj-a",
			Active:      false,
			LinkedAt:    time.Now(),
		}))

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "inactive link",
			Type:     messages.TypeInstruction,
			Channel:  "discord",
			ThreadID: inactiveCh,
		}))

		assert.Empty(t, sentChannelIDs(rt))
		denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyInactiveLink), denied[0]["reason"])
	})

	t.Run("store_lookup_error", func(t *testing.T) {
		const targetCh = "120000000000000003"
		session, rt := newRecordingSession(t, nil)
		logBuf := &authzLogBuffer{}
		b := testBroker(session)
		b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
		b.store = failingLinkStore{Store: newTestBrokerStore(t)}

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "store error",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
			ThreadID:  targetCh,
		}))

		assert.Empty(t, sentChannelIDs(rt))
		denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyLookupError), denied[0]["reason"])
	})

	t.Run("thread_parent_lookup_error_never_falls_through_to_DM", func(t *testing.T) {
		const targetCh = "120000000000000004"
		t.Cleanup(func() {
			threadParentsMu.Lock()
			delete(threadParents, targetCh)
			threadParentsMu.Unlock()
		})

		s, err := discordgo.New("Bot test-token")
		require.NoError(t, err)
		rt := &channelGetFailTransport{failID: targetCh}
		s.Client = &http.Client{Transport: rt}
		s.MaxRestRetries = 0
		s.ShouldRetryOnRateLimit = false

		logBuf := &authzLogBuffer{}
		b := testBroker(s)
		b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
		b.store = newTestBrokerStore(t)

		ctx := context.Background()
		require.NoError(t, b.store.CreateUserMapping(ctx, &DiscordUserMapping{
			DiscordUserID:   "discord-u1",
			DiscordUsername: "alice",
			ScionEmail:      "alice@example.com",
			ScionUserID:     "u1",
			LinkedAt:        time.Now(),
		}))
		// Even if createDMChannel would return targetCh, a Discord API failure
		// resolving whether targetCh is a thread must deny with lookup_error
		// rather than treating targetCh as unlinked and trying Rule 2 DM.
		b.createDMChannel = func(string) (string, error) {
			return targetCh, nil
		}

		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "thread parent REST error",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
			ThreadID:  targetCh,
		}))

		assert.Empty(t, rt.postedChannels())
		denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyLookupError), denied[0]["reason"])
	})

	t.Run("no_store", func(t *testing.T) {
		const targetCh = "120000000000000005"
		session, rt := newRecordingSession(t, nil)
		logBuf := &authzLogBuffer{}
		b := testBroker(session)
		b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
		b.store = nil

		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.user.u1.messages", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "nil store",
			Type:     messages.TypeInstruction,
			Channel:  "discord",
			ThreadID: targetCh,
		}))

		assert.Empty(t, sentChannelIDs(rt))
		denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNoStore), denied[0]["reason"])
	})

	t.Run("no_project_non_canonical_topic", func(t *testing.T) {
		const targetCh = "120000000000000006"
		session, rt := newRecordingSession(t, []*discordgo.Channel{
			{ID: targetCh, Type: discordgo.ChannelTypeGuildText},
		})
		logBuf := &authzLogBuffer{}
		b := testBroker(session)
		b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
		b.store = newTestBrokerStore(t)

		// Even if a link exists under the fallback topic name, non-canonical
		// topics must yield projectID="" in the authorization path (DenyNoProject).
		require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID:   targetCh,
			GuildID:     testGuildID,
			ProjectID:   "scion.global.broadcast",
			ProjectSlug: "scion.global.broadcast",
			Active:      true,
			LinkedAt:    time.Now(),
		}))
		// Implicit fallback for scion.global.broadcast would still broadcast to
		// links matching parseTopicComponents if present, so link a separate
		// channel under "scion" to verify explicit targetCh is denied.
		require.NoError(t, b.store.DeleteChannelLink(context.Background(), targetCh))
		require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID:   targetCh,
			GuildID:     testGuildID,
			ProjectID:   "scion",
			ProjectSlug: "scion",
			Active:      true,
			LinkedAt:    time.Now(),
		}))

		require.NoError(t, b.Publish(context.Background(), "scion.global.broadcast", &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "agent:coder",
			Msg:      "non-canonical topic",
			Type:     messages.TypeInstruction,
			Channel:  "discord",
			ThreadID: targetCh,
		}))

		assert.Empty(t, sentChannelIDs(rt))
		denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, "", denied[0]["project_id"])
		assert.Equal(t, string(plugin.DenyNoProject), denied[0]["reason"])
	})
}

// A6: Active link for topic project (direct channel and thread whose parent is
// linked to the topic project) delivers to the exact container and thread.
func TestDiscordBroker_TargetAuthz_Allowed(t *testing.T) {
	const (
		parentCh = "130000000000000001"
		threadCh = "130000000000000002"
		topic    = "scion.project.proj-a.user.u1.messages"
	)
	t.Cleanup(func() {
		threadParentsMu.Lock()
		delete(threadParents, threadCh)
		threadParentsMu.Unlock()
	})

	session, rt := newRecordingSession(t, []*discordgo.Channel{
		{ID: parentCh, Type: discordgo.ChannelTypeGuildText},
		{ID: threadCh, Type: discordgo.ChannelTypeGuildPublicThread, ParentID: parentCh},
	})
	logBuf := &authzLogBuffer{}
	b := testBroker(session)
	b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
	b.store = newTestBrokerStore(t)

	require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
		ChannelID:   parentCh,
		GuildID:     testGuildID,
		ProjectID:   "proj-a",
		ProjectSlug: "proj-a",
		Active:      true,
		LinkedAt:    time.Now(),
	}))

	// 1. Direct linked channel.
	require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
		Version:  messages.Version,
		Sender:   "agent:coder",
		Msg:      "direct channel reply",
		Type:     messages.TypeInstruction,
		Channel:  "discord",
		ThreadID: parentCh,
	}))

	// 2. Thread whose parent is linked to proj-a.
	require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
		Version: messages.Version,
		Sender:  "agent:coder",
		Msg:     "thread reply",
		Type:    messages.TypeInstruction,
		Channel: "discord",
		Conversation: &messages.Conversation{
			Surface:     "discord",
			ExternalRef: threadCh,
			ParentRef:   parentCh,
		},
	}))

	assert.Equal(t, []string{parentCh, threadCh}, sentChannelIDs(rt))
	assert.Empty(t, findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied"))
}

// A7: Recipient's own DM or ConversationContext channel is allowed; another
// user's DM channel is denied with not_recipient_dm.
func TestDiscordBroker_TargetAuthz_RecipientDM(t *testing.T) {
	const (
		aliceDM       = "140000000000000001"
		bobDM         = "140000000000000002"
		aliceCtxCh    = "140000000000000003"
		aliceLatestCh = "140000000000000004"
		projAChannel  = "140000000000000005"
		topic         = "scion.project.proj-a.user.u1.messages"
	)
	t.Cleanup(func() {
		threadParentsMu.Lock()
		delete(threadParents, aliceDM)
		delete(threadParents, bobDM)
		delete(threadParents, aliceCtxCh)
		delete(threadParents, aliceLatestCh)
		threadParentsMu.Unlock()
	})

	newFixture := func(t *testing.T) (*DiscordBroker, *recordingTransport, *authzLogBuffer) {
		t.Helper()
		session, rt := newRecordingSession(t, []*discordgo.Channel{
			{ID: aliceDM, Type: discordgo.ChannelTypeDM},
			{ID: bobDM, Type: discordgo.ChannelTypeDM},
			{ID: aliceCtxCh, Type: discordgo.ChannelTypeDM},
			{ID: aliceLatestCh, Type: discordgo.ChannelTypeDM},
			{ID: projAChannel, Type: discordgo.ChannelTypeGuildText},
		})
		logBuf := &authzLogBuffer{}
		b := testBroker(session)
		b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
		b.store = newTestBrokerStore(t)

		ctx := context.Background()
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:   projAChannel,
			GuildID:     testGuildID,
			ProjectID:   "proj-a",
			ProjectSlug: "proj-a",
			Active:      true,
			LinkedAt:    time.Now(),
		}))
		require.NoError(t, b.store.CreateUserMapping(ctx, &DiscordUserMapping{
			DiscordUserID:   "discord-alice",
			DiscordUsername: "alice",
			ScionEmail:      "alice@example.com",
			ScionUserID:     "u-alice",
			LinkedAt:        time.Now(),
		}))
		require.NoError(t, b.store.CreateUserMapping(ctx, &DiscordUserMapping{
			DiscordUserID:   "discord-bob",
			DiscordUsername: "bob",
			ScionEmail:      "bob@example.com",
			ScionUserID:     "u-bob",
			LinkedAt:        time.Now(),
		}))
		b.createDMChannel = func(discordUserID string) (string, error) {
			switch discordUserID {
			case "discord-alice":
				return aliceDM, nil
			case "discord-bob":
				return bobDM, nil
			default:
				return "", errors.New("unknown user")
			}
		}
		return b, rt, logBuf
	}

	t.Run("recipient own DM channel allowed", func(t *testing.T) {
		b, rt, logBuf := newFixture(t)
		require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "reply to alice DM",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
			ThreadID:  aliceDM,
		}))
		assert.Equal(t, []string{aliceDM}, sentChannelIDs(rt))
		assert.Empty(t, findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied"))
	})

	t.Run("recipient conversation context exact agent allowed", func(t *testing.T) {
		b, rt, logBuf := newFixture(t)
		require.NoError(t, b.store.SetConversationContext(context.Background(), &ConversationContext{
			DiscordUserID: "discord-alice",
			ProjectID:     "proj-a",
			AgentSlug:     "coder",
			LastChannelID: aliceCtxCh,
			LastMessageAt: time.Now(),
		}))
		require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "reply to alice ctx",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
			ThreadID:  aliceCtxCh,
		}))
		assert.Equal(t, []string{aliceCtxCh}, sentChannelIDs(rt))
		assert.Empty(t, findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied"))
	})

	t.Run("recipient latest conversation context allowed", func(t *testing.T) {
		b, rt, logBuf := newFixture(t)
		require.NoError(t, b.store.SetConversationContext(context.Background(), &ConversationContext{
			DiscordUserID: "discord-alice",
			ProjectID:     "proj-a",
			AgentSlug:     "other-agent",
			LastChannelID: aliceLatestCh,
			LastMessageAt: time.Now(),
		}))
		require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:reviewer",
			Recipient: "user:alice@example.com",
			Msg:       "reply to alice latest ctx",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
			ThreadID:  aliceLatestCh,
		}))
		assert.Equal(t, []string{aliceLatestCh}, sentChannelIDs(rt))
		assert.Empty(t, findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied"))
	})

	t.Run("another user DM channel denied", func(t *testing.T) {
		b, rt, logBuf := newFixture(t)
		require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "targeted at bob DM",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
			ThreadID:  bobDM,
		}))
		// Never sent to Bob's DM; falls through to proj-a's linked channel.
		assert.Equal(t, []string{projAChannel}, sentChannelIDs(rt))
		denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
		require.Len(t, denied, 1)
		assert.Equal(t, string(plugin.DenyNotRecipientDM), denied[0]["reason"])
	})
}

// A8: discord_channel_id metadata is ignored; routing matches the no-metadata case.
func TestDiscordBroker_TargetAuthz_MetadataIgnored(t *testing.T) {
	const (
		projAChannel = "150000000000000001"
		otherChannel = "150000000000000002"
		topic        = "scion.project.proj-a.agent.coder.messages"
	)

	session, rt := newRecordingSession(t, []*discordgo.Channel{
		{ID: projAChannel, Type: discordgo.ChannelTypeGuildText},
		{ID: otherChannel, Type: discordgo.ChannelTypeGuildText},
	})
	b := testBroker(session)
	b.log = discardLogger()
	b.store = newTestBrokerStore(t)

	require.NoError(t, b.store.CreateChannelLink(context.Background(), &ChannelLink{
		ChannelID:   projAChannel,
		GuildID:     testGuildID,
		ProjectID:   "proj-a",
		ProjectSlug: "proj-a",
		Active:      true,
		LinkedAt:    time.Now(),
	}))

	require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
		Version:  messages.Version,
		Sender:   "agent:coder",
		Msg:      "with metadata channel id",
		Type:     messages.TypeInstruction,
		Channel:  "discord",
		Metadata: map[string]string{"discord_channel_id": otherChannel},
	}))

	assert.Equal(t, []string{projAChannel}, sentChannelIDs(rt),
		"discord_channel_id metadata must be ignored; routing must match no-metadata broadcast")
}

// A13: Observer (ShowAgentToAgent) and state-change (ShowStateChanges) filters
// fail closed when a target has no link, has the toggle disabled, or encounters
// a store lookup error.
func TestDiscordBroker_ObserverFilterFailsClosed(t *testing.T) {
	const (
		unlinkedCtxCh = "160000000000000001"
		linkedOffCh   = "160000000000000002"
		linkedOnCh    = "160000000000000003"
		topic         = "scion.project.proj-a.agent.reviewer.messages"
	)
	t.Cleanup(func() {
		threadParentsMu.Lock()
		delete(threadParents, unlinkedCtxCh)
		delete(threadParents, linkedOffCh)
		delete(threadParents, linkedOnCh)
		threadParentsMu.Unlock()
	})

	t.Run("agent-to-agent filtered on ShowAgentToAgent=false and delivered on true", func(t *testing.T) {
		session, rt := newRecordingSession(t, []*discordgo.Channel{
			{ID: linkedOffCh, Type: discordgo.ChannelTypeGuildText},
			{ID: linkedOnCh, Type: discordgo.ChannelTypeGuildText},
		})
		b := testBroker(session)
		b.log = discardLogger()
		b.store = newTestBrokerStore(t)

		ctx := context.Background()
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:        linkedOffCh,
			GuildID:          testGuildID,
			ProjectID:        "proj-a",
			ProjectSlug:      "proj-a",
			Active:           true,
			ShowAgentToAgent: false,
			LinkedAt:         time.Now(),
		}))
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:        linkedOnCh,
			GuildID:          testGuildID,
			ProjectID:        "proj-a",
			ProjectSlug:      "proj-a",
			Active:           true,
			ShowAgentToAgent: true,
			LinkedAt:         time.Now(),
		}))

		require.NoError(t, b.Publish(ctx, topic, &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "agent:reviewer",
			Msg:       "observer copy",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
		}))

		assert.Equal(t, []string{linkedOnCh}, sentChannelIDs(rt))
	})

	t.Run("agent-to-agent fails closed on store lookup error", func(t *testing.T) {
		session, rt := newRecordingSession(t, []*discordgo.Channel{
			{ID: linkedOnCh, Type: discordgo.ChannelTypeGuildText},
		})
		baseStore := newTestBrokerStore(t)
		require.NoError(t, baseStore.CreateChannelLink(context.Background(), &ChannelLink{
			ChannelID:        linkedOnCh,
			GuildID:          testGuildID,
			ProjectID:        "proj-a",
			ProjectSlug:      "proj-a",
			Active:           true,
			ShowAgentToAgent: true,
			LinkedAt:         time.Now(),
		}))

		b := testBroker(session)
		b.log = discardLogger()
		b.store = failingLinkStore{Store: baseStore}

		require.NoError(t, b.Publish(context.Background(), topic, &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "agent:reviewer",
			Msg:       "observer copy with store error",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
		}))

		assert.Empty(t, sentChannelIDs(rt))
	})

	t.Run("state-change filtered on ShowStateChanges=false and delivered on true", func(t *testing.T) {
		session, rt := newRecordingSession(t, []*discordgo.Channel{
			{ID: linkedOffCh, Type: discordgo.ChannelTypeGuildText},
			{ID: linkedOnCh, Type: discordgo.ChannelTypeGuildText},
		})
		b := testBroker(session)
		b.log = discardLogger()
		b.store = newTestBrokerStore(t)

		ctx := context.Background()
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:        linkedOffCh,
			GuildID:          testGuildID,
			ProjectID:        "proj-a",
			ProjectSlug:      "proj-a",
			Active:           true,
			ShowStateChanges: false,
			LinkedAt:         time.Now(),
		}))
		require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
			ChannelID:        linkedOnCh,
			GuildID:          testGuildID,
			ProjectID:        "proj-a",
			ProjectSlug:      "proj-a",
			Active:           true,
			ShowStateChanges: true,
			LinkedAt:         time.Now(),
		}))

		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "agent completed",
			Type:    messages.TypeStateChange,
			Channel: "discord",
		}))

		assert.Equal(t, []string{linkedOnCh}, sentChannelIDs(rt))
	})
}

// A13b: input_needed with a cross-project explicit target sends no card and
// creates no PendingAskUser row for the foreign project's channel.
func TestDiscordBroker_InputNeeded_CrossProjectTargetDenied(t *testing.T) {
	const (
		projBChannel = "170000000000000002"
		topic        = "scion.project.proj-a.user.u1.messages"
	)

	session, err := discordgo.New("Bot test-token")
	require.NoError(t, err)
	rt := &askUserTransport{}
	session.Client = &http.Client{Transport: rt}
	session.MaxRestRetries = 0
	session.ShouldRetryOnRateLimit = false
	_ = session.State.GuildAdd(&discordgo.Guild{ID: testGuildID})
	_ = session.State.ChannelAdd(&discordgo.Channel{ID: projBChannel, GuildID: testGuildID, Type: discordgo.ChannelTypeGuildText})

	logBuf := &authzLogBuffer{}
	askStore := &recordingAskStore{Store: newTestBrokerStore(t)}
	b := testBroker(session)
	b.log = slog.New(slog.NewJSONHandler(logBuf, nil))
	b.store = askStore

	ctx := context.Background()
	require.NoError(t, b.store.CreateChannelLink(ctx, &ChannelLink{
		ChannelID:   projBChannel,
		GuildID:     testGuildID,
		ProjectID:   "proj-b",
		ProjectSlug: "proj-b",
		Active:      true,
		LinkedAt:    time.Now(),
	}))

	require.NoError(t, b.Publish(ctx, topic, &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice@example.com",
		Msg:       "Approve action?",
		Type:      messages.TypeInputNeeded,
		Channel:   "discord",
		ThreadID:  projBChannel,
		Metadata:  map[string]string{"choices": `["yes","no"]`},
	}))

	assert.Empty(t, rt.postedCustomIDs(t, projBChannel), "no input-needed card may be posted to foreign project channel")
	assert.Empty(t, askStore.createdAsks(), "no PendingAskUser row may be created for foreign project channel")

	denied := findAuthzLogEvents(logBuf.Entries(t), "outbound_target_denied")
	require.Len(t, denied, 1)
	assert.Equal(t, string(plugin.DenyForeignProject), denied[0]["reason"])
}
