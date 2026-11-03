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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/provision"
	"github.com/GoogleCloudPlatform/scion/pkg/shareddirs"
)

// WorkspaceMigrationStatus is the machine-readable outcome of migrating one
// agent's workspace (MigrateAgentWorkspaces). In every refused_* and
// failed_* outcome the agent's broker-local workspace is left in place.
type WorkspaceMigrationStatus string

const (
	// MigrationWouldMigrate: a dry run found a workspace it would migrate.
	MigrationWouldMigrate WorkspaceMigrationStatus = "would_migrate"
	// MigrationMigrated: the workspace was copied to the share, verified,
	// renamed into place, and the broker-local workspace removed.
	MigrationMigrated WorkspaceMigrationStatus = "migrated"
	// MigrationMigratedSourceKept: the workspace is in place on the share,
	// but the broker-local workspace was kept (the agent started before it
	// could be removed, or its removal failed). A later run removes it once
	// the agent is stopped and both are still equal.
	MigrationMigratedSourceKept WorkspaceMigrationStatus = "migrated_source_kept"
	// MigrationAlreadyMigrated: the workspace is on the share and no
	// differing broker-local workspace remains.
	MigrationAlreadyMigrated WorkspaceMigrationStatus = "already_migrated"
	// MigrationSkipped: nothing to migrate for this agent.
	MigrationSkipped WorkspaceMigrationStatus = "skipped"
	// MigrationRefusedRunning: the agent's container is running, or its
	// state could not be determined.
	MigrationRefusedRunning WorkspaceMigrationStatus = "refused_running"
	// MigrationRefusedConflict: the share already holds something for this
	// agent that is not an equal copy, or another migration holds the
	// agent's lock. Nothing was changed.
	MigrationRefusedConflict WorkspaceMigrationStatus = "refused_conflict"
	// MigrationRefusedUnsupported: the workspace or its target cannot be
	// migrated safely (an entry type or owner that is not copied, an unsafe
	// target directory, insufficient space, an unsupported runtime).
	MigrationRefusedUnsupported WorkspaceMigrationStatus = "refused_unsupported"
	// MigrationFailedCopy: copying to the share failed; the partial copy
	// was discarded or is discarded by the next run.
	MigrationFailedCopy WorkspaceMigrationStatus = "failed_copy"
	// MigrationFailedVerify: the copy on the share does not match the
	// broker-local workspace; it was discarded.
	MigrationFailedVerify WorkspaceMigrationStatus = "failed_verify"
)

// Fixed causes. A result's Cause is one of these (with a count where the
// text says so), never text taken from inside a workspace.
const (
	causeNoLocalWorkspace     = "no broker-local workspace"
	causeLocalWorkspaceEmpty  = "broker-local workspace is empty"
	causeLegacyAgentDir       = "legacy in-project agent directory"
	causeNotSharedLayout      = "project storage layout is not shared"
	causeLayoutUnresolved     = "project storage layout cannot be resolved for this agent"
	causeRunning              = "agent container is running"
	causeRunningUnknown       = "agent running state unknown"
	causeLocked               = "another migration holds the agent lock"
	causeLeafDiffers          = "share workspace exists and differs from the broker-local workspace"
	causeTargetNotDir         = "share entry for the agent is not a directory"
	causeTargetParentUnsafe   = "share agents directory is not owned by the migrator or is group/other writable"
	causeSourceNotDir         = "broker-local workspace is not a directory"
	causeLinkedWorktree       = "broker-local workspace is a linked git worktree"
	causeSpecialFile          = "workspace holds special files (fifo, socket or device)"
	causeUnreadableForeign    = "workspace holds entries owned by another user that the agent cannot read"
	causeTooDeep              = "workspace tree is too deep"
	causeInsufficientSpace    = "insufficient space"
	causeCopyFailed           = "copy to the share failed"
	causeVerifyMismatch       = "copy on the share does not match"
	causeStagingRaced         = "staging directory changed during the copy"
	causeRenameConflict       = "share workspace appeared during the migration"
	causeStartedBeforeRemoval = "agent started before the broker-local workspace could be removed"
	causeRemovalFailed        = "broker-local workspace removal failed"
)

// WorkspaceMigrationResult is one agent's outcome. Source and Target are
// the broker-local workspace and its directory on the share, both derived
// from configuration; no name from inside a workspace is ever reported.
type WorkspaceMigrationResult struct {
	Agent  string                   `json:"agent"`
	Status WorkspaceMigrationStatus `json:"status"`
	Cause  string                   `json:"cause,omitempty"`
	Source string                   `json:"source,omitempty"`
	Target string                   `json:"target,omitempty"`
	// SourceBytes and SourceEntries size the broker-local workspace
	// (regular file bytes; files, directories and links, .git, untracked
	// and ignored files included), when it was measured.
	SourceBytes   int64 `json:"source_bytes,omitempty"`
	SourceEntries int   `json:"source_entries,omitempty"`
}

// AgentRunningFunc reports whether an agent's container is running (or may
// be). An error refuses the agent.
type AgentRunningFunc func(ctx context.Context, agentName string) (bool, error)

// WorkspaceMigrationOptions selects what MigrateAgentWorkspaces migrates.
type WorkspaceMigrationOptions struct {
	// ProjectDir is the project's shared configuration directory
	// (<project root>/.scion, config.IsSharedProjectConfigDir).
	ProjectDir string
	// Settings builds the project storage Layout; nil loads it for
	// ProjectDir.
	Settings *config.VersionedSettings
	// Agents names the agents to migrate; empty selects every agent
	// directory in the project's broker-local agents root.
	Agents []string
	// DryRun reports what would happen and changes nothing.
	DryRun bool
	// Running is required: it is asked before the copy, immediately before
	// the rename into place, and before the broker-local workspace is
	// removed (AgentManager.AgentContainerLive).
	Running AgentRunningFunc
}

// errWorkspaceMigrationUnsupportedRuntime refuses a running check on a
// runtime whose agents do not use a broker-local workspace.
var errWorkspaceMigrationUnsupportedRuntime = errors.New("workspace migration supports local container runtimes only")

// MigrateAgentWorkspaces moves the broker-local workspaces of agent-scoped
// agents (clone-per-agent, empty-per-agent) of a project on a shared
// Layout, provisioned before the project's storage moved there, to the
// agent's directory on the share (<AgentRoot>/workspace, the directory
// sharedLayoutAgentWorkspace places them in). It is an explicit operator
// step: Start never moves a workspace, and refuses such an agent until it
// is migrated (checkLocalRuntimeAgentWorkspace).
//
// Per agent it copies into a staging directory beside the target, verifies
// the copy against the source byte for byte (every entry's path, type,
// size, mode, mapped owner, link text and content hash, including .git,
// untracked and ignored files; no program is run on either tree), renames
// the staging directory into place, and only then removes the broker-local
// workspace. Any failure keeps the source. It is idempotent: a migrated
// agent is reported already_migrated, and a leftover staging directory from
// an interrupted run is discarded and redone. See migrateAgentWorkspace for
// the per-agent steps.
//
// The returned error is for the run as a whole (an unusable project or
// options); per-agent outcomes are in the results, in agent name order.
func MigrateAgentWorkspaces(ctx context.Context, opts WorkspaceMigrationOptions) ([]WorkspaceMigrationResult, error) {
	if err := workspaceMigrationSupported(); err != nil {
		return nil, err
	}
	if opts.Running == nil {
		return nil, errors.New("workspace migration: a running check is required")
	}
	if !config.IsSharedProjectConfigDir(opts.ProjectDir) {
		return nil, fmt.Errorf("workspace migration: %s is not a shared project configuration directory", opts.ProjectDir)
	}
	abs, err := filepath.Abs(opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("workspace migration: %w", err)
	}
	// IsSharedProjectConfigDir checked that the parent's name is the
	// project ID recorded in the configuration.
	projectID := filepath.Base(filepath.Dir(abs))
	agentsRoot, err := config.AgentsRootForProject(opts.ProjectDir, true, projectID)
	if err != nil {
		return nil, fmt.Errorf("workspace migration: %w", err)
	}
	names := opts.Agents
	if len(names) == 0 {
		if names, err = listMigrationAgents(agentsRoot); err != nil {
			return nil, fmt.Errorf("workspace migration: %w", err)
		}
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	results := make([]WorkspaceMigrationResult, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if !isNFSWorktreeName(name) {
			return results, fmt.Errorf("workspace migration: invalid agent name %q", name)
		}
		results = append(results, migrateOneAgentWorkspace(ctx, opts, projectID, agentsRoot, name))
	}
	return results, nil
}

// listMigrationAgents lists the agent directories in the broker-local
// agents root: directory entries (never followed links) whose name is an
// agent slug. A missing root has none.
func listMigrationAgents(agentsRoot string) ([]string, error) {
	entries, err := os.ReadDir(agentsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && isNFSWorktreeName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// workspaceMigrationPlan is one agent's source and target, derived from
// configuration only.
type workspaceMigrationPlan struct {
	agent string
	// agentsRoot is the broker-local agents root; the source is
	// <agentsRoot>/<agent>/workspace.
	agentsRoot string
	// hostBase and agentRootRel address <AgentRoot> on the share
	// (projectfs.Layout.AgentRoot).
	hostBase     string
	agentRootRel string
	// owner is the Layout's owner, which the workspace leaf and every
	// entry copied into it get when the migrator runs as root.
	owner shareddirs.Owner
}

func (p workspaceMigrationPlan) source() string {
	return filepath.Join(p.agentsRoot, p.agent, provision.AgentWorkspaceDir)
}

func (p workspaceMigrationPlan) target() string {
	return filepath.Join(p.hostBase, p.agentRootRel, provision.AgentWorkspaceDir)
}

// migrationStagingName is the staging directory's name under <AgentRoot>,
// beside the workspace leaf. Its presence is the only partial-state marker.
func migrationStagingName(agent string) string { return ".migrating-" + agent }

// migrateOneAgentWorkspace plans one agent and runs migrateAgentWorkspace.
func migrateOneAgentWorkspace(ctx context.Context, opts WorkspaceMigrationOptions, projectID, agentsRoot, name string) WorkspaceMigrationResult {
	res := WorkspaceMigrationResult{Agent: name}
	if legacy := config.LegacySharedProjectAgentDir(opts.ProjectDir, name); legacy != "" {
		// It keeps its in-project workspace and guard root
		// (legacyAgentWorkspaceRoot) until it is recreated.
		res.Status, res.Cause = MigrationSkipped, causeLegacyAgentDir
		return res
	}
	layout, agentRoot, ok, err := sharedLayoutAgentRoot(ctx, opts.Settings, opts.ProjectDir, name)
	if err != nil {
		res.Status, res.Cause = MigrationRefusedUnsupported, causeLayoutUnresolved
		return res
	}
	if !ok {
		res.Status, res.Cause = MigrationSkipped, causeNotSharedLayout
		return res
	}
	p := workspaceMigrationPlan{
		agent:        name,
		agentsRoot:   agentsRoot,
		hostBase:     agentRoot.HostBase,
		agentRootRel: agentRoot.RelPath,
		owner:        shareddirs.Owner{UID: layout.UID, GID: layout.GID},
	}
	res.Source, res.Target = p.source(), p.target()
	return migrateAgentWorkspace(ctx, p, opts.DryRun, opts.Running, res)
}

// AgentContainerLive reports whether agentName (in projectID, when set) has
// a container on the manager's runtime that is not stopped, errored or
// halted, through the same scoped runtime lookup as message delivery
// (liveAgentEntry), so every local container runtime is covered. Unlike
// delivery it fails closed: a lookup error is returned, and a runtime other
// than a local container runtime is refused, since only those agents have
// a broker-local workspace to migrate.
func (m *AgentManager) AgentContainerLive(ctx context.Context, agentName, projectID string) (bool, error) {
	if m == nil || m.Runtime == nil || !isLocalContainerRuntime(m.Runtime.Name()) {
		return false, errWorkspaceMigrationUnsupportedRuntime
	}
	filter := map[string]string{"scion.name": api.Slugify(agentName)}
	if projectID != "" {
		filter["scion.project_id"] = projectID
	}
	agents, err := m.listRuntime(ctx, filter)
	if err != nil {
		return false, err
	}
	return liveAgentEntry(agents, agentName, projectID), nil
}
