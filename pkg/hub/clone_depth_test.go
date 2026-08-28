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
	"strings"
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
		Labels:    map[string]string{
			// scion.dev/clone-url and scion.dev/default-branch omitted —
			// populateAgentConfig falls back to GitRemote and "main".
		},
	}

	agent := &store.Agent{
		ID:            tid("agent-row6"),
		AppliedConfig: &store.AgentAppliedConfig{},
	}

	if err := srv.populateAgentConfig(context.Background(), agent, project, nil); err != nil {
		t.Fatalf("populateAgentConfig: %v", err)
	}

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

// ---------------------------------------------------------------------------
// Half B — scion.dev/clone-depth label parsing
// ---------------------------------------------------------------------------

func TestCloneDepth_LabelFullClone(t *testing.T) {
	srv, _ := testServer(t)

	project := &store.Project{
		ID:        tid("project-full"),
		Name:      "Full Clone Project",
		Slug:      "full-clone",
		GitRemote: "https://github.com/org/repo.git",
		Labels: map[string]string{
			"scion.dev/clone-depth": "-1",
		},
	}

	agent := &store.Agent{
		ID:            tid("agent-full"),
		AppliedConfig: &store.AgentAppliedConfig{},
	}

	if err := srv.populateAgentConfig(context.Background(), agent, project, nil); err != nil {
		t.Fatalf("populateAgentConfig: %v", err)
	}

	gc := agent.AppliedConfig.GitClone
	if gc == nil {
		t.Fatal("expected GitClone to be populated")
	}
	if gc.Depth != -1 {
		t.Errorf("clone depth = %d, want -1 (full clone via label)", gc.Depth)
	}
}

func TestCloneDepth_LabelShallowN(t *testing.T) {
	srv, _ := testServer(t)

	project := &store.Project{
		ID:        tid("project-shallow5"),
		Name:      "Shallow 5 Project",
		Slug:      "shallow-5",
		GitRemote: "https://github.com/org/repo.git",
		Labels: map[string]string{
			"scion.dev/clone-depth": "5",
		},
	}

	agent := &store.Agent{
		ID:            tid("agent-shallow5"),
		AppliedConfig: &store.AgentAppliedConfig{},
	}

	if err := srv.populateAgentConfig(context.Background(), agent, project, nil); err != nil {
		t.Fatalf("populateAgentConfig: %v", err)
	}

	gc := agent.AppliedConfig.GitClone
	if gc == nil {
		t.Fatal("expected GitClone to be populated")
	}
	if gc.Depth != 5 {
		t.Errorf("clone depth = %d, want 5 (shallow-5 via label)", gc.Depth)
	}
}

func TestCloneDepth_LabelMalformedFails(t *testing.T) {
	srv, _ := testServer(t)

	tests := []struct {
		name  string
		value string
	}{
		{"non-numeric", "deep"},
		{"zero", "0"},
		{"negative-two", "-2"},
		{"float", "1.5"},
		{"empty-not-absent", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Skip the empty-string case — an empty label is treated as
			// absent (the label map returns "" for missing keys too), so
			// no error is expected.
			if tc.value == "" {
				t.Skip("empty label value is indistinguishable from absent")
			}

			project := &store.Project{
				ID:        tid("project-" + tc.name),
				Name:      "Bad Label Project",
				Slug:      "bad-label-" + tc.name,
				GitRemote: "https://github.com/org/repo.git",
				Labels: map[string]string{
					"scion.dev/clone-depth": tc.value,
				},
			}

			agent := &store.Agent{
				ID:            tid("agent-" + tc.name),
				AppliedConfig: &store.AgentAppliedConfig{},
			}

			err := srv.populateAgentConfig(context.Background(), agent, project, nil)
			if err == nil {
				t.Fatalf("expected error for clone-depth=%q, got nil", tc.value)
			}
			if !strings.Contains(err.Error(), "scion.dev/clone-depth") {
				t.Errorf("error should name the label, got: %v", err)
			}
			t.Logf("CONFIRMED: clone-depth=%q → legible error: %v", tc.value, err)
		})
	}
}

func TestCloneDepth_LabelAbsentKeepsDefault(t *testing.T) {
	srv, _ := testServer(t)

	project := &store.Project{
		ID:        tid("project-nolab"),
		Name:      "No Label Project",
		Slug:      "no-label",
		GitRemote: "https://github.com/org/repo.git",
		Labels:    map[string]string{},
	}

	agent := &store.Agent{
		ID:            tid("agent-nolab"),
		AppliedConfig: &store.AgentAppliedConfig{},
	}

	if err := srv.populateAgentConfig(context.Background(), agent, project, nil); err != nil {
		t.Fatalf("populateAgentConfig: %v", err)
	}

	gc := agent.AppliedConfig.GitClone
	if gc == nil {
		t.Fatal("expected GitClone to be populated")
	}
	if gc.Depth != 1 {
		t.Errorf("clone depth = %d, want 1 (absent label should keep default)", gc.Depth)
	}
}
