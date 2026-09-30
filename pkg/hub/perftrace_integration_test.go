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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// perfTraceFixture seeds one project and N agents, owned by a dedicated
// user, for exercising listProjectAgents/listAgents under perf tracing.
// Deliberately does not reuse bypassAgentsFixture/pm1 fixtures elsewhere in
// this package: those carry setup (broker providers, multi-project
// cross-tenant agents) unrelated to what this test needs and would make it
// harder to see that a red assertion here is actually about tracing, not
// about unrelated fixture machinery.
type perfTraceFixture struct {
	srv     *Server
	store   store.Store
	project *store.Project
}

func setupPerfTraceFixture(t *testing.T, agentCount int) *perfTraceFixture {
	t.Helper()
	srv, s := testServer(t)
	ctx := context.Background()

	owner := &store.User{
		ID:          tid(t.Name() + "-owner"),
		Email:       t.Name() + "-owner@example.com",
		DisplayName: "Perf Trace Owner",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, owner))

	project := &store.Project{
		ID:      tid(t.Name() + "-project"),
		Name:    "Perf Trace Project",
		Slug:    t.Name() + "-project",
		OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	for i := 0; i < agentCount; i++ {
		suffix := fmt.Sprintf("%s-agent-%d", t.Name(), i)
		a := &store.Agent{
			ID:        tid(suffix),
			Slug:      tid(suffix + "-slug"),
			Name:      "agent",
			ProjectID: project.ID,
			Phase:     "running",
			CreatedBy: owner.ID,
			OwnerID:   owner.ID,
		}
		require.NoError(t, s.CreateAgent(ctx, a))
	}

	return &perfTraceFixture{srv: srv, store: s, project: project}
}

func doRequestWithHeaders(srv *Server, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+testDevToken)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// stripServerTime removes ListAgentsResponse.ServerTime (set fresh from
// time.Now() on every call, so it never matches between two separate
// requests) so the rest of the body can be compared for an exact match.
func stripServerTime(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &m))
	delete(m, "serverTime")
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

// TestPerfTraceDisabledByDefaultNoHeadersNoBehaviorChange is the
// ptone/scion#2392 acceptance criterion "disabled/default behavior are
// documented" made concrete: with SCION_HUB_PERF_TRACE unset (the state
// every other test in this package runs in, and production's default),
// requesting the opt-in trace header back gets nothing extra, and the
// ordinary response is unaffected.
func TestPerfTraceDisabledByDefaultNoHeadersNoBehaviorChange(t *testing.T) {
	withPerfTraceEnabled(t, false)
	f := setupPerfTraceFixture(t, 3)

	rec := doRequestWithHeaders(f.srv, http.MethodGet,
		"/api/v1/projects/"+f.project.ID+"/agents",
		map[string]string{HeaderPerfTraceRequest: "1"})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get(HeaderPerfTracePhases))
	require.Empty(t, rec.Header().Get(HeaderPerfTraceStoreCalls))
	require.Empty(t, rec.Header().Get(HeaderPerfTraceDecisions))
}

// TestPerfTraceEnabledWithoutOptInHeaderStillNoHeaders verifies the
// double-gate: SCION_HUB_PERF_TRACE=1 alone does not hand trace data back
// to every caller, only to requests that also opt in per-request.
func TestPerfTraceEnabledWithoutOptInHeaderStillNoHeaders(t *testing.T) {
	withPerfTraceEnabled(t, true)
	f := setupPerfTraceFixture(t, 3)

	rec := doRequestWithHeaders(f.srv, http.MethodGet,
		"/api/v1/projects/"+f.project.ID+"/agents", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get(HeaderPerfTracePhases))
	require.Empty(t, rec.Header().Get(HeaderPerfTraceStoreCalls))
	require.Empty(t, rec.Header().Get(HeaderPerfTraceDecisions))
}

// TestPerfTraceProjectAgentsListRecordsPhasesAndDecisions is the primary
// ptone/scion#2392 acceptance-criteria test: a repeatable workload against
// listProjectAgents, with tracing enabled and opted into, records phase
// timings and at least one authorization decision, without changing the
// response body.
func TestPerfTraceProjectAgentsListRecordsPhasesAndDecisions(t *testing.T) {
	withPerfTraceEnabled(t, true)
	f := setupPerfTraceFixture(t, 5)

	baseline := doRequestWithHeaders(f.srv, http.MethodGet,
		"/api/v1/projects/"+f.project.ID+"/agents", nil)
	require.Equal(t, http.StatusOK, baseline.Code)

	traced := doRequestWithHeaders(f.srv, http.MethodGet,
		"/api/v1/projects/"+f.project.ID+"/agents",
		map[string]string{HeaderPerfTraceRequest: "1"})
	require.Equal(t, http.StatusOK, traced.Code)

	// Tracing must not change the response body -- other than serverTime,
	// which is set fresh from time.Now() on every call regardless of
	// tracing and would never match between two separate requests.
	require.JSONEq(t, stripServerTime(t, baseline.Body.String()), stripServerTime(t, traced.Body.String()))

	phases := traced.Header().Get(HeaderPerfTracePhases)
	require.Contains(t, phases, "db_fetch=")
	require.Contains(t, phases, "enrich=")
	require.Contains(t, phases, "capabilities_batch=")
	require.Contains(t, phases, "capabilities_scope=")
	// "serialize" cannot appear here: writePerfTraceHeaders runs before
	// writeJSON, i.e. before the serialize phase has ended. It is still
	// captured in the perf_trace log line emitted by perfTraceMiddleware,
	// which observes the whole request.
	require.NotContains(t, phases, "serialize=")

	decisions := traced.Header().Get(HeaderPerfTraceDecisions)
	require.Contains(t, decisions, "count=")
	require.NotContains(t, decisions, "count=0,")
}

// TestPerfTraceListAgentsRecordsAllPhases exercises the global endpoint
// (handlers_agents_core.go's listAgents), which has two phases
// listProjectAgents does not: authz_scope_resolve and messageability.
func TestPerfTraceListAgentsRecordsAllPhases(t *testing.T) {
	withPerfTraceEnabled(t, true)
	f := setupPerfTraceFixture(t, 4)

	rec := doRequestWithHeaders(f.srv, http.MethodGet,
		"/api/v1/agents?projectId="+f.project.ID,
		map[string]string{HeaderPerfTraceRequest: "1"})
	require.Equal(t, http.StatusOK, rec.Code)

	phases := rec.Header().Get(HeaderPerfTracePhases)
	require.Contains(t, phases, "authz_scope_resolve=")
	require.Contains(t, phases, "db_fetch=")
	require.Contains(t, phases, "enrich=")
	require.Contains(t, phases, "capabilities_batch=")
	require.Contains(t, phases, "messageability=")
	require.Contains(t, phases, "capabilities_scope=")
}

// TestPerfTraceStoreCallCountsScaleWithAgentCount is a repeatable-workload
// check for the acceptance criterion "A repeatable 25/100/500-agent
// workload records phase timings [and] store calls" -- and, unexpectedly
// during development of this test, direct confirmation of ptone/scion#2367's
// central diagnosis from inside a unit test rather than only from the
// perf/2393 harness's wall-clock measurements.
//
// The original hypothesis here was that these counts would stay flat across
// agent counts for the dev/super-admin test identity, on the theory that a
// super-admin bypass would short-circuit before reaching the per-item
// authorization-hot-path calls ComputeCapabilitiesBatch makes per (resource,
// action) pair. That hypothesis was wrong: even the dev identity's
// evaluation reaches GetEffectiveGroups / ListRoleBindingsForPrincipals /
// GetRoleDefinitionsByIDs / ListAccessConstraints once per agent (2 agents:
// 21 calls per method; 20 agents: 165 calls per method -- see the exact
// counts asserted below), which is exactly the "once per agent per list
// request rather than once per request" amplification the root tracker
// describes. This test now asserts that scaling directly, pinned to the
// specific counts this fixture produces so a future change to either the
// fixture or the authorization path shows up as a clear diff here rather
// than a silently-adjusted tolerance.
func TestPerfTraceStoreCallCountsScaleWithAgentCount(t *testing.T) {
	withPerfTraceEnabled(t, true)

	countsFor := func(agentCount int) string {
		f := setupPerfTraceFixture(t, agentCount)
		rec := doRequestWithHeaders(f.srv, http.MethodGet,
			"/api/v1/projects/"+f.project.ID+"/agents",
			map[string]string{HeaderPerfTraceRequest: "1"})
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Header().Get(HeaderPerfTraceStoreCalls)
	}

	const small, large = 2, 20
	smallCounts := countsFor(small)
	largeCounts := countsFor(large)

	require.Equal(t,
		"GetEffectiveGroups=21,GetRoleDefinitionsByIDs=21,ListAccessConstraints=21,ListRoleBindingsForPrincipals=21",
		smallCounts, "store-call counts at %d agents", small)
	require.Equal(t,
		"GetEffectiveGroups=165,GetRoleDefinitionsByIDs=165,ListAccessConstraints=165,ListRoleBindingsForPrincipals=165",
		largeCounts, "store-call counts at %d agents", large)
	require.NotEqual(t, smallCounts, largeCounts,
		"store-call counts must scale with agent count -- if this ever starts "+
			"failing because the counts became equal, that is #2376/#2377's "+
			"authorization-input-reuse work succeeding, and this test's fixed "+
			"expected counts (not the NotEqual check itself) should be updated "+
			"to match the new, flat behavior")
}
