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
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
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

func TestMoveBundleHomeRecord_WriteReadAndExpand(t *testing.T) {
	dir := t.TempDir()

	// Missing record fails closed.
	_, err := ReadMoveBundleHomeRecord(dir)
	assert.ErrorIs(t, err, ErrMoveBundleHomeRecordMissing)

	// Empty UnixUsername cannot build a record.
	_, err = BuildMoveBundleHomeRecord(runtime.RunConfig{})
	assert.ErrorIs(t, err, ErrMoveBundleHomeRecordInvalid)

	// Valid record persists resolved UnixUsername, HomeAbs, file secrets, and auth files.
	cfg := runtime.RunConfig{
		UnixUsername: "scion",
		ResolvedSecrets: []api.ResolvedSecret{
			{Name: "k1", Type: "file", Target: "~/.config/app/key.pem"},
			{Name: "k2", Type: "file", Target: "/home/scion/.ssh/id_ed25519"},
			{Name: "k3", Type: "file", Target: "/etc/outside.pem"},
		},
		ResolvedAuth: &api.ResolvedAuth{
			Files: []api.FileMapping{
				{SourcePath: "/host/a", ContainerPath: "~/.claude/.credentials.json"},
			},
		},
	}
	require.NoError(t, WriteMoveBundleHomeRecord(dir, cfg))

	rec, err := ReadMoveBundleHomeRecord(dir)
	require.NoError(t, err)
	assert.Equal(t, "scion", rec.UnixUsername)
	assert.Equal(t, "/home/scion", rec.HomeAbs)
	assert.Contains(t, rec.HomeExcludes, ".config/app/key.pem")
	assert.Contains(t, rec.HomeExcludes, ".ssh/id_ed25519")
	assert.Contains(t, rec.HomeExcludes, ".claude/.credentials.json")
	assert.NotContains(t, rec.HomeExcludes, "etc/outside.pem")

	// ExpandHomeRelTarget works directly against homeAbs (including /root) and rejects /home.
	rel, ok := ExpandHomeRelTarget("~/.gemini/antigravity-cli/token", "/home/scion")
	assert.True(t, ok)
	assert.Equal(t, ".gemini/antigravity-cli/token", rel)
	rel, ok = ExpandHomeRelTarget("~/.copilot/config.json", "/root")
	assert.True(t, ok)
	assert.Equal(t, ".copilot/config.json", rel)
	_, ok = ExpandHomeRelTarget("~/.kube/config", "/home")
	assert.False(t, ok, "/home is not a user home")
}

func TestMoveBundleHomeRecord_FailedWriteRemovesStaleRecord(t *testing.T) {
	dir := t.TempDir()

	// Seed a valid record from an earlier start.
	require.NoError(t, WriteMoveBundleHomeRecord(dir, runtime.RunConfig{
		UnixUsername: "scion",
		ResolvedSecrets: []api.ResolvedSecret{
			{Name: "old", Type: "file", Target: "~/.config/old/secret.key"},
		},
	}))
	_, err := ReadMoveBundleHomeRecord(dir)
	require.NoError(t, err)

	// A subsequent start whose RunConfig cannot produce a valid record must fail
	// AND remove the stale record so export fails closed instead of using stale excludes.
	err = WriteMoveBundleHomeRecord(dir, runtime.RunConfig{})
	assert.ErrorIs(t, err, ErrMoveBundleHomeRecordInvalid)

	_, err = ReadMoveBundleHomeRecord(dir)
	assert.ErrorIs(t, err, ErrMoveBundleHomeRecordMissing, "stale record must be removed when a later write fails")
}

func TestStart_WritesMoveBundleHomeRecord(t *testing.T) {
	fx := newMoveAbsentFixture(t, "docker", false)
	opts := moveAbsentOpts(fx.projectPath, "start-rec-agent", true)
	opts.ResolvedSecrets = []api.ResolvedSecret{
		{Name: "my_file_secret", Type: "file", Target: "~/.config/app/secret.key", Value: "top-secret-value"},
	}

	_, err := fx.mgr.Start(context.Background(), opts)
	require.NoError(t, err)

	agentDir := filepath.Join(fx.projectPath, "agents", "start-rec-agent")
	rec, err := ReadMoveBundleHomeRecord(agentDir)
	require.NoError(t, err, "AgentManager.Start must persist the move-bundle home record")
	assert.NotEmpty(t, rec.UnixUsername)
	assert.NotEmpty(t, rec.HomeAbs)
	assert.Contains(t, rec.HomeExcludes, ".config/app/secret.key")
}

func TestResolveMoveBundleAgentDirs_MatchesProvisionAgentStateDir(t *testing.T) {
	fx := newMoveAbsentFixture(t, "docker", false)
	opts := api.StartOptions{
		Name:        "parity-agent",
		ProjectPath: fx.projectPath,
	}
	wantAgentDir, err := provisionAgentStateDir(context.Background(), opts)
	require.NoError(t, err)
	wantProjectDir, err := config.GetResolvedProjectDir(fx.projectPath)
	require.NoError(t, err)

	got, err := ResolveMoveBundleAgentDirs(context.Background(), "docker", opts)
	require.NoError(t, err)
	assert.Equal(t, wantAgentDir, got.AgentDir)
	assert.Equal(t, filepath.Join(wantAgentDir, "workspace"), got.WorkspaceDir)
	assert.Equal(t, config.GetAgentHomePath(wantProjectDir, "parity-agent"), got.HomeDir)
	assert.False(t, got.SharedWorkspace)
}
