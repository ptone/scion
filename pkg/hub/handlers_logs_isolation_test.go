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
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// Project-isolation coverage for the three streaming log handlers, which had no
// isolation check at all: handleAgentCloudLogsStream, handleAgentMessageLogsStream
// and handleProjectMessageLogsStream. Their non-streaming twins each had one, so
// this is copy-drift — the streaming variants were written from the others and
// the check was dropped.
//
// The assertion is 404, not 403. A caller outside the project must not be able
// to distinguish "exists but forbidden" from "does not exist", so the isolation
// check runs before authorization and answers NotFound.
//
// Test naming: everything file-local is prefixed logsIso so it cannot collide
// with the parallel conversion work in other handler files.

type logsIsoFixture struct {
	srv    *Server
	store  store.Store
	owner  *store.User
	projA  *store.Project
	projB  *store.Project
	target *store.Agent
	caller *store.Agent
}

func logsIsoSetup(t *testing.T) *logsIsoFixture {
	t.Helper()

	s, err := newTestStore(":memory:")
	if err != nil {
		t.Skipf("skipping: test store unavailable (%v)", err)
	}
	require.NoError(t, s.Migrate(context.Background()))

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-id")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// Rule 9: logQueryService is NOT reliably nil in a test process. When the
	// environment carries a GCP project (GOOGLE_CLOUD_PROJECT), NewServer
	// resolves it and builds a real Cloud Logging client, so a test that relied
	// on the ambient value would exercise the nil -> 501 path in CI and a live
	// client here. Every test in this file sets the field explicitly.
	//
	// A zero-value service is the right seam: all six gated handlers check it
	// for nil, pass, and then return their 404/403 before touching the embedded
	// logadmin/logv2 clients. Verified empirically — an allowed caller panics at
	// logquery.go:135 and :266 with a nil receiver, which is exactly why the
	// positive paths are covered at the authz layer and not here.
	srv.logQueryService = &LogQueryService{}

	ctx := context.Background()
	f := &logsIsoFixture{srv: srv, store: s}

	f.owner = &store.User{
		ID:          tid("logsiso-owner"),
		Email:       "logsiso-owner@example.com",
		DisplayName: "Logs Iso Owner",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.owner))

	f.projA = &store.Project{
		ID:      tid("logsiso-pa"),
		Name:    "Logs Iso PA",
		Slug:    "logsiso-pa",
		OwnerID: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projA))

	f.projB = &store.Project{
		ID:      tid("logsiso-pb"),
		Name:    "Logs Iso PB",
		Slug:    "logsiso-pb",
		OwnerID: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projB))

	mk := func(name, projectID string) *store.Agent {
		a := &store.Agent{
			ID:        tid(name),
			Slug:      tid(name),
			Name:      name,
			ProjectID: projectID,
			Phase:     string(state.PhaseRunning),
			CreatedBy: f.owner.ID,
			OwnerID:   f.owner.ID,
			Ancestry:  []string{f.owner.ID},
		}
		require.NoError(t, s.CreateAgent(ctx, a))
		return a
	}
	// target lives in project A; caller is an agent in project B and has no
	// relationship to it — not a peer, not an ancestor.
	f.target = mk("logsiso-target", f.projA.ID)
	f.caller = mk("logsiso-caller", f.projB.ID)

	return f
}

// asCallerAgent issues a GET carrying an agent token for the project-B agent.
func (f *logsIsoFixture) asCallerAgent(t *testing.T, path string) int {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(f.caller.ID, f.caller.ProjectID, nil, nil)
	require.NoError(t, err)
	return doRequestWithAgentToken(t, f.srv, "GET", path, nil, tok).Code
}

// TestLogsIso_StreamingHandlersDenyCrossProjectAgent covers the three streaming
// handlers that previously had no isolation check. Before this change each one
// admitted an agent from an unrelated project: the only guard present was the
// user-identity check, which is skipped in silence for an agent caller (#591),
// so the request proceeded to open a log stream for another project's resource.
func TestLogsIso_StreamingHandlersDenyCrossProjectAgent(t *testing.T) {
	f := logsIsoSetup(t)

	tests := []struct {
		name string
		path string
	}{
		{
			name: "agent cloud logs stream",
			path: "/api/v1/agents/" + f.target.ID + "/cloud-logs/stream",
		},
		{
			name: "agent message logs stream",
			path: "/api/v1/agents/" + f.target.ID + "/message-logs/stream",
		},
		{
			name: "project message logs stream",
			path: "/api/v1/projects/" + f.projA.ID + "/message-logs/stream",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code := f.asCallerAgent(t, tc.path)
			require.Equal(t, 404, code,
				"a cross-project agent must get 404, not 403: 403 would confirm the resource exists")
		})
	}
}

// TestLogsIso_NonStreamingHandlersDenyCrossProjectAgent pins the behaviour of
// the twins that already had the check, so the streaming and non-streaming
// variants cannot drift apart again without a test failing.
func TestLogsIso_NonStreamingHandlersDenyCrossProjectAgent(t *testing.T) {
	f := logsIsoSetup(t)

	tests := []struct {
		name string
		path string
	}{
		{
			name: "agent logs",
			path: "/api/v1/agents/" + f.target.ID + "/logs",
		},
		{
			name: "agent cloud logs",
			path: "/api/v1/agents/" + f.target.ID + "/cloud-logs",
		},
		{
			name: "agent message logs",
			path: "/api/v1/agents/" + f.target.ID + "/message-logs",
		},
		{
			name: "project message logs",
			path: "/api/v1/projects/" + f.projA.ID + "/message-logs",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code := f.asCallerAgent(t, tc.path)
			require.Equal(t, 404, code,
				"a cross-project agent must get 404, not 403")
		})
	}
}

// TestLogsIso_SameProjectAgentPassesIsolation establishes that the new checks
// deny on project boundary rather than denying agents wholesale. An agent
// reading its own project's resources must get past isolation.
//
// It asserts "not 404" rather than a success code deliberately: passing
// isolation hands the request to the authorization layer, and what happens next
// is that layer's business, tested there. Pinning a success code here would
// couple this test to the read-baseline policy and to the log backend.
func TestLogsIso_SameProjectAgentPassesIsolation(t *testing.T) {
	f := logsIsoSetup(t)

	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	// A caller inside project A, asking about project A's own agent.
	tok, err := svc.GenerateAgentToken(f.target.ID, f.target.ProjectID, nil, nil)
	require.NoError(t, err)

	code := doRequestWithAgentToken(t, f.srv, "GET",
		"/api/v1/agents/"+f.target.ID+"/logs", nil, tok).Code

	require.NotEqual(t, 404, code,
		"an agent in the resource's own project must pass the isolation check; "+
			"a 404 here would mean the check denies on identity kind rather than on project")
}
