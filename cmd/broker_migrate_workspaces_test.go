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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

const migrateWorkspacesTestProjectID = "4f1d2c3b-5a69-4e70-8b91-a2b3c4d5e6f7"

// The migration is an operator step on the broker host: it is not
// available in agent mode, and it is refused on Windows.
func TestBrokerMigrateWorkspaces_HumanOnlyAndUnixOnly(t *testing.T) {
	const path = "runtime-broker.migrate-workspaces"
	require.NotNil(t, resolveCommandPath(rootCmd, path), "the command exists in the real tree")
	assert.False(t, agentAllowed[path], "not in agentAllowed")
	assert.False(t, agentAllowed["runtime-broker"], "its parent is not in agentAllowed")
	assert.False(t, windowsAllowed[path], "not in windowsAllowed")

	assert.False(t, commandRequiresProject(resolveCommandPath(rootCmd, path)),
		"its projects come from --project or the shared layout, never the current directory")

	t.Run("agent mode", func(t *testing.T) {
		root := &cobra.Command{Use: "scion"}
		root.AddCommand(cloneCommandShape(resolveCommandPath(rootCmd, "runtime-broker")))
		t.Setenv("SCION_CLI_MODE", "agent")
		applyModeRestrictions(root, resolveMode())
		assert.Nil(t, resolveCommandPath(root, path), "removed in agent mode")
	})

	t.Run("windows", func(t *testing.T) {
		for _, args := range [][]string{
			{"runtime-broker", "migrate-workspaces", "--dry-run"},
			{"broker", "migrate-workspaces", "--json", "--project", "x"},
		} {
			root, ran := gateTestTree(t)
			applyPlatformRestrictions(root, "windows")
			root.SetArgs(args)
			err := root.Execute()
			require.Error(t, err)
			assert.True(t, errors.Is(err, errUnsupportedOnWindows), "got %v", err)
			assert.Empty(t, *ran, "a gated command must not run")
		}
	})
}

// migrateWorkspacesTestProject makes a shared project configuration
// directory <tmp>/<id>/.scion and returns the project root.
func migrateWorkspacesTestProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), migrateWorkspacesTestProjectID)
	scionDir := filepath.Join(root, config.DotScion)
	require.NoError(t, os.MkdirAll(scionDir, 0o755))
	require.NoError(t, config.WriteProjectID(scionDir, migrateWorkspacesTestProjectID))
	return root
}

type migrateWorkspacesFake struct {
	got     []agent.WorkspaceMigrationOptions
	results []agent.WorkspaceMigrationResult
	ids     []string
}

func (f *migrateWorkspacesFake) deps(dirs ...string) migrateWorkspacesDeps {
	return migrateWorkspacesDeps{
		projectDirs: func(context.Context) ([]string, error) { return dirs, nil },
		migrate: func(_ context.Context, opts agent.WorkspaceMigrationOptions) ([]agent.WorkspaceMigrationResult, error) {
			f.got = append(f.got, opts)
			return f.results, nil
		},
		running: func(projectID string) agent.AgentRunningFunc {
			f.ids = append(f.ids, projectID)
			return func(context.Context, string) (bool, error) { return false, nil }
		},
	}
}

func TestMigrateWorkspaces_PassesOptionsAndPrintsJSON(t *testing.T) {
	root := migrateWorkspacesTestProject(t)
	f := &migrateWorkspacesFake{results: []agent.WorkspaceMigrationResult{
		{Agent: "a1", Status: agent.MigrationWouldMigrate, Source: "/local/a1/workspace", Target: "/share/a1/workspace", SourceBytes: 10, SourceEntries: 2},
		{Agent: "a2", Status: agent.MigrationAlreadyMigrated},
	}}
	var out bytes.Buffer
	require.NoError(t, migrateWorkspaces(context.Background(), &out, f.deps(), root, []string{"a1", "a2"}, true, true))

	require.Len(t, f.got, 1)
	assert.Equal(t, filepath.Join(root, config.DotScion), f.got[0].ProjectDir, "the project root resolves to its .scion directory")
	assert.Equal(t, []string{"a1", "a2"}, f.got[0].Agents)
	assert.True(t, f.got[0].DryRun)
	assert.NotNil(t, f.got[0].Running)
	assert.Equal(t, []string{migrateWorkspacesTestProjectID}, f.ids, "the running check is scoped to the project")

	var doc struct {
		DryRun   bool `json:"dry_run"`
		Projects []struct {
			ProjectDir string `json:"project_dir"`
			Results    []struct {
				Agent  string `json:"agent"`
				Status string `json:"status"`
				Bytes  int64  `json:"source_bytes"`
			} `json:"results"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &doc))
	assert.True(t, doc.DryRun)
	require.Len(t, doc.Projects, 1)
	require.Len(t, doc.Projects[0].Results, 2)
	assert.Equal(t, "would_migrate", doc.Projects[0].Results[0].Status)
	assert.Equal(t, int64(10), doc.Projects[0].Results[0].Bytes)
	assert.Equal(t, "already_migrated", doc.Projects[0].Results[1].Status)
}

// A refused or failed agent makes the command fail after printing, so a
// switch run can stop on it; the human output names the status and cause.
func TestMigrateWorkspaces_RefusalFailsTheRun(t *testing.T) {
	root := migrateWorkspacesTestProject(t)
	for _, status := range []agent.WorkspaceMigrationStatus{agent.MigrationRefusedRunning, agent.MigrationFailedVerify, agent.MigrationMigratedSourceKept} {
		f := &migrateWorkspacesFake{results: []agent.WorkspaceMigrationResult{{Agent: "a1", Status: status, Cause: "fixed cause"}}}
		var out bytes.Buffer
		err := migrateWorkspaces(context.Background(), &out, f.deps(), filepath.Join(root, config.DotScion), nil, false, false)
		require.Error(t, err, status)
		assert.Contains(t, out.String(), string(status)+": fixed cause")
	}
	for _, status := range []agent.WorkspaceMigrationStatus{agent.MigrationMigrated, agent.MigrationSkipped, agent.MigrationAlreadyMigrated} {
		f := &migrateWorkspacesFake{results: []agent.WorkspaceMigrationResult{{Agent: "a1", Status: status}}}
		require.NoError(t, migrateWorkspaces(context.Background(), &bytes.Buffer{}, f.deps(), root, nil, false, false), status)
	}
}

// Without --project every project on the shared layout is processed;
// --agent needs --project.
func TestMigrateWorkspaces_AllProjects(t *testing.T) {
	root := migrateWorkspacesTestProject(t)
	f := &migrateWorkspacesFake{}
	require.NoError(t, migrateWorkspaces(context.Background(), &bytes.Buffer{}, f.deps(filepath.Join(root, config.DotScion)), "", nil, true, false))
	require.Len(t, f.got, 1)
	assert.Equal(t, filepath.Join(root, config.DotScion), f.got[0].ProjectDir)

	err := migrateWorkspaces(context.Background(), &bytes.Buffer{}, f.deps(), "", []string{"a1"}, true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--agent requires --project")
}

// A directory that is not a shared project configuration is refused
// before anything runs.
func TestMigrateWorkspaces_NotSharedProjectRefused(t *testing.T) {
	f := &migrateWorkspacesFake{}
	err := migrateWorkspaces(context.Background(), &bytes.Buffer{}, f.deps(), t.TempDir(), nil, true, false)
	require.Error(t, err)
	assert.Empty(t, f.got)
}
