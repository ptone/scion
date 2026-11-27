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

package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

// capturingLogHandler records structured log attributes for target-denial assertions.
type capturingLogHandler struct {
	mu      sync.Mutex
	records []map[string]any
}

func (h *capturingLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingLogHandler) Handle(_ context.Context, r slog.Record) error {
	entry := map[string]any{"msg": r.Message, "level": r.Level.String()}
	r.Attrs(func(a slog.Attr) bool {
		entry[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, entry)
	h.mu.Unlock()
	return nil
}

func (h *capturingLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingLogHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *capturingLogHandler) denialRecords() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]any
	for _, r := range h.records {
		if r["event"] == "outbound_target_denied" {
			out = append(out, r)
		}
	}
	return out
}

// authzFaultStore wraps a Store and selectively injects lookup errors.
type authzFaultStore struct {
	Store
	getGroupLinkErr            error
	getGroupLinksForProjectErr error
	getUserMappingErr          error
	savedAsksMu                sync.Mutex
	savedAsks                  []*PendingAskUser
}

func (s *authzFaultStore) GetGroupLink(ctx context.Context, chatID int64) (*GroupLink, error) {
	if s.getGroupLinkErr != nil {
		return nil, s.getGroupLinkErr
	}
	return s.Store.GetGroupLink(ctx, chatID)
}

func (s *authzFaultStore) GetGroupLinksForProject(ctx context.Context, projectID string) ([]*GroupLink, error) {
	if s.getGroupLinksForProjectErr != nil {
		return nil, s.getGroupLinksForProjectErr
	}
	return s.Store.GetGroupLinksForProject(ctx, projectID)
}

func (s *authzFaultStore) GetUserMappingByEmail(ctx context.Context, email string) (*TelegramUserMapping, error) {
	if s.getUserMappingErr != nil {
		return nil, s.getUserMappingErr
	}
	return s.Store.GetUserMappingByEmail(ctx, email)
}

func (s *authzFaultStore) SavePendingAskUser(ctx context.Context, pending *PendingAskUser) error {
	s.savedAsksMu.Lock()
	s.savedAsks = append(s.savedAsks, pending)
	s.savedAsksMu.Unlock()
	return s.Store.SavePendingAskUser(ctx, pending)
}

func (s *authzFaultStore) getSavedAsks() []*PendingAskUser {
	s.savedAsksMu.Lock()
	defer s.savedAsksMu.Unlock()
	out := make([]*PendingAskUser, len(s.savedAsks))
	copy(out, s.savedAsks)
	return out
}

func newTargetAuthzBrokerV2(t *testing.T, tgSrv *fakeTGServerV2, hub *fakeHubClient) (*TelegramBrokerV2, *capturingLogHandler) {
	t.Helper()
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "tg-authz.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	logHandler := &capturingLogHandler{}
	b := NewV2(slog.New(logHandler))
	b.api = NewAPIClient("test-token", tgSrv.srv.URL)
	b.store = st
	b.hubClient = hub
	return b, logHandler
}

// A4 (v2): Cross-project explicit target is denied, logs outbound_target_denied
// with reason=foreign_project, does not leak the forum thread ID, and falls
// through to the topic project's own routing.
func TestV2_TargetAuthz_CrossProjectDeniedAndFallsThrough(t *testing.T) {
	cases := []struct {
		name       string
		makeMsg    func() *messages.StructuredMessage
		wantSource string
	}{
		{
			name: "Conversation.ExternalRef chat:thread",
			makeMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice",
					Msg:       "reply to foreign forum topic",
					Type:      messages.TypeInstruction,
					Conversation: &messages.Conversation{
						Surface:     "telegram",
						ExternalRef: "-100200:42",
						ParentRef:   "-100200",
					},
				}
			},
			wantSource: string(plugin.TargetSourceConversation),
		},
		{
			name: "Conversation.ParentRef + ExternalRef",
			makeMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice",
					Msg:       "reply via foreign parent ref",
					Type:      messages.TypeInstruction,
					Conversation: &messages.Conversation{
						Surface:     "telegram",
						ExternalRef: "42",
						ParentRef:   "-100200",
					},
				}
			},
			wantSource: string(plugin.TargetSourceConversation),
		},
		{
			name: "legacy ThreadID chat:thread",
			makeMsg: func() *messages.StructuredMessage {
				return &messages.StructuredMessage{
					Version:   messages.Version,
					Sender:    "agent:coder",
					Recipient: "user:alice",
					Msg:       "reply via foreign thread id",
					Type:      messages.TypeInstruction,
					ThreadID:  "-100200:42",
				}
			},
			wantSource: string(plugin.TargetSourceThreadID),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgSrv := newFakeTGServerV2(t)
			b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)
			ctx := context.Background()

			require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
				ChatID:    -100100,
				ProjectID: "proj-a",
				Active:    true,
			}))
			require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
				ChatID:    -100200,
				ProjectID: "proj-b",
				Active:    true,
			}))

			err := b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", tc.makeMsg())
			require.NoError(t, err)

			sent := tgSrv.getSentMessages()
			require.Len(t, sent, 1, "must fall through to proj-a's active link")
			assert.Equal(t, int64(-100100), sent[0].ChatID)
			assert.Equal(t, int64(0), sent[0].MessageThreadID, "denied forum thread ID must not leak to fallback chat")

			denials := logs.denialRecords()
			require.Len(t, denials, 1)
			assert.Equal(t, "telegram", denials[0]["surface"])
			assert.Equal(t, "proj-a", denials[0]["project_id"])
			assert.Equal(t, tc.wantSource, denials[0]["source"])
			assert.Equal(t, "-100200", denials[0]["target"])
			assert.Equal(t, string(plugin.DenyForeignProject), denials[0]["reason"])
		})
	}
}

// A5 + I1 (v2): Fail-closed denials for not_linked, inactive_link, lookup_error,
// no_store, and no_project (non-canonical topic such as scion.global.broadcast).
func TestV2_TargetAuthz_FailClosedReasons(t *testing.T) {
	t.Run("not_linked", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "unlinked target",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100999:7",
			},
		}
		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.coder.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages())

		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyNotLinked), denials[0]["reason"])
	})

	t.Run("inactive_link", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)
		ctx := context.Background()

		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:    -100100,
			ProjectID: "proj-a",
			Active:    false,
		}))

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "inactive link target",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100100:7",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages())

		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyInactiveLink), denials[0]["reason"])
	})

	t.Run("lookup_error", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)
		b.store = &authzFaultStore{
			Store:           b.store,
			getGroupLinkErr: errors.New("db unavailable"),
		}

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "store error target",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100100:7",
			},
		}
		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.coder.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages())

		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyLookupError), denials[0]["reason"])
	})

	t.Run("no_store", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)
		b.store = nil

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "nil store target",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100100:7",
			},
		}
		require.NoError(t, b.Publish(context.Background(), "scion.project.proj-a.agent.coder.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages(), "nil store must fail closed")

		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyNoStore), denials[0]["reason"])
	})

	t.Run("no_project on non-canonical topic (I1)", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)
		ctx := context.Background()

		// Even if a link existed with ProjectID matching the raw topic string,
		// a non-canonical topic must fail closed with no_project.
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:    -100100,
			ProjectID: "scion.global.broadcast",
			Active:    true,
		}))

		ok, reason := b.authorizeExplicitTarget(ctx, b.store, canonicalTopicProjectID("scion.global.broadcast"), "", nil, "-100100")
		assert.False(t, ok)
		assert.Equal(t, plugin.DenyNoProject, reason)

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "global broadcast with explicit target",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100100:5",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.global.broadcast", msg))

		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, "", denials[0]["project_id"])
		assert.Equal(t, string(plugin.DenyNoProject), denials[0]["reason"])
	})
}

// A6 (v2): Active link for topic project with forum topic delivers to exact
// chat and MessageThreadID.
func TestV2_TargetAuthz_AllowedForumTopic(t *testing.T) {
	tgSrv := newFakeTGServerV2(t)
	b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)
	ctx := context.Background()

	require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
		ChatID:    -100100,
		ProjectID: "proj-a",
		Active:    true,
	}))

	msg := &messages.StructuredMessage{
		Version:   messages.Version,
		Sender:    "agent:coder",
		Recipient: "user:alice",
		Msg:       "forum topic reply",
		Type:      messages.TypeInstruction,
		Conversation: &messages.Conversation{
			Surface:     "telegram",
			ExternalRef: "42",
			ParentRef:   "-100100",
		},
	}
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

	sent := tgSrv.getSentMessages()
	require.Len(t, sent, 1)
	assert.Equal(t, int64(-100100), sent[0].ChatID)
	assert.Equal(t, int64(42), sent[0].MessageThreadID)
	assert.Empty(t, logs.denialRecords())
}

// A7 (v2): Rule 2 personal DM authorization.
//   - Recipient's own positive chat ID + can read project -> delivered.
//   - Another user's positive chat ID -> denied not_recipient_dm.
//   - Recipient who cannot read project -> denied not_recipient_dm and dropped.
//   - Hub project-read check error -> denied lookup_error and dropped.
func TestV2_TargetAuthz_PersonalDM(t *testing.T) {
	ctx := context.Background()

	t.Run("recipient own DM with project read access allowed", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		hub := newFakeHubClient()
		hub.userProjects = map[string][]ProjectOption{
			"user:alice@example.com": {{ID: "proj-a", Slug: "proj-a"}},
		}
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, hub)

		require.NoError(t, b.store.SaveUserMapping(ctx, &TelegramUserMapping{
			TelegramUserID: "555001",
			ScionEmail:     "alice@example.com",
		}))

		msg := &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "hello alice dm",
			Type:      messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "555001:0",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

		sent := tgSrv.getSentMessages()
		require.Len(t, sent, 1)
		assert.Equal(t, int64(555001), sent[0].ChatID)
		assert.Empty(t, logs.denialRecords())
	})

	t.Run("another user DM denied not_recipient_dm", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		hub := newFakeHubClient()
		hub.userProjects = map[string][]ProjectOption{
			"user:alice@example.com": {{ID: "proj-a", Slug: "proj-a"}},
			"user:bob@example.com":   {{ID: "proj-a", Slug: "proj-a"}},
		}
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, hub)

		require.NoError(t, b.store.SaveUserMapping(ctx, &TelegramUserMapping{
			TelegramUserID: "555001",
			ScionEmail:     "alice@example.com",
		}))
		require.NoError(t, b.store.SaveUserMapping(ctx, &TelegramUserMapping{
			TelegramUserID: "555002",
			ScionEmail:     "bob@example.com",
		}))
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:    -100100,
			ProjectID: "proj-a",
			Active:    true,
		}))

		msg := &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "targeted at bob dm",
			Type:      messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "555002:0",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

		sent := tgSrv.getSentMessages()
		require.Len(t, sent, 1)
		assert.Equal(t, int64(-100100), sent[0].ChatID, "must not deliver to bob's DM; falls through to proj-a group")

		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyNotRecipientDM), denials[0]["reason"])
	})

	t.Run("recipient who cannot read project denied", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		hub := newFakeHubClient()
		hub.userProjects = map[string][]ProjectOption{
			"user:bob@example.com": {{ID: "proj-b", Slug: "proj-b"}},
		}
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, hub)

		require.NoError(t, b.store.SaveUserMapping(ctx, &TelegramUserMapping{
			TelegramUserID: "555002",
			ScionEmail:     "bob@example.com",
		}))
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:    -100100,
			ProjectID: "proj-a",
			Active:    true,
		}))

		msg := &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:bob@example.com",
			Msg:       "proj-a message for bob",
			Type:      messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "555002:0",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

		// Explicit target denied, and Priority 1 recipient check also drops rather than broadcasting.
		assert.Empty(t, tgSrv.getSentMessages())

		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyNotRecipientDM), denials[0]["reason"])
	})

	t.Run("hub project check failure fails closed with lookup_error", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		hub := newFakeHubClient()
		hub.listUserProjectsErr = errors.New("hub timeout")
		b, logs := newTargetAuthzBrokerV2(t, tgSrv, hub)

		require.NoError(t, b.store.SaveUserMapping(ctx, &TelegramUserMapping{
			TelegramUserID: "555001",
			ScionEmail:     "alice@example.com",
		}))

		msg := &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "dm when hub fails",
			Type:      messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "555001:0",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

		assert.Empty(t, tgSrv.getSentMessages())
		denials := logs.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyLookupError), denials[0]["reason"])
	})
}

// A8 (v2): telegram_attachment_path and telegram_attachment_name metadata keys
// are ignored; attachments are read exclusively from msg.Attachments.
func TestV2_MetadataAttachmentKeysIgnored(t *testing.T) {
	tgSrv := newFakeTGServerV2(t)
	b, _ := newTargetAuthzBrokerV2(t, tgSrv, nil)
	ctx := context.Background()

	require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
		ChatID:    -100100,
		ProjectID: "proj-a",
		Active:    true,
	}))

	var opened []string
	orig := openAttachmentFile
	t.Cleanup(func() { openAttachmentFile = orig })
	openAttachmentFile = func(name string) (*os.File, error) {
		opened = append(opened, name)
		return orig(name)
	}

	// 1. Metadata telegram_attachment_path without msg.Attachments must not open any file.
	msg := &messages.StructuredMessage{
		Version: messages.Version,
		Sender:  "agent:coder",
		Msg:     "text only message",
		Type:    messages.TypeInstruction,
		Metadata: map[string]string{
			"telegram_attachment_path": "/etc/passwd",
			"telegram_attachment_name": "override.txt",
		},
	}
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))
	assert.Empty(t, opened, "telegram_attachment_path metadata must never be opened")
	sent := tgSrv.getSentMessages()
	require.Len(t, sent, 1)
	assert.Equal(t, int64(-100100), sent[0].ChatID)

	// 2. publishAttachment ignores telegram_attachment_name metadata and uses filepath.Base.
	tmpFile := filepath.Join(t.TempDir(), "real-report.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("report body"), 0o600))

	var gotFilename string
	docSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		_, hdr, err := r.FormFile("document")
		require.NoError(t, err)
		gotFilename = hdr.Filename
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":101,"chat":{"id":-100100}}}`))
	}))
	defer docSrv.Close()

	docAPI := NewAPIClient("test-token", docSrv.URL)
	err := b.publishAttachment(ctx, docAPI, []int64{-100100}, &messages.StructuredMessage{
		Version: messages.Version,
		Sender:  "agent:coder",
		Msg:     "with attachment",
		Type:    messages.TypeInstruction,
		Metadata: map[string]string{
			"telegram_attachment_name": "custom-name.txt",
		},
	}, "coder", tmpFile)
	require.NoError(t, err)
	assert.Equal(t, "real-report.txt", gotFilename, "telegram_attachment_name metadata must be ignored")
}

// A13 (v2): Agent-to-agent observer filter and state-change group filter fail closed.
func TestV2_ObserverAndStateChangeFiltersFailClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("agent-to-agent ShowAgentToAgent=false suppressed", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, _ := newTargetAuthzBrokerV2(t, tgSrv, nil)
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:           -100100,
			ProjectID:        "proj-a",
			Active:           true,
			ShowAgentToAgent: false,
		}))

		msg := messages.NewInstruction("agent:coder", "agent:reviewer", "internal handoff")
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.reviewer.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages())
	})

	t.Run("agent-to-agent lookup error fails closed", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, _ := newTargetAuthzBrokerV2(t, tgSrv, nil)
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:           -100100,
			ProjectID:        "proj-a",
			Active:           true,
			ShowAgentToAgent: true,
		}))
		// GetGroupLinksForProject succeeds (Priority 2), but GetGroupLink fails in observer check.
		b.store = &authzFaultStore{
			Store:           b.store,
			getGroupLinkErr: errors.New("db error"),
		}

		msg := messages.NewInstruction("agent:coder", "agent:reviewer", "internal handoff on db err")
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.reviewer.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages(), "observer check must fail closed on lookup error")
	})

	t.Run("agent-to-agent ShowAgentToAgent=true delivered", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, _ := newTargetAuthzBrokerV2(t, tgSrv, nil)
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:           -100100,
			ProjectID:        "proj-a",
			Active:           true,
			ShowAgentToAgent: true,
		}))

		msg := messages.NewInstruction("agent:coder", "agent:reviewer", "visible handoff")
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.reviewer.messages", msg))
		sent := tgSrv.getSentMessages()
		require.Len(t, sent, 1)
		assert.Equal(t, int64(-100100), sent[0].ChatID)
	})

	t.Run("state-change NotifyInGroup fails closed", func(t *testing.T) {
		tgSrv := newFakeTGServerV2(t)
		b, _ := newTargetAuthzBrokerV2(t, tgSrv, nil)
		// Group 1: active, NotifyInGroup=false -> skipped
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:        -100100,
			ProjectID:     "proj-a",
			Active:        true,
			NotifyInGroup: false,
		}))
		// Group 2: inactive, NotifyInGroup=true -> skipped
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:        -100101,
			ProjectID:     "proj-a",
			Active:        false,
			NotifyInGroup: true,
		}))

		msg := messages.NewNotification("agent:coder", "user:unknown", "agent stopped", messages.TypeStateChange)
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages())

		// Store error on GetGroupLinksForProject -> fails closed
		faultStore := &authzFaultStore{
			Store:                      b.store,
			getGroupLinksForProjectErr: errors.New("db error"),
		}
		b.store = faultStore
		msg2 := messages.NewNotification("agent:coder", "user:unknown", "agent running", messages.TypeStateChange)
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg2))
		assert.Empty(t, tgSrv.getSentMessages())

		// Active + NotifyInGroup=true -> delivered
		b.store = faultStore.Store
		require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
			ChatID:        -100102,
			ProjectID:     "proj-a",
			Active:        true,
			NotifyInGroup: true,
		}))
		msg3 := messages.NewNotification("agent:coder", "user:unknown", "agent completed", messages.TypeStateChange)
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg3))
		sent := tgSrv.getSentMessages()
		require.Len(t, sent, 1)
		assert.Equal(t, int64(-100102), sent[0].ChatID)
	})
}

// A13b (v2): Input-needed with a cross-project explicit target sends no card
// and creates no PendingAskUser row for the foreign project's chat.
func TestV2_InputNeeded_CrossProjectTargetDenied(t *testing.T) {
	tgSrv := newFakeTGServerV2(t)
	b, logs := newTargetAuthzBrokerV2(t, tgSrv, nil)
	ctx := context.Background()

	faultStore := &authzFaultStore{Store: b.store}
	b.store = faultStore

	// Only proj-b has a linked group; proj-a has none.
	require.NoError(t, b.store.SaveGroupLink(ctx, &GroupLink{
		ChatID:    -100200,
		ProjectID: "proj-b",
		LinkedAt:  time.Now().UTC(),
		Active:    true,
	}))

	msg := &messages.StructuredMessage{
		Version: messages.Version,
		Sender:  "agent:coder",
		Msg:     "Approve deployment?",
		Type:    messages.TypeInputNeeded,
		Conversation: &messages.Conversation{
			Surface:     "telegram",
			ExternalRef: "-100200:10",
		},
		Metadata: map[string]string{
			"choices": `["Yes","No"]`,
		},
	}
	require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

	assert.Empty(t, tgSrv.getSentMessages(), "foreign project chat must receive no input-needed card")
	assert.Empty(t, faultStore.getSavedAsks(), "no PendingAskUser row may be saved for foreign project chat")

	denials := logs.denialRecords()
	require.Len(t, denials, 1)
	assert.Equal(t, string(plugin.DenyForeignProject), denials[0]["reason"])
}

// A4, A5, A6, A7, I1 (v1): TelegramBroker static-route target authorization.
func TestV1_TargetAuthz(t *testing.T) {
	ctx := context.Background()

	t.Run("A4 cross-project target denied and falls through", func(t *testing.T) {
		tgSrv := newFakeTelegramServer(t)
		logHandler := &capturingLogHandler{}
		b := New(slog.New(logHandler))
		b.api = NewAPIClient("test-token", tgSrv.srv.URL)
		b.chatRoutes[-100100] = "scion.project.proj-a.>"
		b.chatRoutes[-100200] = "scion.project.proj-b.>"

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "v1 cross project",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100200",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

		sent := tgSrv.getSentMessages()
		require.Len(t, sent, 1)
		assert.Equal(t, int64(-100100), sent[0].ChatID)

		denials := logHandler.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyForeignProject), denials[0]["reason"])
		assert.Equal(t, "proj-a", denials[0]["project_id"])
	})

	t.Run("A5 empty routes and unmapped chat fail closed", func(t *testing.T) {
		tgSrv := newFakeTelegramServer(t)
		logHandler := &capturingLogHandler{}
		b := New(slog.New(logHandler))
		b.api = NewAPIClient("test-token", tgSrv.srv.URL)

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "v1 unmapped",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100999",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages())

		denials := logHandler.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyNotLinked), denials[0]["reason"])
	})

	t.Run("A6 matching chatRoutes pattern allowed", func(t *testing.T) {
		tgSrv := newFakeTelegramServer(t)
		logHandler := &capturingLogHandler{}
		b := New(slog.New(logHandler))
		b.api = NewAPIClient("test-token", tgSrv.srv.URL)
		b.chatRoutes[-100100] = "scion.project.proj-a.>"

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "agent:coder",
			Msg:     "v1 allowed",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100100",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))

		sent := tgSrv.getSentMessages()
		require.Len(t, sent, 1)
		assert.Equal(t, int64(-100100), sent[0].ChatID)
		assert.Empty(t, logHandler.denialRecords())
	})

	t.Run("A7 v1 has no Rule 2 for positive DM chat ID", func(t *testing.T) {
		tgSrv := newFakeTelegramServer(t)
		logHandler := &capturingLogHandler{}
		b := New(slog.New(logHandler))
		b.api = NewAPIClient("test-token", tgSrv.srv.URL)

		msg := &messages.StructuredMessage{
			Version:   messages.Version,
			Sender:    "agent:coder",
			Recipient: "user:alice@example.com",
			Msg:       "v1 dm attempt",
			Type:      messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "555001:0",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.project.proj-a.agent.coder.messages", msg))
		assert.Empty(t, tgSrv.getSentMessages())

		denials := logHandler.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyNotLinked), denials[0]["reason"])
	})

	t.Run("I1 non-canonical topic denies explicit target with no_project while implicit route still delivers", func(t *testing.T) {
		tgSrv := newFakeTelegramServer(t)
		logHandler := &capturingLogHandler{}
		b := New(slog.New(logHandler))
		b.api = NewAPIClient("test-token", tgSrv.srv.URL)
		b.chatRoutes[-100100] = "scion.global.>"
		b.topicChats["scion.global.broadcast"] = []int64{-100100}

		msg := &messages.StructuredMessage{
			Version: messages.Version,
			Sender:  "system",
			Msg:     "global broadcast",
			Type:    messages.TypeInstruction,
			Conversation: &messages.Conversation{
				Surface:     "telegram",
				ExternalRef: "-100200",
			},
		}
		require.NoError(t, b.Publish(ctx, "scion.global.broadcast", msg))

		// Explicit target -100200 denied with no_project; implicit route -100100 still delivers.
		sent := tgSrv.getSentMessages()
		require.Len(t, sent, 1)
		assert.Equal(t, int64(-100100), sent[0].ChatID)

		denials := logHandler.denialRecords()
		require.Len(t, denials, 1)
		assert.Equal(t, string(plugin.DenyNoProject), denials[0]["reason"])
		assert.True(t, strings.EqualFold("", denials[0]["project_id"].(string)))
	})
}
