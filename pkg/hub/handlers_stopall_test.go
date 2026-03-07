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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ptone/scion-agent/pkg/agent/state"
	"github.com/ptone/scion-agent/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopAllAgents_Global(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Create a grove
	grove := &store.Grove{
		ID:   "grove-1",
		Name: "Test Grove",
		Slug: "test-grove",
	}
	require.NoError(t, s.CreateGrove(ctx, grove))

	// Create running agents
	for i, name := range []string{"agent-1", "agent-2", "agent-3"} {
		agent := &store.Agent{
			ID:      name,
			Slug:    name,
			Name:    name,
			GroveID: grove.ID,
			Phase:   string(state.PhaseRunning),
		}
		if i == 2 {
			// agent-3 is already stopped
			agent.Phase = string(state.PhaseStopped)
		}
		require.NoError(t, s.CreateAgent(ctx, agent))
	}

	t.Run("stops all running agents", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/stop-all", nil)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp StopAllAgentsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

		assert.Equal(t, 2, resp.Stopped)
		assert.Equal(t, 0, resp.Failed)
		assert.Equal(t, 2, resp.Total)

		// Verify agents are stopped in store
		for _, name := range []string{"agent-1", "agent-2"} {
			agent, err := s.GetAgent(ctx, name)
			require.NoError(t, err)
			assert.Equal(t, string(state.PhaseStopped), agent.Phase)
		}
	})

	t.Run("returns empty when no running agents", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/stop-all", nil)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp StopAllAgentsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

		assert.Equal(t, 0, resp.Total)
	})

	t.Run("requires POST method", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodGet, "/api/v1/agents/stop-all", nil)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("requires authentication", func(t *testing.T) {
		rec := doRequestNoAuth(t, srv, http.MethodPost, "/api/v1/agents/stop-all", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestStopAllAgents_GroveScoped(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Create two groves
	grove1 := &store.Grove{ID: "grove-1", Name: "Grove 1", Slug: "grove-1"}
	grove2 := &store.Grove{ID: "grove-2", Name: "Grove 2", Slug: "grove-2"}
	require.NoError(t, s.CreateGrove(ctx, grove1))
	require.NoError(t, s.CreateGrove(ctx, grove2))

	// Create running agents in both groves
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{
		ID: "g1-agent-1", Slug: "g1-agent-1", Name: "G1 Agent 1",
		GroveID: grove1.ID, Phase: string(state.PhaseRunning),
	}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{
		ID: "g1-agent-2", Slug: "g1-agent-2", Name: "G1 Agent 2",
		GroveID: grove1.ID, Phase: string(state.PhaseRunning),
	}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{
		ID: "g2-agent-1", Slug: "g2-agent-1", Name: "G2 Agent 1",
		GroveID: grove2.ID, Phase: string(state.PhaseRunning),
	}))

	t.Run("stops only agents in scoped grove", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/groves/grove-1/agents/stop-all", nil)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp StopAllAgentsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

		assert.Equal(t, 2, resp.Stopped)
		assert.Equal(t, 0, resp.Failed)
		assert.Equal(t, 2, resp.Total)

		// Verify grove-1 agents are stopped
		a1, _ := s.GetAgent(ctx, "g1-agent-1")
		assert.Equal(t, string(state.PhaseStopped), a1.Phase)

		// Verify grove-2 agent is still running
		a2, _ := s.GetAgent(ctx, "g2-agent-1")
		assert.Equal(t, string(state.PhaseRunning), a2.Phase)
	})
}

func TestStopAllAgents_ScopeCapabilities(t *testing.T) {
	srv, _ := testServer(t)

	// The stop_all action should appear in scope capabilities for admin users
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/agents", nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Capabilities *Capabilities `json:"_capabilities"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Capabilities)
	assert.Contains(t, resp.Capabilities.Actions, "stop_all")
}
