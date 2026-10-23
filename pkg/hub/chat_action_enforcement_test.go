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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every thread action _capabilities reports is enforced by its route
// through chatThreadAccess (ptone/scion#5519 Phase 1). For each route: a
// project viewer reads the thread but is refused with 403
// chat_post_denied (details.chat_action names the action, details.reason
// read_only) and nothing changes; a project member and a project owner
// keep the action; a token without inbox:write is refused.

// assertChatActionDenied checks the refusal of a reader who may not take
// action: 403 chat_post_denied naming the action, reason read_only.
func assertChatActionDenied(t *testing.T, rec *httptest.ResponseRecorder, action string) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	code, details := errorCodeAndDetails(t, rec)
	assert.Equal(t, ErrCodeChatPostDenied, code)
	assert.Equal(t, action, details["chat_action"])
	assert.Equal(t, ChatReasonReadOnly, details["reason"])
}

// topicName reads a thread's stored name and default agent; ok is false
// when the thread no longer exists.
func (f *chatFixture) topicState(t *testing.T, key string) (name, defaultAgent string, ok bool) {
	t.Helper()
	topic, err := f.wcs.GetTopic(context.Background(), key)
	if err != nil || topic == nil {
		return "", "", false
	}
	return topic.Name, topic.DefaultAgent, true
}

func TestChatTopicRename_RequiresRenameAction(t *testing.T) {
	f := newChatFixture(t)
	viewer := f.chatViewer(t, f.projA)
	path := "/api/v1/chat/topics/" + f.topicA
	before, _, _ := f.topicState(t, f.topicA)

	rec := f.session(t, viewer, http.MethodPatch, path, map[string]string{"name": "viewer-rename"})
	assertChatActionDenied(t, rec, ChatActionRename)
	name, _, ok := f.topicState(t, f.topicA)
	require.True(t, ok)
	assert.Equal(t, before, name, "a refused rename leaves the name unchanged")

	// A viewer's token with inbox:write passes the token checks and is
	// refused by the thread decision.
	viewerToken := f.mint(t, viewer, projectBoundary(f.projA), "project:read", "inbox:write")
	assertChatActionDenied(t, f.call(t, viewerToken, http.MethodPatch, path, map[string]string{"name": "viewer-token-rename"}), ChatActionRename)

	// A token without inbox:write is refused.
	readOnly := f.mint(t, f.member, projectBoundary(f.projA), "project:read")
	assert.Equal(t, http.StatusForbidden, f.call(t, readOnly, http.MethodPatch, path, map[string]string{"name": "token-rename"}).Code)
	name, _, _ = f.topicState(t, f.topicA)
	assert.Equal(t, before, name)

	rec = f.session(t, f.member, http.MethodPatch, path, map[string]string{"name": "member-rename"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	name, _, _ = f.topicState(t, f.topicA)
	assert.Equal(t, "member-rename", name)

	rec = f.session(t, f.owner, http.MethodPatch, path, map[string]string{"name": "owner-rename"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	name, _, _ = f.topicState(t, f.topicA)
	assert.Equal(t, "owner-rename", name)
}

func TestChatTopicDefaultAgent_RequiresSetDefaultAgentAction(t *testing.T) {
	f := newChatFixture(t)
	viewer := f.chatViewer(t, f.projA)
	path := "/api/v1/chat/topics/" + f.topicA

	rec := f.session(t, viewer, http.MethodPatch, path, map[string]string{"defaultAgent": f.agentA.Slug})
	assertChatActionDenied(t, rec, ChatActionSetDefaultAgent)
	_, da, ok := f.topicState(t, f.topicA)
	require.True(t, ok)
	assert.Empty(t, da, "a refused PATCH sets no default agent")

	readOnly := f.mint(t, f.member, projectBoundary(f.projA), "project:read")
	assert.Equal(t, http.StatusForbidden, f.call(t, readOnly, http.MethodPatch, path, map[string]string{"defaultAgent": f.agentA.Slug}).Code)
	_, da, _ = f.topicState(t, f.topicA)
	assert.Empty(t, da)

	rec = f.session(t, f.member, http.MethodPatch, path, map[string]string{"defaultAgent": f.agentA.Slug})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, da, _ = f.topicState(t, f.topicA)
	assert.Equal(t, f.agentA.Slug, da)

	rec = f.session(t, f.owner, http.MethodPatch, path, map[string]string{"defaultAgent": ""})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, da, _ = f.topicState(t, f.topicA)
	assert.Empty(t, da)
}

func TestChatTopicDelete_RequiresDeleteAction(t *testing.T) {
	f := newChatFixture(t)
	viewer := f.chatViewer(t, f.projA)
	forMember, forOwner := f.topic(t, f.projA), f.topic(t, f.projA)

	rec := f.session(t, viewer, http.MethodDelete, "/api/v1/chat/topics/"+forMember, nil)
	assertChatActionDenied(t, rec, ChatActionDelete)
	_, _, ok := f.topicState(t, forMember)
	assert.True(t, ok, "a refused delete leaves the thread in place")

	readOnly := f.mint(t, f.member, projectBoundary(f.projA), "project:read")
	assert.Equal(t, http.StatusForbidden, f.call(t, readOnly, http.MethodDelete, "/api/v1/chat/topics/"+forMember, nil).Code)
	_, _, ok = f.topicState(t, forMember)
	assert.True(t, ok)

	rec = f.session(t, f.member, http.MethodDelete, "/api/v1/chat/topics/"+forMember, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, _, ok = f.topicState(t, forMember)
	assert.False(t, ok)

	rec = f.session(t, f.owner, http.MethodDelete, "/api/v1/chat/topics/"+forOwner, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, _, ok = f.topicState(t, forOwner)
	assert.False(t, ok)
}

func TestChatConversationDefaultAgent_RequiresSetDefaultAgentAction(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	viewer := f.chatViewer(t, f.projA)
	conv := f.group(t, f.projA, viewer, f.member, f.owner)
	path := "/api/v1/conversations/" + conv.ID + "/default-agent"
	body := map[string]string{"agentId": f.agentA.ID}
	unset := func() bool {
		t.Helper()
		got, err := f.s.GetConversation(ctx, conv.ID)
		require.NoError(t, err)
		return got.DefaultAgentID == nil || *got.DefaultAgentID == ""
	}

	assertChatActionDenied(t, f.session(t, viewer, http.MethodPut, path, body), ChatActionSetDefaultAgent)
	assert.True(t, unset(), "a refused request sets no default agent")

	readOnly := f.mint(t, f.member, projectBoundary(f.projA), "project:read")
	assert.Equal(t, http.StatusForbidden, f.call(t, readOnly, http.MethodPut, path, body).Code)
	assert.True(t, unset())

	rec := f.session(t, f.member, http.MethodPut, path, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, unset())

	rec = f.session(t, f.owner, http.MethodPut, path, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestChatConversationAddParticipant_RequiresAddParticipantAction(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	viewer := f.chatViewer(t, f.projA)
	other := f.chatViewer(t, f.projA)
	conv := f.group(t, f.projA, viewer, f.member, f.owner)
	path := "/api/v1/conversations/" + conv.ID + "/participants"
	add := func(id string) map[string]string {
		return map[string]string{"principalKind": "user", "principalId": id}
	}
	isParticipant := func(id string) bool {
		t.Helper()
		ok, err := isConversationParticipant(ctx, f.s, conv.ID, "user", id)
		require.NoError(t, err)
		return ok
	}

	assertChatActionDenied(t, f.session(t, viewer, http.MethodPost, path, add(other)), ChatActionAddParticipant)
	assert.False(t, isParticipant(other), "a refused request adds no participant")

	readOnly := f.mint(t, f.member, projectBoundary(f.projA), "project:read")
	assert.Equal(t, http.StatusForbidden, f.call(t, readOnly, http.MethodPost, path, add(other)).Code)
	assert.False(t, isParticipant(other))

	rec := f.session(t, f.member, http.MethodPost, path, add(other))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.True(t, isParticipant(other))

	rec = f.session(t, f.owner, http.MethodPost, path, add(f.admin))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.True(t, isParticipant(f.admin))
}

func TestChatMessageEdit_RequiresEditOwnAction(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	viewer := f.chatViewer(t, f.projA)
	content := func(id string) string {
		t.Helper()
		m, err := f.s.GetMessage(ctx, id)
		require.NoError(t, err)
		return m.Msg
	}
	path := func(id string) string { return chatConversationPath(f.topicA, "messages/"+id) }

	mine := f.chatMessage(t, "user:"+viewer, viewer, f.topicA, f.projA, "viewer said")
	assertChatActionDenied(t, f.session(t, viewer, http.MethodPut, path(mine), map[string]string{"content": "edited"}), ChatActionEditOwn)
	assert.Equal(t, "viewer said", content(mine), "a refused edit leaves the message unchanged")

	memberMsg := f.chatMessage(t, "user:"+f.member, f.member, f.topicA, f.projA, "member said")
	readOnly := f.mint(t, f.member, projectBoundary(f.projA), "project:read")
	assert.Equal(t, http.StatusForbidden, f.call(t, readOnly, http.MethodPut, path(memberMsg), map[string]string{"content": "token edit"}).Code)
	assert.Equal(t, "member said", content(memberMsg))

	rec := f.session(t, f.member, http.MethodPut, path(memberMsg), map[string]string{"content": "member edited"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "member edited", content(memberMsg))

	ownerMsg := f.chatMessage(t, "user:"+f.owner, f.owner, f.topicA, f.projA, "owner said")
	rec = f.session(t, f.owner, http.MethodPut, path(ownerMsg), map[string]string{"content": "owner edited"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "owner edited", content(ownerMsg))
}

// TestChatMessageDelete_DeleteOwnAction: delete_own is a reader's action,
// so a viewer still retracts its own message; a token without inbox:write
// is refused and the message is unchanged.
func TestChatMessageDelete_DeleteOwnAction(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	viewer := f.chatViewer(t, f.projA)
	content := func(id string) string {
		t.Helper()
		m, err := f.s.GetMessage(ctx, id)
		require.NoError(t, err)
		return m.Msg
	}
	path := func(id string) string { return chatConversationPath(f.topicA, "messages/"+id) }

	memberMsg := f.chatMessage(t, "user:"+f.member, f.member, f.topicA, f.projA, "member said")
	readOnly := f.mint(t, f.member, projectBoundary(f.projA), "project:read")
	assert.Equal(t, http.StatusForbidden, f.call(t, readOnly, http.MethodDelete, path(memberMsg), nil).Code)
	assert.Equal(t, "member said", content(memberMsg))

	for _, who := range []string{viewer, f.member, f.owner} {
		id := f.chatMessage(t, "user:"+who, who, f.topicA, f.projA, "to retract")
		rec := f.session(t, who, http.MethodDelete, path(id), nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Empty(t, content(id), "a retracted message is redacted")
	}
}
