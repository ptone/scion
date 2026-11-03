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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

var (
	brokerMigrateWorkspacesProject string
	brokerMigrateWorkspacesAgents  []string
	brokerMigrateWorkspacesDryRun  bool
	brokerMigrateWorkspacesJSON    bool
)

var brokerMigrateWorkspacesCmd = &cobra.Command{
	Use:   "migrate-workspaces",
	Short: "Move pre-switch agent workspaces onto the shared project storage layout",
	Long: `Move the workspaces of agents created before their project moved to shared
project storage from this Runtime Broker onto the share.

Run it on the Runtime Broker host, with the agents stopped, after the switch to
shared project storage. Agents whose workspace is still on this broker are
refused at start until they are migrated.

It handles clone-per-agent and empty-per-agent agents of projects on a shared
layout. For each agent it copies the broker-local workspace into a staging
directory beside the agent's workspace on the share, verifies the copy byte for
byte (including .git, untracked and ignored files), renames it into place, and
only then removes the broker-local copy. Any failure keeps the broker-local
workspace. Running agents are refused. Running it again is safe: migrated
agents are reported as already migrated.

Without --project, every project on this host's shared layout is processed.
Without --agent, every agent of each project is processed.

Examples:
  # See what would be migrated
  scion runtime-broker migrate-workspaces --dry-run

  # Migrate one project's agents, with machine-readable results
  scion runtime-broker migrate-workspaces --project /mnt/share/projects/<id> --json`,
	Args: cobra.NoArgs,
	RunE: runBrokerMigrateWorkspaces,
}

func init() {
	brokerCmd.AddCommand(brokerMigrateWorkspacesCmd)
	f := brokerMigrateWorkspacesCmd.Flags()
	f.StringVar(&brokerMigrateWorkspacesProject, "project", "", "Project root on the share, or its .scion directory (default: every project on the shared layout)")
	f.StringSliceVar(&brokerMigrateWorkspacesAgents, "agent", nil, "Agent to migrate (repeatable; default: every agent of the project)")
	f.BoolVar(&brokerMigrateWorkspacesDryRun, "dry-run", false, "List what would be done without changing anything")
	f.BoolVar(&brokerMigrateWorkspacesJSON, "json", false, "Print the results as JSON")
}

// migrateWorkspacesProjectResult is one project's results in the JSON output.
type migrateWorkspacesProjectResult struct {
	ProjectDir string                           `json:"project_dir"`
	Results    []agent.WorkspaceMigrationResult `json:"results"`
}

// migrateWorkspacesDeps are the parts of the command tests replace.
type migrateWorkspacesDeps struct {
	projectDirs func(ctx context.Context) ([]string, error)
	migrate     func(ctx context.Context, opts agent.WorkspaceMigrationOptions) ([]agent.WorkspaceMigrationResult, error)
	running     func(projectID string) agent.AgentRunningFunc
}

func runBrokerMigrateWorkspaces(cmd *cobra.Command, _ []string) error {
	mgr := agent.NewManager(runtime.GetRuntime("", profile))
	deps := migrateWorkspacesDeps{
		projectDirs: agent.SharedLayoutProjectConfigDirs,
		migrate:     agent.MigrateAgentWorkspaces,
		running: func(projectID string) agent.AgentRunningFunc {
			return func(ctx context.Context, name string) (bool, error) {
				return mgr.AgentContainerLive(ctx, name, projectID)
			}
		},
	}
	return migrateWorkspaces(cmd.Context(), cmd.OutOrStdout(), deps, brokerMigrateWorkspacesProject,
		brokerMigrateWorkspacesAgents, brokerMigrateWorkspacesDryRun, brokerMigrateWorkspacesJSON)
}

// migrateWorkspaces runs the migration for the selected projects and prints
// the results. It returns an error when any agent was refused or failed, so
// the exit status says whether the switch can proceed.
func migrateWorkspaces(ctx context.Context, out io.Writer, deps migrateWorkspacesDeps, project string, agents []string, dryRun, asJSON bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var dirs []string
	if project != "" {
		dir := filepath.Clean(project)
		if filepath.Base(dir) != config.DotScion {
			dir = filepath.Join(dir, config.DotScion)
		}
		dirs = []string{dir}
	} else {
		if len(agents) > 0 {
			return fmt.Errorf("--agent requires --project")
		}
		var err error
		if dirs, err = deps.projectDirs(ctx); err != nil {
			return err
		}
	}
	all := make([]migrateWorkspacesProjectResult, 0, len(dirs))
	notDone := 0
	for _, dir := range dirs {
		projectID, err := agent.WorkspaceMigrationProjectID(dir)
		if err != nil {
			return err
		}
		res, err := deps.migrate(ctx, agent.WorkspaceMigrationOptions{
			ProjectDir: dir,
			Agents:     agents,
			DryRun:     dryRun,
			Running:    deps.running(projectID),
		})
		if err != nil {
			return err
		}
		for _, r := range res {
			if migrationNotDone(r.Status) {
				notDone++
			}
		}
		all = append(all, migrateWorkspacesProjectResult{ProjectDir: dir, Results: res})
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			DryRun   bool                             `json:"dry_run"`
			Projects []migrateWorkspacesProjectResult `json:"projects"`
		}{dryRun, all}); err != nil {
			return err
		}
	} else {
		printMigrateWorkspaces(out, all)
	}
	if notDone > 0 {
		return fmt.Errorf("%d agent(s) were refused or failed; their broker-local workspaces are unchanged or kept", notDone)
	}
	return nil
}

// migrationNotDone reports whether an agent's outcome needs the operator.
func migrationNotDone(s agent.WorkspaceMigrationStatus) bool {
	return strings.HasPrefix(string(s), "refused_") || strings.HasPrefix(string(s), "failed_") ||
		s == agent.MigrationMigratedSourceKept
}

func printMigrateWorkspaces(out io.Writer, all []migrateWorkspacesProjectResult) {
	if len(all) == 0 {
		_, _ = fmt.Fprintln(out, "No projects on the shared layout.")
		return
	}
	for _, p := range all {
		_, _ = fmt.Fprintf(out, "Project %s\n", p.ProjectDir)
		if len(p.Results) == 0 {
			_, _ = fmt.Fprintln(out, "  no agents")
		}
		for _, r := range p.Results {
			line := fmt.Sprintf("  %-24s %s", r.Agent, r.Status)
			if r.Cause != "" {
				line += ": " + r.Cause
			}
			_, _ = fmt.Fprintln(out, line)
			if r.Source != "" && (r.Status == agent.MigrationWouldMigrate || r.Status == agent.MigrationMigrated || r.Status == agent.MigrationMigratedSourceKept) {
				_, _ = fmt.Fprintf(out, "      %s -> %s (%d bytes, %d entries)\n", r.Source, r.Target, r.SourceBytes, r.SourceEntries)
			}
		}
	}
}
