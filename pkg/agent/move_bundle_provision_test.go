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

//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvision_MoveBundleWorkspaceSourceLeavesWorkspaceEmpty(t *testing.T) {
	fx := newMoveAbsentFixture(t, "docker", false)
	opts := moveAbsentOpts(fx.projectPath, "bundle-target", false)
	opts.MoveReincarnationID = "rid-bundle-1"
	opts.WorkspaceSource = api.WorkspaceSourceMoveBundle
	opts.GitClone = &api.GitCloneConfig{
		URL:    "https://example.com/org/repo.git",
		Branch: "main",
	}

	cfg, err := fx.mgr.Provision(context.Background(), opts)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	agentDir := filepath.Join(fx.projectPath, "agents", "bundle-target")
	wsDir := filepath.Join(agentDir, "workspace")
	homeDir := config.GetAgentHomePath(fx.projectPath, "bundle-target")

	// Move marker was written in agentDir (not inside workspace).
	assertMarker(t, agentDir, "rid-bundle-1")
	assert.NoFileExists(t, filepath.Join(wsDir, MoveMarkerFile))

	// Workspace directory exists and is strictly empty (no clone, no .git, no empty-repo init).
	wsEntries, err := os.ReadDir(wsDir)
	require.NoError(t, err)
	assert.Empty(t, wsEntries, "move-bundle provision must leave workspace directory empty")

	// Home directory was rendered normally.
	homeEntries, err := os.ReadDir(homeDir)
	require.NoError(t, err)
	assert.NotEmpty(t, homeEntries, "move-bundle provision must render home normally")
}
