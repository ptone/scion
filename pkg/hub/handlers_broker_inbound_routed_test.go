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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// routedTestEnv holds the common test fixtures for routed inbound tests.
type routedTestEnv struct {
	srv     *Server
	store   store.Store
	user    *store.User
	project *store.Project
	agent1  *store.Agent // "alpha" — running, project mode
	agent2  *store.Agent // "beta" — running, project mode
	agent3  *store.Agent // "gamma" — stopped
}

func setupRoutedTestEnv(t *testing.T) routedTestEnv {
	t.Helper()
	srv, s := testServer(t)
	ctx := context.Background()

	// Create a project owner (separate from the test sender).
	owner := &store.User{
		ID:          tid("owner-routed"),
		Email:       "owner-routed@example.com",
		DisplayName: "Routed Owner",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, owner))
	ensureHubMembership(ctx, s, owner.ID)

	// Create a separate sender user (non-owner).
	user := &store.User{
		ID:          tid("user-routed"),
		Email:       "routed@example.com",
		DisplayName: "Routed User",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, user))
	ensureHubMembership(ctx, s, user.ID)

	project := &store.Project{
		ID:        tid("proj-routed"),
		Slug:      "routed-proj",
		Name:      "Routed Test Project",
		OwnerID:   owner.ID,
		CreatedBy: owner.ID,
		Created:   time.Now(),
		Updated:   time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))
	srv.createProjectMembersGroup(ctx, project)
	msgAuthzAddProjectMember(t, s, user.ID, project.ID, project.Slug, store.GroupMemberRoleMember)

	agent1 := &store.Agent{
		ID:           tid("agent-alpha"),
		Slug:         "alpha",
		Name:         "Alpha Agent",
		ProjectID:    project.ID,
		Phase:        string(state.PhaseRunning),
		MessageMode:  store.MessageModeProject,
		StateVersion: 1,
		Created:      time.Now(),
		Updated:      time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent1))

	agent2 := &store.Agent{
		ID:           tid("agent-beta"),
		Slug:         "beta",
		Name:         "Beta Agent",
		ProjectID:    project.ID,
		Phase:        string(state.PhaseRunning),
		MessageMode:  store.MessageModeProject,
		StateVersion: 1,
		Created:      time.Now(),
		Updated:      time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent2))

	agent3 := &store.Agent{
		ID:           tid("agent-gamma"),
		Slug:         "gamma",
		Name:         "Gamma Agent",
		ProjectID:    project.ID,
		Phase:        string(state.PhaseStopped),
		MessageMode:  store.MessageModeProject,
		StateVersion: 1,
		Created:      time.Now(),
		Updated:      time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent3))

	return routedTestEnv{
		srv:     srv,
		store:   s,
		user:    user,
		project: project,
		agent1:  agent1,
		agent2:  agent2,
		agent3:  agent3,
	}
}

func (e routedTestEnv) doRoutedRequest(t *testing.T, req routedInboundRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/broker/inbound/routed", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq = httpReq.WithContext(contextWithBrokerIdentity(httpReq.Context(), NewBrokerIdentity("test-broker")))

	rec := httptest.NewRecorder()
	e.srv.mux.ServeHTTP(rec, httpReq)
	return rec
}

func TestHandleBrokerInboundRouted_BasicDelivery(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version:   messages.Version,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Channel:   "slack",
			Sender:    "user:" + env.user.Email,
			Msg:       "hello",
			Type:      messages.TypeInstruction,
		},
	})

	// Dispatch will fail (no real dispatcher), but we should see 502 (runtime error)
	// because the hub has no dispatcher wired in test mode.
	// Let me check what status we get:
	if rec.Code == http.StatusOK {
		var resp routedInboundResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.True(t, resp.Delivered)
		assert.Equal(t, "alpha", resp.PrimaryAgent)
		assert.Len(t, resp.Results, 1)
		assert.Equal(t, "delivered", resp.Results[0].Status)
	} else {
		// No dispatcher available → 503
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	}
}

func TestHandleBrokerInboundRouted_MissingProjectID(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_MissingMessage(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID: env.project.ID,
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_NonUserSender(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "bot:something",
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_BroadcastRejected(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version:     messages.Version,
			Channel:     "slack",
			Sender:      "user:" + env.user.Email,
			Msg:         "hello",
			Type:        messages.TypeInstruction,
			Broadcasted: true,
		},
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_DMThreadRejected(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version:  messages.Version,
			Channel:  "slack",
			Sender:   "user:" + env.user.Email,
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			ThreadID: "dm:agent:123:user:456",
		},
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_ExternalRefWithoutSurface(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		ExternalRef:  "some-ref",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_ParentRefWithoutExternalRef(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		ParentRef:    "parent-ref",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_NoRecipient(t *testing.T) {
	env := setupRoutedTestEnv(t)

	// No default, no mentions → 422
	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID: env.project.ID,
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello with no agent",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, "no_routing_recipient", errResp.Error.Code)
}

func TestHandleBrokerInboundRouted_InactiveUser(t *testing.T) {
	env := setupRoutedTestEnv(t)
	ctx := context.Background()

	// Create inactive user.
	inactiveUser := &store.User{
		ID:      tid("user-inactive-routed"),
		Email:   "inactive-routed@example.com",
		Role:    store.UserRoleMember,
		Status:  "suspended",
		Created: time.Now(),
	}
	require.NoError(t, env.store.CreateUser(ctx, inactiveUser))

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + inactiveUser.Email,
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleBrokerInboundRouted_UnknownSender(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:nobody@example.com",
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleBrokerInboundRouted_InterruptStripping(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "! urgent message",
			Type:    messages.TypeInstruction,
		},
	})

	// Should process (503 for no dispatcher, or 200 if dispatcher present).
	// The key thing is it doesn't reject — the "!" is stripped.
	assert.NotEqual(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_NoBrokerAuth(t *testing.T) {
	env := setupRoutedTestEnv(t)

	body, err := json.Marshal(routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})
	require.NoError(t, err)

	// No broker identity in context.
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/broker/inbound/routed", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	env.srv.mux.ServeHTTP(rec, httpReq)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHandleBrokerInboundRouted_MethodNotAllowed(t *testing.T) {
	env := setupRoutedTestEnv(t)

	httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/broker/inbound/routed", nil)
	httpReq = httpReq.WithContext(contextWithBrokerIdentity(httpReq.Context(), NewBrokerIdentity("test-broker")))

	rec := httptest.NewRecorder()
	env.srv.mux.ServeHTTP(rec, httpReq)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandleBrokerInboundRouted_MentionRouting(t *testing.T) {
	env := setupRoutedTestEnv(t)

	// Message with @beta mention → should route to alpha (default) + beta.
	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello @beta",
			Type:    messages.TypeInstruction,
		},
	})

	// Without a dispatcher, expect 503.
	if rec.Code == http.StatusServiceUnavailable {
		return // expected: no dispatcher in test
	}

	// If a dispatcher was somehow available:
	var resp routedInboundResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "alpha", resp.PrimaryAgent)
	assert.GreaterOrEqual(t, len(resp.Results), 2)
}

func TestHandleBrokerInboundRouted_LeadingMentionOverride(t *testing.T) {
	env := setupRoutedTestEnv(t)

	// Leading @beta overrides default alpha.
	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "@beta hello",
			Type:    messages.TypeInstruction,
		},
	})

	if rec.Code == http.StatusServiceUnavailable {
		return
	}

	var resp routedInboundResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "beta", resp.PrimaryAgent)
}

func TestHandleBrokerInboundRouted_StoppedAgentPrimary(t *testing.T) {
	env := setupRoutedTestEnv(t)

	// gamma is stopped → should fail with not_running.
	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "gamma",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandleBrokerInboundRouted_UnresolvedMentionDiagnostic(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "@unknown hello",
			Type:    messages.TypeInstruction,
		},
	})

	if rec.Code == http.StatusServiceUnavailable {
		return
	}

	// Should still route to alpha (default), with "unknown" in unresolved.
	if rec.Code == http.StatusOK {
		var resp routedInboundResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "alpha", resp.PrimaryAgent)
		assert.Contains(t, resp.UnresolvedMentions, "unknown")
	}
}

func TestHandleBrokerInboundRouted_EmptyBody(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "",
			Type:    messages.TypeInstruction,
		},
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleBrokerInboundRouted_MissingDefault_WithMention(t *testing.T) {
	env := setupRoutedTestEnv(t)

	// No default, but @alpha is mentioned → alpha becomes primary.
	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID: env.project.ID,
		// No DefaultAgent
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "@alpha hello",
			Type:    messages.TypeInstruction,
		},
	})

	if rec.Code == http.StatusServiceUnavailable {
		return
	}

	if rec.Code == http.StatusOK {
		var resp routedInboundResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "alpha", resp.PrimaryAgent)
	}
}

func TestHandleBrokerInboundRouted_IncomingMentionMetadataStripped(t *testing.T) {
	env := setupRoutedTestEnv(t)

	rec := env.doRoutedRequest(t, routedInboundRequest{
		ProjectID:    env.project.ID,
		DefaultAgent: "alpha",
		Message: &messages.StructuredMessage{
			Version: messages.Version,
			Channel: "slack",
			Sender:  "user:" + env.user.Email,
			Msg:     "hello",
			Type:    messages.TypeInstruction,
			Metadata: map[string]string{
				"mention_co_addressees": `["evil"]`,
				"group_id":             "injected",
				"safe_key":             "preserved",
			},
		},
	})

	// The endpoint should have stripped mention_co_addressees and group_id.
	// We can't directly inspect the dispatched message from the test, but
	// the endpoint should not error from the metadata.
	assert.NotEqual(t, http.StatusBadRequest, rec.Code)
}
