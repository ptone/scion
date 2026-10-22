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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// Sources of a repository root record (provisionedWorktreeStateFile). A
// record is written only when agentDir is outside every mount of the agent
// (agentDirOutsideAgentMounts), so its source also records that it was
// written under that condition. A record without one of these sources
// (written by an older version) is not used.
const (
	// repoRootSourceBroker: the broker provisioned the agent's worktree
	// and passed its base on ctx (api.ContextWithProvisionedWorktreeRepoRoot).
	repoRootSourceBroker = "broker"
	// repoRootSourceProvision: ProvisionAgent recorded the project's
	// repository root (trustedProjectRepoRoot) for a git project.
	repoRootSourceProvision = "provision"
	// repoRootSourceBackfill: Start recorded trustedProjectRepoRoot for an
	// agent provisioned before records were written.
	repoRootSourceBackfill = "backfill"
)

// errAgentDirMounted is returned when a record is not written because
// agentDir is inside one of the agent's mounts.
var errAgentDirMounted = errors.New("agent directory is inside one of the agent's mounts")

// trustedProjectRepoRoot returns the main repository that holds projectDir,
// found without running git: the nearest ancestor of projectDir (projectDir
// included, symlinks resolved) that has a .git directory, not a .git file,
// with no commondir file in it. It reads no repository configuration and no
// environment, so neither core.worktree, GIT_DIR nor anything inside an
// agent's workspace (which sits below projectDir) can change the answer. It
// returns "" when projectDir cannot be resolved or no such ancestor exists,
// and stops (returning "") at the first .git that is not a directory.
func trustedProjectRepoRoot(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	dir, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return ""
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		if fi, err := os.Lstat(gitPath); err == nil {
			if !fi.IsDir() {
				return ""
			}
			if _, err := os.Lstat(filepath.Join(gitPath, "commondir")); err == nil {
				return ""
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// agentMountSources returns the host paths the agent's container mounts
// from: the workspace, the repository root mount (repoRoot/.git when the
// workspace is below repoRoot, otherwise the whole repoRoot, as
// pkg/runtime/common.go does), and configured local volumes.
func agentMountSources(workspace, repoRoot string, volumes []api.VolumeMount) []string {
	var sources []string
	if workspace != "" {
		sources = append(sources, workspace)
	}
	if repoRoot != "" {
		if workspace != "" && workspace != repoRoot && pathUnder(workspace, repoRoot) {
			sources = append(sources, filepath.Join(repoRoot, ".git"))
		} else {
			sources = append(sources, repoRoot)
		}
	}
	for _, v := range volumes {
		if v.Type == "gcs" || v.Source == "" {
			continue
		}
		src := v.Source
		if home, err := os.UserHomeDir(); err == nil {
			if src == "~" {
				src = home
			} else if strings.HasPrefix(src, "~/") {
				src = filepath.Join(home, src[2:])
			}
		}
		sources = append(sources, src)
	}
	return sources
}

// agentDirOutsideAgentMounts reports whether agentDir lies outside every
// path in sources: it is neither one of them nor below one. Paths are
// compared with symlinks resolved where they exist. An agentDir that cannot
// be resolved is reported as not outside.
//
// Only the agent's own mounts are known here; see the item notes for what
// this does not cover.
func agentDirOutsideAgentMounts(agentDir string, sources []string) bool {
	dir, err := filepath.EvalSymlinks(agentDir)
	if err != nil {
		return false
	}
	for _, src := range sources {
		if src == "" {
			continue
		}
		s := src
		if resolved, err := filepath.EvalSymlinks(src); err == nil {
			s = resolved
		} else if !filepath.IsAbs(s) {
			return false
		}
		if pathUnder(dir, filepath.Clean(s)) {
			return false
		}
	}
	return true
}

// samePath reports whether a and b name the same path, compared cleaned
// and, when both resolve, with symlinks resolved.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// pathUnder reports whether p is base or below it (both clean and absolute).
func pathUnder(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// readRepoRootRecord reads agentDir's repository root record. It reports
// false when there is no record, it does not parse, or its source is not
// one this version writes.
func readRepoRootRecord(agentDir string) (provisionedWorktreeState, bool) {
	data, err := os.ReadFile(filepath.Join(agentDir, provisionedWorktreeStateFile))
	if err != nil {
		return provisionedWorktreeState{}, false
	}
	var state provisionedWorktreeState
	if err := json.Unmarshal(data, &state); err != nil || state.RepoRoot == "" {
		return provisionedWorktreeState{}, false
	}
	switch state.Source {
	case repoRootSourceBroker, repoRootSourceProvision, repoRootSourceBackfill:
		return state, true
	default:
		return provisionedWorktreeState{}, false
	}
}

// writeRepoRootRecord writes root with source to agentDir's record, but only
// when agentDir is outside every path in mountSources; otherwise it writes
// nothing and returns errAgentDirMounted.
func writeRepoRootRecord(agentDir, root, source string, mountSources []string) error {
	if !agentDirOutsideAgentMounts(agentDir, mountSources) {
		return errAgentDirMounted
	}
	if cur, ok := readRepoRootRecord(agentDir); ok && cur.RepoRoot == root && cur.Source == source {
		return nil
	}
	data, err := json.Marshal(provisionedWorktreeState{RepoRoot: root, Source: source})
	if err != nil {
		return err
	}
	return writeAgentInfoFile(filepath.Join(agentDir, provisionedWorktreeStateFile), data, 0o644)
}

// checkProvisionRepoRoot returns trustedProjectRepoRoot(projectDir) after
// checking it against the repository root git reports for projectDir
// (util.RepoRootDir, with symlinks resolved). A difference, or no trusted
// root, is an error: provisioning does not proceed on a repository it cannot
// identify without git.
func checkProvisionRepoRoot(projectDir string) (string, error) {
	trusted := trustedProjectRepoRoot(projectDir)
	gitRoot, err := util.RepoRootDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolve the project's repository root: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(gitRoot); err == nil {
		gitRoot = resolved
	}
	if trusted == "" || trusted != gitRoot {
		return "", fmt.Errorf("the project's repository root could not be confirmed without reading container-writable git metadata, so agents cannot be provisioned for it in this interim release (ptone/scion#4611): git reports %q but the directory walk from %s finds %q (the project's top-level .git must be a directory without a commondir file; a linked worktree or a submodule working directory is not supported here yet). Provision from the repository's main checkout instead. A project that merely contains submodules is unaffected", gitRoot, projectDir, trusted)
	}
	return trusted, nil
}

// startRepoRootInput is what resolveStartRepoRoot needs from Start.
type startRepoRootInput struct {
	agentName   string
	agentDir    string
	projectDir  string
	workspace   string // effectiveWorkspace, as Start has it
	ctxRepoRoot string // the broker's signal for this dispatch, if any
	explicit    bool   // an explicit-workspace start
	volumes     []api.VolumeMount
}

// resolveStartRepoRoot returns the repository root Start passes to the
// runtime, and the broker value (if any) the workspace-backend step must
// re-check. Nothing is derived from the workspace's own git metadata:
//  1. A broker signal on ctx is used once validatedWorktreeRepoRoot
//     confirms it against the workspace.
//  2. Otherwise agentDir's record is read (readRepoRootRecord). It is used
//     only while agentDir is outside every mount of the agent now
//     (agentDirOutsideAgentMounts), computed for the root it names:
//     - a broker record once validatedWorktreeRepoRoot confirms it;
//     - a provision or backfill record only when its root still equals
//     trustedProjectRepoRoot(projectDir), and not on an explicit-workspace
//     start.
//  3. With no usable record, an explicit-workspace start, or a project
//     that is not a git project, gets "".
//  4. Otherwise (a git project provisioned before records were written),
//     when the workspace is the per-agent directory <agentDir>/workspace,
//     trustedProjectRepoRoot is recorded as a backfill record, when
//     agentDir is outside every mount of the agent, and used. If the walk
//     finds no root, or the record cannot be written, the result is "".
//
// The explicit flag can only lower the result to "".
func resolveStartRepoRoot(in startRepoRootInput) (repoRoot, brokerCandidate string) {
	if in.ctxRepoRoot != "" {
		return validatedWorktreeRepoRoot(in.ctxRepoRoot, in.workspace), in.ctxRepoRoot
	}
	rec, ok := readRepoRootRecord(in.agentDir)
	if ok {
		if !agentDirOutsideAgentMounts(in.agentDir, agentMountSources(in.workspace, rec.RepoRoot, in.volumes)) {
			slog.Warn("start: the agent directory is inside one of the agent's mounts; its repository root record is not used",
				"agent", in.agentName)
			return "", ""
		}
		if rec.Source == repoRootSourceBroker {
			return validatedWorktreeRepoRoot(rec.RepoRoot, in.workspace), rec.RepoRoot
		}
		if in.explicit {
			return "", ""
		}
		// A provision or backfill record is honoured only when its root
		// still equals the trusted walk from the project directory. The
		// provenance field is not a proof — anything that can write the
		// record can write the source too — so the root is checked against
		// the project here, not trusted on the source alone. The walk runs
		// no git and reads no config or environment.
		if !samePath(rec.RepoRoot, trustedProjectRepoRoot(in.projectDir)) {
			slog.Warn("start: the recorded repository root does not match the project; it is not used",
				"agent", in.agentName)
			return "", ""
		}
		return rec.RepoRoot, ""
	}
	if in.explicit || !isGitWorkspaceProject(in.projectDir) {
		return "", ""
	}
	// Backfill only the per-agent workspace directory, the one place
	// ProvisionAgent creates a git project's worktree. An agent attached to
	// a worktree elsewhere, or with an explicit workspace whose persisted
	// flag was lost, gets no repository root.
	if !samePath(in.workspace, filepath.Join(in.agentDir, "workspace")) {
		return "", ""
	}
	root := trustedProjectRepoRoot(in.projectDir)
	if root == "" {
		return "", ""
	}
	if err := writeRepoRootRecord(in.agentDir, root, repoRootSourceBackfill, agentMountSources(in.workspace, root, in.volumes)); err != nil {
		slog.Warn("start: repository root not recorded; the agent starts without a repository root mount",
			"agent", in.agentName, "reason", err)
		return "", ""
	}
	slog.Info("start: recorded the project's repository root for an agent provisioned without one",
		"agent", in.agentName, "repo_root", root)
	return root, ""
}

// refuseWorkspaceInAgentsDir rejects an explicit workspace that resolves at or
// below the project's own .scion/agents directory (which holds agents' own
// directories, never a workspace). Such a workspace would mount the agents
// tree read-write and is not covered by the in-project agents-dir mask, since
// the agents directory would not sit at <workspace>/.scion/agents. Other
// locations under .scion (for example the hub-native base's
// .scion/workspace/worktrees/<agent>) are allowed. projectDir is the resolved
// .scion directory (or its parent); both spellings are handled.
func refuseWorkspaceInAgentsDir(projectDir, workspace string) error {
	scionDir := projectDir
	if filepath.Base(projectDir) != config.DotScion {
		scionDir = filepath.Join(projectDir, config.DotScion)
	}
	agentsDir := filepath.Join(scionDir, "agents")
	ra, err := filepath.EvalSymlinks(agentsDir)
	if err != nil {
		return nil // no agents directory to be inside
	}
	rw, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		// Fail closed: the caller passes an existing workspace, so a
		// resolution failure here is unexpected.
		return fmt.Errorf("resolve workspace %q: %w", workspace, err)
	}
	if pathUnder(rw, ra) {
		return fmt.Errorf("workspace %q is inside the project's agents directory (%s), which holds agent state, not a workspace; choose a workspace outside it", workspace, agentsDir)
	}
	return nil
}
