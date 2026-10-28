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

package runtimebroker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/projectfs"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// sharedWorkspaceKnownNotGit reports whether the broker positively knows
// that this start's workspace is the project's shared workspace on the
// broker's shared Layout and that it is not a git repository, so
// SCION_WORKSPACE_GIT=false may be sent (the Kubernetes runtime then allows
// the shared workspace; see k8sSharedRepoShapeRefusal in pkg/runtime).
//
// Every input is a broker or hub fact a project member cannot set:
//   - the broker's Layout is shared and the start has a project ID;
//   - the hub dispatched the shared-plain sharing mode (its workspaceMode
//     field, where empty means shared-plain), never a clone, worktree or
//     empty-per-agent mode;
//   - the hub sent no git source for the workspace: no per-agent GitClone,
//     no shared-workspace clone settings and no SharedWorkspace flag. The
//     hub sets the flag and the clone settings only for a project with a git
//     remote (store.Project.IsSharedWorkspace, sharedWorkspaceCloneConfig)
//     and re-sends them on every start;
//   - no worktree was provisioned and no linked checkout is used;
//   - at decision time, the project's workspace directory on the share is a
//     visible directory (not a symlink) with no .git entry, and git does not
//     see it as inside a work tree (util.CheckGitRepoDir, which reports an
//     ownership refusal as an error rather than as "not git").
//
// Anything else, including a workspace directory that does not exist yet,
// returns false: the variable stays absent and the Kubernetes guard refuses
// the start (fail closed). Labels, project env, request env and template
// values are never read.
func (s *Server) sharedWorkspaceKnownNotGit(ctx context.Context, in startContextInputs, opts *api.StartOptions, worktreeProvisioned, linkedCheckout bool) bool {
	layout := s.config.StorageLayout
	if !layout.Backend.Shared() || in.ProjectID == "" {
		return false
	}
	if store.ResolveWorkspaceSharingMode(in.WorkspaceMode) != store.SharingModeSharedPlain {
		return false
	}
	if c := in.Config; c != nil && (c.SharedWorkspace || c.GitClone != nil || c.SharedWorkspaceClone != nil) {
		return false
	}
	if worktreeProvisioned || linkedCheckout || opts.GitClone != nil || opts.SharedWorkspaceClone != nil {
		return false
	}
	leaf, err := layout.Resolve(projectfs.Project{ID: in.ProjectID}, "", projectfs.WorkspaceMount(projectfs.ModeSharedPlain, ""))
	if err != nil {
		return false
	}
	return workspaceDirKnownNotGit(ctx, leaf.HostPath)
}

// workspaceDirKnownNotGit reports whether dir is, right now, a visible
// directory (not a symlink) that holds no .git entry and that git does not
// see as inside a work tree. Any error or doubt returns false.
func workspaceDirKnownNotGit(ctx context.Context, dir string) bool {
	if dir == "" || projectfs.ShareDirVisible(ctx, dir) != nil {
		return false
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !errors.Is(err, fs.ErrNotExist) {
		return false // a .git entry, or it could not be checked
	}
	isGit, err := util.CheckGitRepoDir(dir)
	return err == nil && !isGit
}
