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
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/stretchr/testify/require"
)

// TestApplyAgentUpdate_NameReplayStaysUnderAgentsDir is the regression anchor
// for the PATCH-name validation added to applyAgentUpdate
// (handlers_agents_core.go). Every hub->broker create/replay dispatch
// (buildCreateRequest, httpdispatcher.go:527) currently forwards agent.Name
// as the broker-side agent identifier, and the broker computes the agent's
// on-disk directory from that identifier via config.GetAgentDir
// (pkg/config/project_marker.go:328), which is a plain filepath.Join with no
// containment check of its own.
//
// Before the validation added here, a PATCH could set Name to a value whose
// components climb out of the "agents" directory, and that value would flow
// unchanged through buildCreateRequest into the computed directory. This
// test proves reachability by recomputing that same directory with
// config.GetAgentDir against the value applyAgentUpdate actually persisted —
// it does not call the broker's GetAgent/Manager path, so it never exercises
// that directory's filesystem side effects.
//
// On the base this guards, the PATCH below succeeds and the recomputed
// directory falls outside the sample agents dir, so this test fails. With
// the validation in place, the PATCH is rejected and the agent's persisted
// Name is unchanged, so the recomputed directory stays inside it.
func TestApplyAgentUpdate_NameReplayStaysUnderAgentsDir(t *testing.T) {
	f := projectAgentAuthzSetup(t)
	ctx := context.Background()

	dispatcher := NewHTTPAgentDispatcher(f.store, false, slog.Default())

	const patchedName = "../../sibling-project"
	rec := doRequestAsUser(t, f.srv, f.member, http.MethodPatch, f.targetPath(),
		map[string]interface{}{"name": patchedName})

	// Re-derive the value that would be dispatched to the broker for this
	// agent on a subsequent replay (finalize-env, reconcile, reprovision,
	// bootstrap upload, scheduler — see the callers listed at
	// buildCreateRequest, httpdispatcher.go:519), regardless of whether the
	// PATCH above was accepted.
	got, err := f.store.GetAgent(ctx, f.target.ID)
	require.NoError(t, err)

	req, err := dispatcher.buildCreateRequest(ctx, got, "test")
	require.NoError(t, err)

	sampleProjectDir := t.TempDir()
	agentsDir := filepath.Join(sampleProjectDir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))

	agentDir := config.GetAgentDir(sampleProjectDir, req.Name, false)
	rel, err := filepath.Rel(agentsDir, agentDir)
	require.NoError(t, err)

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("PATCH response %d; dispatched name %q recomputes to %s, outside %s",
			rec.Code, req.Name, agentDir, agentsDir)
	}
}
