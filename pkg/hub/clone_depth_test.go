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

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Row 6 (task #49): Default git-anchored project agent create → Depth: 1.
// This is the withdrawal condition: if any change moves the default clone
// depth for an ordinary agent, stop and report before implementing.
func TestCloneDepth_Row6_DefaultGitAnchoredDepthOne(t *testing.T) {
	srv, _ := testServer(t)

	project := &store.Project{
		ID:        tid("project-git"),
		Name:      "Git Project",
		Slug:      "git-project",
		GitRemote: "https://github.com/org/repo.git",
		Labels: map[string]string{
			// scion.dev/clone-url and scion.dev/default-branch omitted —
			// populateAgentConfig falls back to GitRemote and "main".
		},
	}

	agent := &store.Agent{
		ID:            tid("agent-row6"),
		AppliedConfig: &store.AgentAppliedConfig{},
	}

	srv.populateAgentConfig(context.Background(), agent, project, nil)

	gc := agent.AppliedConfig.GitClone
	if gc == nil {
		t.Fatal("expected GitClone to be populated for a git-anchored project")
	}
	if gc.Depth != 1 {
		t.Errorf("default clone depth = %d, want 1 — the default has moved, which is a withdrawal condition (task #49 row 6)", gc.Depth)
	} else {
		t.Logf("CONFIRMED: default git-anchored clone depth = 1 (unchanged)")
	}
}
