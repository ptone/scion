package discord

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestStructuredMessage() *messages.StructuredMessage {
	return &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Channel:   "discord",
		Sender:    "user:alice@example.com",
		Recipient: "agent:coder",
		Msg:       "hello",
		Type:      messages.TypeInstruction,
	}
}

func TestParseHubError(t *testing.T) {
	t.Run("valid error response", func(t *testing.T) {
		body := `{"error":{"code":"agent_not_found","message":"Agent \"coder\" not found in project"}}`
		resp := &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		he := parseHubError(resp)
		require.NotNil(t, he)
		assert.Equal(t, 404, he.StatusCode)
		assert.Equal(t, "agent_not_found", he.Code)
		assert.Equal(t, `Agent "coder" not found in project`, he.Message)
	})

	t.Run("empty body", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(strings.NewReader("")),
		}
		he := parseHubError(resp)
		assert.Equal(t, "unknown", he.Code)
		assert.Equal(t, "Internal Server Error", he.Message)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 403,
			Body:       io.NopCloser(strings.NewReader("not json")),
		}
		he := parseHubError(resp)
		assert.Equal(t, "unknown", he.Code)
		assert.Equal(t, "Forbidden", he.Message)
	})
}

func TestHubError_UserFacingMessage(t *testing.T) {
	tests := []struct {
		name     string
		err      hubError
		contains string
	}{
		{
			name:     "agent not found",
			err:      hubError{StatusCode: 404, Code: "agent_not_found", Message: "Agent not found"},
			contains: "Target agent not found",
		},
		{
			name:     "forbidden",
			err:      hubError{StatusCode: 403, Code: "forbidden", Message: "no permission"},
			contains: "permission",
		},
		{
			name:     "unauthorized",
			err:      hubError{StatusCode: 401, Code: "unauthorized", Message: "bad auth"},
			contains: "Authentication error",
		},
		{
			name:     "broker auth failed",
			err:      hubError{StatusCode: 401, Code: "broker_auth_failed", Message: "bad hmac"},
			contains: "Authentication error",
		},
		{
			name:     "server error",
			err:      hubError{StatusCode: 502, Code: "runtime_error", Message: "agent unreachable"},
			contains: "try again or contact",
		},
		{
			name:     "other client error",
			err:      hubError{StatusCode: 400, Code: "invalid_request", Message: "bad topic format"},
			contains: "try again or contact",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.userFacingMessage()
			assert.Contains(t, msg, tt.contains)
		})
	}
}

func TestDeliverInbound_ReturnsHubError(t *testing.T) {
	t.Run("404 agent not found", func(t *testing.T) {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"code":    "agent_not_found",
					"message": "Agent not found",
				},
			})
		}))
		defer hub.Close()

		b := &DiscordBroker{
			log:        discardLogger(),
			hubURL:     hub.URL,
			httpClient: http.DefaultClient,
		}

		he := b.deliverInbound("scion.project.p1.agent.coder.messages", newTestStructuredMessage())
		require.NotNil(t, he)
		assert.Equal(t, 404, he.StatusCode)
		assert.Equal(t, "agent_not_found", he.Code)
	})

	t.Run("403 forbidden", func(t *testing.T) {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"code":    "forbidden",
					"message": "user does not have permission",
				},
			})
		}))
		defer hub.Close()

		b := &DiscordBroker{
			log:        discardLogger(),
			hubURL:     hub.URL,
			httpClient: http.DefaultClient,
		}

		he := b.deliverInbound("scion.project.p1.agent.coder.messages", newTestStructuredMessage())
		require.NotNil(t, he)
		assert.Equal(t, 403, he.StatusCode)
		assert.Equal(t, "forbidden", he.Code)
	})

	t.Run("200 success returns nil", func(t *testing.T) {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"delivered": true,
				"agentId":   "agent-123",
			})
		}))
		defer hub.Close()

		b := &DiscordBroker{
			log:        discardLogger(),
			hubURL:     hub.URL,
			httpClient: http.DefaultClient,
		}

		he := b.deliverInbound("scion.project.p1.agent.coder.messages", newTestStructuredMessage())
		assert.Nil(t, he)
	})

	t.Run("in-process handler returns nil", func(t *testing.T) {
		b := &DiscordBroker{
			log: discardLogger(),
			InboundHandler: func(_ string, _ *messages.StructuredMessage) {
			},
		}

		he := b.deliverInbound("test.topic", newTestStructuredMessage())
		assert.Nil(t, he)
	})
}

const testGuildID = "guild1"

func stubSession(channels []*discordgo.Channel) *discordgo.Session {
	s := &discordgo.Session{
		State: discordgo.NewState(),
	}
	_ = s.State.GuildAdd(&discordgo.Guild{ID: testGuildID})
	for _, ch := range channels {
		if ch.GuildID == "" {
			ch.GuildID = testGuildID
		}
		_ = s.State.ChannelAdd(ch)
	}
	return s
}

func testBroker(session *discordgo.Session) *DiscordBroker {
	return &DiscordBroker{
		session:       session,
		log:           slog.Default(),
		sentIDs:       make(map[string]time.Time),
		threadParents: make(map[string]string),
	}
}

func TestIsForumChannel(t *testing.T) {
	tests := []struct {
		name      string
		chType    discordgo.ChannelType
		wantForum bool
	}{
		{"text channel", discordgo.ChannelTypeGuildText, false},
		{"DM channel", discordgo.ChannelTypeDM, false},
		{"voice channel", discordgo.ChannelTypeGuildVoice, false},
		{"category", discordgo.ChannelTypeGuildCategory, false},
		{"news channel", discordgo.ChannelTypeGuildNews, false},
		{"public thread", discordgo.ChannelTypeGuildPublicThread, false},
		{"private thread", discordgo.ChannelTypeGuildPrivateThread, false},
		{"news thread", discordgo.ChannelTypeGuildNewsThread, false},
		{"stage voice", discordgo.ChannelTypeGuildStageVoice, false},
		{"forum channel", discordgo.ChannelTypeGuildForum, true},
		{"media channel", discordgo.ChannelTypeGuildMedia, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := stubSession([]*discordgo.Channel{
				{ID: "ch1", Type: tt.chType},
			})
			b := &DiscordBroker{session: session}
			assert.Equal(t, tt.wantForum, b.isForumChannel("ch1"))
		})
	}
}

func TestIsForumChannel_NilSession(t *testing.T) {
	b := &DiscordBroker{}
	assert.False(t, b.isForumChannel("ch1"))
}

func TestForumGuardCondition(t *testing.T) {
	session := stubSession([]*discordgo.Channel{
		{ID: "forum1", Type: discordgo.ChannelTypeGuildForum},
		{ID: "media1", Type: discordgo.ChannelTypeGuildMedia},
		{ID: "text1", Type: discordgo.ChannelTypeGuildText},
	})
	b := &DiscordBroker{session: session}

	tests := []struct {
		name      string
		channelID string
		threadID  string
		wantBlock bool
	}{
		{"forum without threadID", "forum1", "", true},
		{"forum with threadID", "forum1", "thread123", false},
		{"media without threadID", "media1", "", true},
		{"media with threadID", "media1", "thread456", false},
		{"text without threadID", "text1", "", false},
		{"text with threadID", "text1", "thread789", false},
		{"unknown channel without threadID", "unknown", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked := tt.threadID == "" && b.isForumChannel(tt.channelID)
			assert.Equal(t, tt.wantBlock, blocked)
		})
	}
}

func TestPublish_ForumChannelWithoutThreadID_ReturnsError(t *testing.T) {
	session := stubSession([]*discordgo.Channel{
		{ID: "forum123", Type: discordgo.ChannelTypeGuildForum},
	})
	b := testBroker(session)

	msg := &messages.StructuredMessage{
		Version:  messages.Version,
		Channel:  "discord",
		Sender:   "agent:test",
		Msg:      "hello",
		Type:     messages.TypeInstruction,
		Metadata: map[string]string{"discord_channel_id": "forum123"},
	}

	err := b.Publish(context.Background(), "test-topic", msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forum/media channel")
	assert.Contains(t, err.Error(), "thread ID is required")
}

func TestPublish_MediaChannelWithoutThreadID_ReturnsError(t *testing.T) {
	session := stubSession([]*discordgo.Channel{
		{ID: "media123", Type: discordgo.ChannelTypeGuildMedia},
	})
	b := testBroker(session)

	msg := &messages.StructuredMessage{
		Version:  messages.Version,
		Channel:  "discord",
		Sender:   "agent:test",
		Msg:      "hello",
		Type:     messages.TypeInstruction,
		Metadata: map[string]string{"discord_channel_id": "media123"},
	}

	err := b.Publish(context.Background(), "test-topic", msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forum/media channel")
}

// --- Thread inheritance tests ---

func TestResolveChannelLink_ThreadInheritsParent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create a channel link for the parent channel with a default agent.
	require.NoError(t, store.CreateChannelLink(ctx, &ChannelLink{
		ChannelID:    "parent-ch-1",
		GuildID:      testGuildID,
		ProjectID:    "proj-1",
		ProjectSlug:  "my-project",
		DefaultAgent: "coder",
		LinkedAt:     time.Now().UTC(),
		Active:       true,
	}))

	// Set up a session with a thread that has parent-ch-1 as its parent.
	session := stubSession([]*discordgo.Channel{
		{ID: "parent-ch-1", Type: discordgo.ChannelTypeGuildText},
		{ID: "thread-1", Type: discordgo.ChannelTypeGuildPublicThread, ParentID: "parent-ch-1"},
	})

	// Clear the global threadParents cache to test fresh resolution.
	threadParentsMu.Lock()
	delete(threadParents, "thread-1")
	threadParentsMu.Unlock()

	// Resolve channel link for the thread — should fall through to parent.
	link, err := resolveChannelLink(ctx, session, store, "thread-1")
	require.NoError(t, err)
	require.NotNil(t, link, "thread should inherit parent channel link")
	assert.Equal(t, "parent-ch-1", link.ChannelID)
	assert.Equal(t, "proj-1", link.ProjectID)
	assert.Equal(t, "coder", link.DefaultAgent)
}

func TestResolveChannelLink_ThreadWithOwnLink(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Parent channel has a default agent.
	require.NoError(t, store.CreateChannelLink(ctx, &ChannelLink{
		ChannelID:    "parent-ch-2",
		GuildID:      testGuildID,
		ProjectID:    "proj-1",
		DefaultAgent: "coder",
		LinkedAt:     time.Now().UTC(),
		Active:       true,
	}))

	// Thread has its own explicit link with a different agent.
	require.NoError(t, store.CreateChannelLink(ctx, &ChannelLink{
		ChannelID:    "thread-2",
		GuildID:      testGuildID,
		ProjectID:    "proj-1",
		DefaultAgent: "reviewer",
		LinkedAt:     time.Now().UTC(),
		Active:       true,
	}))

	session := stubSession([]*discordgo.Channel{
		{ID: "parent-ch-2", Type: discordgo.ChannelTypeGuildText},
		{ID: "thread-2", Type: discordgo.ChannelTypeGuildPublicThread, ParentID: "parent-ch-2"},
	})

	// Thread has its own link — should use that, not the parent's.
	link, err := resolveChannelLink(ctx, session, store, "thread-2")
	require.NoError(t, err)
	require.NotNil(t, link)
	assert.Equal(t, "thread-2", link.ChannelID)
	assert.Equal(t, "reviewer", link.DefaultAgent)
}

func TestResolveChannelLink_ChannelNoConfig(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	session := stubSession([]*discordgo.Channel{
		{ID: "unlinked-ch", Type: discordgo.ChannelTypeGuildText},
	})

	// Channel with no link should return nil.
	link, err := resolveChannelLink(ctx, session, store, "unlinked-ch")
	require.NoError(t, err)
	assert.Nil(t, link)
}

func TestResolveChannelLink_PrivateThread(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreateChannelLink(ctx, &ChannelLink{
		ChannelID:    "parent-ch-3",
		GuildID:      testGuildID,
		ProjectID:    "proj-1",
		DefaultAgent: "ops",
		LinkedAt:     time.Now().UTC(),
		Active:       true,
	}))

	session := stubSession([]*discordgo.Channel{
		{ID: "parent-ch-3", Type: discordgo.ChannelTypeGuildText},
		{ID: "priv-thread-1", Type: discordgo.ChannelTypeGuildPrivateThread, ParentID: "parent-ch-3"},
	})

	threadParentsMu.Lock()
	delete(threadParents, "priv-thread-1")
	threadParentsMu.Unlock()

	link, err := resolveChannelLink(ctx, session, store, "priv-thread-1")
	require.NoError(t, err)
	require.NotNil(t, link)
	assert.Equal(t, "parent-ch-3", link.ChannelID)
	assert.Equal(t, "ops", link.DefaultAgent)
}

func TestHandleThreadCreate_CachesParentMapping(t *testing.T) {
	session := stubSession([]*discordgo.Channel{
		{ID: "parent-ch-4", Type: discordgo.ChannelTypeGuildText},
	})
	b := testBroker(session)

	// Clear any prior state in the global cache.
	threadParentsMu.Lock()
	delete(threadParents, "new-thread-1")
	threadParentsMu.Unlock()

	// Simulate a ThreadCreate event.
	b.handleThreadCreate(session, &discordgo.ThreadCreate{
		Channel: &discordgo.Channel{
			ID:       "new-thread-1",
			ParentID: "parent-ch-4",
			Name:     "discussion-thread",
			Type:     discordgo.ChannelTypeGuildPublicThread,
		},
	})

	// Verify the global cache was populated.
	threadParentsMu.Lock()
	parentID := threadParents["new-thread-1"]
	threadParentsMu.Unlock()
	assert.Equal(t, "parent-ch-4", parentID)

	// Verify the broker-level cache was populated.
	b.mu.RLock()
	brokerParent := b.threadParents["new-thread-1"]
	b.mu.RUnlock()
	assert.Equal(t, "parent-ch-4", brokerParent)
}

func TestHandleThreadCreate_NilChannel(t *testing.T) {
	session := stubSession(nil)
	b := testBroker(session)

	// Should not panic on nil ThreadCreate.
	b.handleThreadCreate(session, nil)

	// Should not panic on nil embedded Channel.
	b.handleThreadCreate(session, &discordgo.ThreadCreate{
		Channel: nil,
	})
}

func TestResolveChannelLink_ThreadWithCachedParent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Set up parent channel link.
	require.NoError(t, store.CreateChannelLink(ctx, &ChannelLink{
		ChannelID:    "parent-ch-5",
		GuildID:      testGuildID,
		ProjectID:    "proj-1",
		DefaultAgent: "devops",
		LinkedAt:     time.Now().UTC(),
		Active:       true,
	}))

	// Pre-populate the global cache (as handleThreadCreate would do).
	threadParentsMu.Lock()
	threadParents["cached-thread-1"] = "parent-ch-5"
	threadParentsMu.Unlock()

	// Session doesn't need the thread in state — cache should be used.
	session := stubSession([]*discordgo.Channel{
		{ID: "parent-ch-5", Type: discordgo.ChannelTypeGuildText},
	})

	link, err := resolveChannelLink(ctx, session, store, "cached-thread-1")
	require.NoError(t, err)
	require.NotNil(t, link)
	assert.Equal(t, "parent-ch-5", link.ChannelID)
	assert.Equal(t, "devops", link.DefaultAgent)
}
