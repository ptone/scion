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

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// TestGetAgent_RejectsNameOutsideSelectedRoot is the regression anchor for
// the containment check added to GetAgent, immediately after agentDir is
// computed and before the stale-directory branch that removes it. GetAgent's
// caller is not always a validated path element (defense in depth for
// createAgent's own check), so GetAgent must independently confirm agentDir
// is still a direct child of the root config.GetAgentDir selected it under
// before doing anything with it.
//
// This matters under both root selections config.SelectAgentsRoot can
// return -- the external agents dir when the caller is in shared-workspace
// mode and a project-id marker exists, or <projectDir>/agents otherwise --
// because a check hardcoded to one of them would be wrong for the other
// (see config.SelectAgentsRoot's doc comment). Each subtest below plants a
// sentinel directory exactly where a name of "../sibling" would land for
// that root selection, and confirms GetAgent errors out before touching it.
func TestGetAgent_RejectsNameOutsideSelectedRoot(t *testing.T) {
	const outOfRootName = "../sibling"

	t.Run("default root (<projectDir>/agents)", func(t *testing.T) {
		tmpDir := t.TempDir()
		scionDir := filepath.Join(tmpDir, "project", ".scion")
		if err := os.MkdirAll(scionDir, 0755); err != nil {
			t.Fatalf("MkdirAll scionDir: %v", err)
		}

		// A "../sibling" name joined onto <scionDir>/agents lands here.
		sentinel := filepath.Join(scionDir, "sibling")
		if err := os.MkdirAll(sentinel, 0755); err != nil {
			t.Fatalf("MkdirAll sentinel: %v", err)
		}
		markerPath := filepath.Join(sentinel, "marker.txt")
		if err := os.WriteFile(markerPath, []byte("keep"), 0644); err != nil {
			t.Fatalf("WriteFile marker: %v", err)
		}

		_, _, _, _, err := GetAgent(context.Background(), outOfRootName, "", "", "", scionDir, "", "", "", "")
		if err == nil {
			t.Fatal("expected GetAgent to reject a name resolving outside the agents root, got nil error")
		}
		if _, statErr := os.Stat(markerPath); statErr != nil {
			t.Fatalf("sentinel must survive untouched, but stat failed: %v", statErr)
		}
	})

	t.Run("external root (shared workspace, project-id marker present)", func(t *testing.T) {
		tmpDir := t.TempDir()
		originalHome := os.Getenv("HOME")
		defer func() { _ = os.Setenv("HOME", originalHome) }()
		_ = os.Setenv("HOME", tmpDir)

		scionDir := filepath.Join(tmpDir, "project", ".scion")
		if err := os.MkdirAll(scionDir, 0755); err != nil {
			t.Fatalf("MkdirAll scionDir: %v", err)
		}
		if err := config.WriteProjectID(scionDir, "11111111-2222-3333-4444-555555555555"); err != nil {
			t.Fatalf("WriteProjectID: %v", err)
		}

		externalAgentsDir, err := config.GetGitProjectExternalAgentsDir(scionDir)
		if err != nil {
			t.Fatalf("GetGitProjectExternalAgentsDir: %v", err)
		}
		if externalAgentsDir == "" {
			t.Fatal("expected a non-empty external agents dir once a project-id marker is present")
		}

		// A "../sibling" name joined onto the external agents dir lands here,
		// a sibling of "agents" under the external .scion directory -- a
		// different location than the default-root case above, which is
		// exactly why the containment check must recompute the selected
		// root rather than assume <projectDir>/agents.
		sentinel := filepath.Join(filepath.Dir(externalAgentsDir), "sibling")
		if err := os.MkdirAll(sentinel, 0755); err != nil {
			t.Fatalf("MkdirAll sentinel: %v", err)
		}
		markerPath := filepath.Join(sentinel, "marker.txt")
		if err := os.WriteFile(markerPath, []byte("keep"), 0644); err != nil {
			t.Fatalf("WriteFile marker: %v", err)
		}

		ctx := api.ContextWithSharedWorkspace(context.Background())
		_, _, _, _, err = GetAgent(ctx, outOfRootName, "", "", "", scionDir, "", "", "", "")
		if err == nil {
			t.Fatal("expected GetAgent to reject a name resolving outside the selected (external) agents root, got nil error")
		}
		if _, statErr := os.Stat(markerPath); statErr != nil {
			t.Fatalf("sentinel must survive untouched, but stat failed: %v", statErr)
		}
	})
}
