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

package runtime

import (
	"os"
	"path"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// sharedWorkspaceAgentsDir is the in-project agents root, relative to the
// workspace. Agent state for shared-workspace projects lives in the
// broker-side agent dir, so the runtimes keep this path out of the
// container's view of a shared workspace, to keep per-agent state dirs out
// of the shared workspace view on all runtimes (ptone/scion#5513).
const sharedWorkspaceAgentsDir = ".scion/agents"

// sharedWorkspaceAgentsMaskTarget returns the container path of the
// in-project agents root of a shared workspace mount, and whether it is to
// be kept out of the container's view. Callers invoke it only where they
// mount cfg.Workspace as the project's shared workspace at
// containerWorkspace.
//
// The condition: the workspace is the repo root itself (compared after
// resolving symlinks on both paths), and <workspace>/.scion is a directory
// on the broker. With requireAgentsDir, <workspace>/.scion/agents must
// already be a directory as well: a runtime whose mask would otherwise make
// the container runtime create that mount point in the shared tree passes
// it, so the mask never creates anything there. When the directory is
// missing there is nothing to hide.
//
// If either path cannot be resolved, the result is false and the reason is
// logged.
func sharedWorkspaceAgentsMaskTarget(cfg RunConfig, containerWorkspace string, requireAgentsDir bool) (string, bool) {
	if cfg.RepoRoot == "" || cfg.Workspace == "" || containerWorkspace == "" {
		return "", false
	}
	repoRoot, err := filepath.EvalSymlinks(cfg.RepoRoot)
	if err != nil {
		runtimeLog().Warn("Not masking the in-project agents dir: repo root does not resolve",
			"agent", cfg.Name, "repo_root", cfg.RepoRoot, "error", err)
		return "", false
	}
	workspace, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		runtimeLog().Warn("Not masking the in-project agents dir: workspace does not resolve",
			"agent", cfg.Name, "workspace", cfg.Workspace, "error", err)
		return "", false
	}
	if repoRoot != workspace {
		return "", false
	}
	if !isDir(filepath.Join(workspace, ".scion")) {
		return "", false
	}
	if requireAgentsDir && !isDir(filepath.Join(workspace, filepath.FromSlash(sharedWorkspaceAgentsDir))) {
		runtimeLog().Debug("Not masking the in-project agents dir: it does not exist",
			"agent", cfg.Name, "workspace", workspace)
		return "", false
	}
	return path.Join(containerWorkspace, sharedWorkspaceAgentsDir), true
}

// plainWorkspaceAgentsMaskTarget returns the container path of
// <workspace>/.scion/agents for a plain workspace mount (no repository root
// layout, for example an explicit --workspace), and whether to shadow it.
// It applies when <workspace>/.scion is a directory (a project root),
// whether or not <workspace>/.scion/agents exists yet; the caller creates
// that directory on the host before mounting the tmpfs. The agents directory
// is required to pre-exist only where the runtime cannot create a tmpfs
// mount point itself (Apple). A workspace that is an ancestor of a project
// (the agents directory sits deeper than <workspace>/.scion) is not
// covered.
func plainWorkspaceAgentsMaskTarget(cfg RunConfig, containerWorkspace string, requireAgentsDir bool) (string, bool) {
	if cfg.Workspace == "" || containerWorkspace == "" {
		return "", false
	}
	workspace, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		runtimeLog().Warn("Not masking the in-project agents dir: workspace does not resolve",
			"agent", cfg.Name, "workspace", cfg.Workspace, "error", err)
		return "", false
	}
	// The workspace root is a project root when it holds a .scion directory.
	// The mask then shadows <workspace>/.scion/agents even when that
	// directory does not exist yet, so agent directories the project creates
	// after this container starts are not shown through the live mount. The
	// agents directory must already exist only where the runtime cannot
	// create a tmpfs mount point itself (Apple), the same rule
	// sharedWorkspaceAgentsMaskTarget uses.
	if !isDir(filepath.Join(workspace, config.DotScion)) {
		return "", false
	}
	if requireAgentsDir && !isDir(filepath.Join(workspace, filepath.FromSlash(sharedWorkspaceAgentsDir))) {
		runtimeLog().Debug("Not masking the in-project agents dir: it does not exist",
			"agent", cfg.Name, "workspace", workspace)
		return "", false
	}
	return path.Join(containerWorkspace, sharedWorkspaceAgentsDir), true
}

// isDir reports whether p is a directory, following symlinks.
func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
