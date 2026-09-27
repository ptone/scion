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

// Package provision implements Tier-1 universal workspace provisioning.
// It is a config-free leaf package that depends only on stdlib, pkg/api,
// and pkg/store — deliberately avoiding pkg/config so that lean binaries
// (e.g. sciontool) can invoke provisioning without pulling in
// filesystem-based project path resolution.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ProvisionSentinelFile is the name of the sentinel file written atomically
// after a successful workspace clone/setup. Its presence short-circuits
// subsequent ProvisionShared calls — the workspace is already ready.
const ProvisionSentinelFile = ".scion-provisioned"

// provisionLockRetries is the number of times to retry acquiring the
// per-project advisory lock before giving up. Each retry sleeps briefly
// (provisionLockRetryDelay) to allow the current holder to finish.
const provisionLockRetries = 30

// provisionLockRetryDelay is the sleep between advisory lock acquisition
// retries. Provisioning (git clone) is typically short (seconds), so a
// short retry cadence is appropriate.
const provisionLockRetryDelay = 1 * time.Second

// ResolvedWorkspace holds the deterministic path resolution result.
type ResolvedWorkspace struct {
	// HostPath is the absolute host-side path for the workspace.
	// For localBackend this is the existing project path (e.g.
	// ~/.scion.projects/<slug>/). For nfsBackend this is
	// <MountRoot>/<shareID>/<ServerRelativePath>.
	HostPath string

	// ServerRelativePath is the path relative to the NFS export root.
	// Empty for localBackend. For nfsBackend, e.g. "projects/<pid>/workspace".
	ServerRelativePath string

	// HostBase is the host mount prefix for NFS-backed workspaces
	// (<MountRoot>/<shareID>). Empty for localBackend.
	HostBase string

	// SharedDirs maps shared-dir name → resolved path info.
	SharedDirs map[string]ResolvedSharedDir

	// Backend identifies which backend produced this resolution: "local",
	// "nfs", "cloudrun-volume" or "gke-shared-volume".
	Backend string
}

// ResolvedSharedDir holds path resolution for a single shared directory.
type ResolvedSharedDir struct {
	// HostPath is the absolute host path for this shared dir.
	HostPath string

	// ServerRelativePath is the NFS export-relative path (empty for local).
	ServerRelativePath string
}

// ProvisionInput holds parameters for workspace provisioning.
type ProvisionInput struct {
	// Ctx is the context for cancellation and timeouts. Optional: when nil,
	// ProvisionShared falls back to context.Background(). Keeping it as a struct
	// field (rather than a ProvisionShared parameter) preserves the existing
	// function signature for callers.
	Ctx context.Context

	// Resolved is the output of a prior Resolve call.
	Resolved ResolvedWorkspace

	// ProjectID is the project's stable UUID.
	ProjectID string

	// AgentID is the agent's stable UUID.
	AgentID string

	// AgentName is a human-readable agent name (used for worktree branch names).
	AgentName string

	// Mode is the workspace sharing mode.
	Mode store.WorkspaceSharingMode

	// GitClone holds git-clone config when the project is git-backed; nil otherwise.
	GitClone *api.GitCloneConfig

	// Locker provides the per-project advisory lock for the NFS first-access
	// provisioning guard (design §7, risk RN1). On Postgres-backed deployments
	// this uses pg_try_advisory_lock(classid, objid) for cross-node mutual
	// exclusion; on SQLite it's a no-op (single-writer serializes already).
	//
	// May be nil — ProvisionShared degrades to sentinel-only guarding
	// (correct for single-node but NOT safe for multi-node).
	Locker store.AdvisoryLocker

	// NFSUID and NFSGID are the stable NFS ownership values (default 1000:1000).
	// Used for one-time chown of newly provisioned workspace directories.
	NFSUID int
	NFSGID int

	// SentinelDir overrides the directory where the provisioning sentinel file
	// (.scion-provisioned) is written and checked. When empty, defaults to
	// filepath.Dir(Resolved.HostPath) — the project root parent of the workspace
	// dir. This is needed for k8s init containers where only the workspace dir
	// itself is mounted (not its parent), so the sentinel must live inside the
	// workspace mount.
	SentinelDir string
}

// ProvisionShared is the universal, vendor-agnostic workspace provisioning
// function (Tier 1). It ensures the workspace directory exists and is ready
// for use. For git projects this includes cloning/worktree setup. Idempotent.
//
// The flow implements the first-access provisioning guard:
//
//  1. Acquire per-project advisory lock (try with retry — provisioning is short).
//  2. If sentinel <projectRoot>/.scion-provisioned exists → done (reuse).
//  3. Else: mkdir -p, git clone, chown 1000:1000, mode 0770, write sentinel.
//  4. Release lock.
//
// For WorktreePerAgent mode, the shared checkout is cloned once under the lock;
// each agent then adds its own git worktree (also under the lock, because
// worktree add/remove touches shared .git metadata).
//
// ClonePerAgent mode MUST NOT reach this path — it is node-local and handled
// by localBackend. An assert guards this.
//
// The flow is idempotent and race-safe: two agents for the same project
// starting on two different nodes contend on the advisory lock; exactly one
// clones, the second sees the sentinel and reuses the workspace.
func ProvisionShared(in ProvisionInput) error {
	// Guard: ClonePerAgent must never use the NFS path. SelectWorkspaceBackend
	// already routes it to localBackend, but assert here as defense in depth.
	if in.Mode == store.SharingModeClonePerAgent {
		return fmt.Errorf("ProvisionShared: ClonePerAgent mode must not use NFS backend " +
			"(should be routed to localBackend by SelectWorkspaceBackend)")
	}

	if in.Resolved.HostPath == "" {
		return fmt.Errorf("ProvisionShared: Resolved.HostPath is required")
	}
	if in.ProjectID == "" {
		return fmt.Errorf("ProvisionShared: ProjectID is required")
	}

	// Determine the sentinel directory: explicit override or default to parent.
	sentinelDir := in.SentinelDir
	if sentinelDir == "" {
		// The project root is the parent of the workspace dir:
		// <MountRoot>/<shareID>/<SubPathRoot>/<projectID>/ contains workspace/ + shared-dirs/.
		sentinelDir = filepath.Dir(in.Resolved.HostPath)
	}

	ctx := in.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// --- Step 1: Acquire per-project advisory lock ---
	release, err := acquireProvisionLock(ctx, in)
	if err != nil {
		return fmt.Errorf("ProvisionShared: failed to acquire lock for project %s: %w", in.ProjectID, err)
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			slog.Warn("ProvisionShared: failed to release advisory lock",
				"project_id", in.ProjectID, "error", releaseErr)
		}
	}()

	// --- Step 2: Check sentinel ---
	sentinelPath := filepath.Join(sentinelDir, ProvisionSentinelFile)
	if _, err := os.Stat(sentinelPath); err == nil {
		// Already provisioned — skip to worktree setup if needed.
		slog.Debug("ProvisionShared: workspace already provisioned (sentinel exists)",
			"project_id", in.ProjectID, "sentinel", sentinelPath)
		return ensureWorktree(ctx, in)
	}

	// --- Step 3: Provision (mkdir + clone + chown + sentinel) ---
	slog.Info("ProvisionShared: provisioning workspace",
		"project_id", in.ProjectID, "host_path", in.Resolved.HostPath)

	// Create workspace directory.
	if err := os.MkdirAll(in.Resolved.HostPath, 0770); err != nil {
		return fmt.Errorf("ProvisionShared: mkdir workspace %s: %w", in.Resolved.HostPath, err)
	}

	// Create shared-dir directories.
	for name, sd := range in.Resolved.SharedDirs {
		if err := os.MkdirAll(sd.HostPath, 0770); err != nil {
			return fmt.Errorf("ProvisionShared: mkdir shared-dir %q %s: %w", name, sd.HostPath, err)
		}
	}

	// Git clone if project is git-backed.
	if in.GitClone != nil && in.GitClone.URL != "" {
		if err := gitCloneWorkspace(ctx, in); err != nil {
			return fmt.Errorf("ProvisionShared: git clone: %w", err)
		}
	}

	// For worktree-per-agent: detach HEAD, disable gc, exclude worktrees/.
	if in.Mode == store.SharingModeWorktreePerAgent {
		if err := prepareBaseForWorktrees(ctx, in.Resolved.HostPath); err != nil {
			return fmt.Errorf("ProvisionShared: prepare base: %w", err)
		}
	}

	// Chown to stable NFS UID/GID (design §9.1). This is a ONE-TIME operation
	// under the advisory lock — per-start chown is skipped for NFS (see N1-5).
	//
	chownRoot := chownTarget(in.Resolved.HostPath)
	uid, gid := resolveUID(in), resolveGID(in)
	if err := chownProjectTree(ctx, chownRoot, uid, gid); err != nil {
		slog.Warn("ProvisionShared: chown failed (non-fatal, may lack privileges)",
			"project_id", in.ProjectID, "path", chownRoot, "uid", uid, "gid", gid, "error", err)
		// Non-fatal: operator may have pre-chowned. Continue to write sentinel.
	}

	// Write sentinel atomically.
	if err := writeSentinel(sentinelPath); err != nil {
		return fmt.Errorf("ProvisionShared: write sentinel: %w", err)
	}

	slog.Info("ProvisionShared: workspace provisioned successfully",
		"project_id", in.ProjectID, "host_path", in.Resolved.HostPath)

	// --- Step 4: Worktree setup (if WorktreePerAgent) ---
	return ensureWorktree(ctx, in)
}

// acquireProvisionLock acquires the per-project advisory lock, retrying briefly
// if another node currently holds it. Returns a release func.
//
// The retry loop respects context cancellation so that server shutdown is not
// blocked for up to provisionLockRetries × provisionLockRetryDelay.
func acquireProvisionLock(ctx context.Context, in ProvisionInput) (func() error, error) {
	if in.Locker == nil {
		// No locker available — degrade to unguarded (correct for single-node,
		// unsafe for multi-node). Log a warning.
		slog.Warn("ProvisionShared: no advisory locker available — provisioning is unguarded",
			"project_id", in.ProjectID)
		return func() error { return nil }, nil
	}

	objID := store.StableProjectHash(in.ProjectID)
	ticker := time.NewTicker(provisionLockRetryDelay)
	defer ticker.Stop()

	for attempt := 0; attempt < provisionLockRetries; attempt++ {
		acquired, release, err := in.Locker.TryAdvisoryLockObject(ctx, store.LockWorkspaceProvision, objID)
		if err != nil {
			return nil, fmt.Errorf("advisory lock attempt %d: %w", attempt, err)
		}
		if acquired {
			return release, nil
		}
		// Another node holds the lock — it's provisioning this project.
		// Wait briefly and retry, but honour context cancellation.
		slog.Debug("ProvisionShared: lock held by another node, retrying",
			"project_id", in.ProjectID, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while waiting for provisioning lock (project %s): %w",
				in.ProjectID, ctx.Err())
		case <-ticker.C:
		}
	}

	return nil, fmt.Errorf("failed to acquire provisioning lock after %d attempts (project %s)",
		provisionLockRetries, in.ProjectID)
}

// ErrCommondirPresent is returned by HardenedGitCommand when the target
// base's .git/commondir file exists. A hub-native shared base is always the
// main working copy of its own repository (never itself a linked worktree),
// so it never legitimately has one; its presence means git's common-
// directory resolution for this gitdir would be redirected somewhere other
// than the mounted, host-managed .git. Rather than operate against an
// unknown/unverified location, the broker refuses.
var ErrCommondirPresent = errors.New("refusing git operation: base .git/commondir is present")

// HardenedGitCommand builds an *exec.Cmd for a broker-side git invocation
// against a project's shared hub-native worktree-per-agent base repo, with
// the invocation-level hardening for the broker-git worktree containment
// change applied. It ensures the broker's own git operations honor only the
// host-managed config/hooks/refs for the base — never a redirected or
// otherwise unexpected location — and returns an error instead of running if
// it cannot establish that.
//
// Two mechanisms enforce this:
//   - GIT_COMMON_DIR is pinned to <dir>/.git on every invocation. Git
//     resolves config/hooks/refs through whatever "common directory" applies
//     to the gitdir it operates on; without pinning it, a top-level
//     <dir>/.git/commondir file (which git honors for the base's own gitdir,
//     not only for linked worktrees) can redirect that resolution elsewhere.
//     Pinning makes the common directory explicit and authoritative for
//     every call through this wrapper, independent of what any commondir
//     file says.
//   - As defense in depth (and detection), the wrapper first checks whether
//     <dir>/.git/commondir exists at all and refuses (ErrCommondirPresent)
//     if so, since a hub-native base never legitimately has one.
//
// It deliberately does NOT disable hooks (core.hooksPath), filters, aliases,
// or the global/system gitconfig — once the above holds, any hook/filter
// still configured for the base is host-managed and must keep running (most
// notably git-lfs: a post-checkout hook + filter.lfs.* smudge/clean driver).
// Specifically:
//   - GIT_CONFIG_NOSYSTEM=1 / GIT_CONFIG_GLOBAL=/dev/null were considered and
//     REJECTED: `git lfs install` writes filter.lfs.* to the global or system
//     gitconfig (not repo-local), so this would silently break LFS. It would
//     also strip broker-host config the broker legitimately relies on
//     (safe.directory, credential.helper, url.insteadOf, http.*,
//     core.sshCommand for its own auth) for no corresponding benefit.
//   - core.sshCommand is left alone for the same reason: this wrapper also
//     covers the base clone/pull paths, which may legitimately depend on the
//     broker's configured SSH transport.
//   - Alias neutralization is unnecessary: every caller here invokes an
//     explicit, hardcoded subcommand, never a user-suppliable one.
//
// What it does additionally clear:
//   - core.fsmonitor (both boolean and hook-path forms): not used by git-lfs
//     or any other broker operation, so clearing it costs nothing.
//   - core.pager=cat: purely cosmetic — a pager must never block
//     non-interactive broker output.
//
// dir is the working directory (cmd.Dir) and the base whose common directory
// is pinned; it must be non-empty. args are the git subcommand and its
// arguments, e.g. "worktree", "add", "--relative-paths", ... The returned
// cmd.Env starts as a copy of the broker process's environment plus
// GIT_COMMON_DIR; callers that need to layer additional env (e.g. the
// GIT_CONFIG_COUNT/KEY_0/VALUE_0 credential-helper technique) must append to
// cmd.Env rather than replace it, or the pin is lost.
func HardenedGitCommand(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	if dir == "" {
		return nil, fmt.Errorf("HardenedGitCommand: dir is required")
	}
	commondirFile := filepath.Join(dir, ".git", "commondir")
	if _, err := os.Stat(commondirFile); err == nil {
		slog.Error("HardenedGitCommand: refusing to operate, base .git/commondir is present",
			"dir", dir, "commondir_file", commondirFile)
		return nil, fmt.Errorf("%w: %s", ErrCommondirPresent, commondirFile)
	}

	hardened := []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.fsmonitor=",
		"-c", "core.pager=cat",
	}
	fullArgs := append(hardened, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMON_DIR="+filepath.Join(dir, ".git"))
	return cmd, nil
}

// gitCloneWorkspace performs the git clone into the workspace directory.
// It clones into the workspace path (in.Resolved.HostPath). The clone runs
// under ctx via exec.CommandContext so that a cancelled/timed-out context
// kills the git process instead of leaving it orphaned.
func gitCloneWorkspace(ctx context.Context, in ProvisionInput) error {
	gc := in.GitClone

	runClone := func() ([]byte, error) {
		args := []string{"clone"}

		// Set depth: nil/omitted = shallow depth 1, 0 = full clone (no --depth), >0 = that depth.
		depth := 1 // default: shallow
		if gc.Depth != nil {
			depth = *gc.Depth
		}
		if depth > 0 {
			args = append(args, "--depth", fmt.Sprintf("%d", depth))
		}
		// depth == 0 means full clone: no --depth flag

		// Set branch if specified.
		if gc.Branch != "" {
			args = append(args, "--branch", gc.Branch)
		}

		// Clone into the workspace directory.
		args = append(args, gc.URL, in.Resolved.HostPath)

		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(),
			// Disable interactive prompts during provisioning.
			"GIT_TERMINAL_PROMPT=0",
		)
		return cmd.CombinedOutput()
	}

	output, err := runClone()
	if err == nil {
		return nil
	}

	// If the workspace is not empty, the clone fails with "already exists and
	// is not an empty directory". This happens after a partially-failed prior
	// attempt (the sentinel was never written, else we'd have skipped cloning).
	if strings.Contains(string(output), "already exists and is not an empty directory") {
		// If .git is present a prior clone completed — reuse it as-is.
		if _, statErr := os.Stat(filepath.Join(in.Resolved.HostPath, ".git")); statErr == nil {
			slog.Warn("ProvisionShared: workspace not empty but .git present, reusing prior clone",
				"project_id", in.ProjectID, "path", in.Resolved.HostPath)
			return nil
		}

		// No .git — the prior attempt died mid-clone, leaving partial contents
		// behind. Clear the directory so provisioning self-heals on retry
		// without manual intervention, then clone once more.
		slog.Warn("ProvisionShared: workspace not empty and no .git (incomplete prior clone), cleaning and retrying",
			"project_id", in.ProjectID, "path", in.Resolved.HostPath)
		if cleanErr := removeDirContents(in.Resolved.HostPath); cleanErr != nil {
			return fmt.Errorf("git clone failed (dir not empty) and cleanup of %s failed: %w",
				in.Resolved.HostPath, cleanErr)
		}
		if output, err = runClone(); err == nil {
			return nil
		}
		return fmt.Errorf("git clone %s (after cleanup retry): %s", gc.URL, strings.TrimSpace(string(output)))
	}

	return fmt.Errorf("git clone %s: %s", gc.URL, strings.TrimSpace(string(output)))
}

// removeDirContents removes every entry inside dir while leaving dir itself in
// place. The workspace directory is frequently a mount point (e.g. a k8s PVC
// subPath), so it cannot be removed outright — only its contents can be cleared.
func removeDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// WorktreePath returns the canonical worktree path for a given agent within
// a shared base checkout: <hostPath>/<WorktreesSubdir>/<agentID>.
func WorktreePath(hostPath, agentID string) string {
	return filepath.Join(hostPath, WorktreesSubdir, agentID)
}

// validateJoinCandidate reports whether path is a genuine, in-tree worktree
// of base (ValidateWorktreeForBase) and is therefore safe for ensureWorktree
// to attach a joining agent to. source is a short label identifying which
// JOIN discovery path produced the candidate, for the warning log.
//
// This is the full relationship check, not just lexical containment: it
// additionally proves candidate is a real worktree of base (its gitfile
// resolves to base's admin dir and the admin dir's back-link resolves back to
// candidate), which lexical containment alone cannot — a discovery path can
// be steered by an in-tree-but-fake directory or by a rewritten
// base/.git/worktrees/<name>/gitdir back-link pointing outside the tree.
func validateJoinCandidate(base, path, agentID, branchName, source string) bool {
	if err := ValidateWorktreeForBase(base, path); err != nil {
		slog.Warn("ProvisionShared: join candidate failed worktree relationship validation, refusing to join",
			"agent_id", agentID, "branch", branchName, "path", path, "source", source, "error", err)
		return false
	}
	return true
}

// ensureWorktree creates or attaches to a per-agent worktree if the mode is
// WorktreePerAgent. For SharedPlain mode this is a no-op.
//
// Create-or-attach logic (D3 hub-join):
//   - If a worktree for the requested branch already exists (found via the
//     sharer registry or git worktree list), the agent ATTACHES to it (JOIN)
//     and registers as a sharer — no second worktree is created.
//   - Otherwise, a new worktree is created and the agent registers as its
//     first sharer.
//
// The worktree add is done under the already-held advisory lock (design §9.2:
// worktree add/remove touches shared .git metadata).
func ensureWorktree(ctx context.Context, in ProvisionInput) error {
	if in.Mode != store.SharingModeWorktreePerAgent {
		return nil // SharedPlain: nothing to do
	}

	if in.AgentID == "" {
		return fmt.Errorf("ProvisionShared: AgentID is required for WorktreePerAgent mode")
	}

	base := in.Resolved.HostPath
	worktreePath := WorktreePath(base, in.AgentID)

	// Derive a branch name from the agent name or ID.
	branchName := in.AgentID
	if in.AgentName != "" {
		branchName = sanitizeBranchName(in.AgentName)
	}

	// If this agent's own worktree directory already exists, register
	// (idempotent) and return.
	if _, err := os.Stat(worktreePath); err == nil {
		slog.Debug("ProvisionShared: worktree already exists",
			"agent_id", in.AgentID, "path", worktreePath)
		return RegisterSharer(base, "", branchName, worktreePath, in.AgentID)
	}

	// Verify the shared checkout exists (.git dir present).
	gitDir := filepath.Join(base, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("ProvisionShared: shared checkout .git not found at %s — "+
			"cannot create worktree without a cloned repository", gitDir)
	}

	// --- JOIN check: does a worktree for this branch already exist? ---

	// 1. Check the sharer registry.
	sharers, existingWtPath, err := ListSharers(base, "", branchName)
	if err != nil {
		return fmt.Errorf("ProvisionShared: list sharers for branch %q: %w", branchName, err)
	}
	if len(sharers) > 0 && existingWtPath != "" {
		if valErr := ValidateWorktreeForBase(base, existingWtPath); valErr == nil {
			slog.Info("ProvisionShared: joining existing worktree (registry)",
				"agent_id", in.AgentID, "branch", branchName, "path", existingWtPath,
				"existing_sharers", sharers)
			return RegisterSharer(base, "", branchName, existingWtPath, in.AgentID)
		} else {
			slog.Warn("ProvisionShared: registry worktree path failed relationship validation, will create new worktree",
				"agent_id", in.AgentID, "branch", branchName, "stale_path", existingWtPath, "error", valErr)
		}
	}

	// 2. Check git worktree list for a prior-run worktree without a registry entry.
	if existingPath, findErr := findWorktreeForBranch(ctx, base, branchName); findErr == nil && existingPath != "" {
		if validateJoinCandidate(base, existingPath, in.AgentID, branchName, "git-worktree-list") {
			slog.Info("ProvisionShared: joining pre-existing worktree (git)",
				"agent_id", in.AgentID, "branch", branchName, "path", existingPath)
			return RegisterSharer(base, "", branchName, existingPath, in.AgentID)
		}
	}

	// --- CREATE: no existing worktree for this branch ---

	worktreesDir := filepath.Join(base, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0770); err != nil {
		return fmt.Errorf("ProvisionShared: mkdir worktrees dir: %w", err)
	}

	slog.Info("ProvisionShared: creating worktree",
		"agent_id", in.AgentID, "branch", branchName, "path", worktreePath)

	// git worktree add --relative-paths -b <branch> <path>
	// --relative-paths is mandatory for container path-identity (design §6).
	cmd, err := HardenedGitCommand(ctx, base, "worktree", "add", "--relative-paths", "-b", branchName, worktreePath)
	if err != nil {
		return fmt.Errorf("ProvisionShared: %w", err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(output))

		// Branch collision: the proactive JOIN checks above should catch this,
		// but handle defensively in case of a race or stale state.
		if strings.Contains(outputStr, "already checked out") || strings.Contains(outputStr, "already used by worktree") {
			if attachPath, findErr := findWorktreeForBranch(ctx, base, branchName); findErr == nil && attachPath != "" &&
				validateJoinCandidate(base, attachPath, in.AgentID, branchName, "git-fallback") {
				slog.Info("ProvisionShared: attaching to existing worktree (git fallback)",
					"agent_id", in.AgentID, "branch", branchName, "path", attachPath)
				return RegisterSharer(base, "", branchName, attachPath, in.AgentID)
			}
			return fmt.Errorf("git worktree add: branch %q already checked out but cannot find existing worktree: %s",
				branchName, outputStr)
		}

		// If branch already exists (but not checked out), try without -b.
		if strings.Contains(outputStr, "already exists") {
			cmd, err = HardenedGitCommand(ctx, base, "worktree", "add", "--relative-paths", worktreePath, branchName)
			if err != nil {
				return fmt.Errorf("ProvisionShared: %w", err)
			}
			output, err = cmd.CombinedOutput()
			if err != nil {
				reuse := strings.TrimSpace(string(output))
				if strings.Contains(reuse, "already checked out") || strings.Contains(reuse, "already used by worktree") {
					if attachPath, findErr := findWorktreeForBranch(ctx, base, branchName); findErr == nil && attachPath != "" &&
						validateJoinCandidate(base, attachPath, in.AgentID, branchName, "reuse-fallback") {
						slog.Info("ProvisionShared: attaching to existing worktree (reuse fallback)",
							"agent_id", in.AgentID, "branch", branchName, "path", attachPath)
						return RegisterSharer(base, "", branchName, attachPath, in.AgentID)
					}
					return fmt.Errorf("git worktree add: branch %q already checked out: %s", branchName, reuse)
				}
				return fmt.Errorf("git worktree add (reuse branch): %s", reuse)
			}
			return RegisterSharer(base, "", branchName, worktreePath, in.AgentID)
		}

		return fmt.Errorf("git worktree add: %s", outputStr)
	}

	return RegisterSharer(base, "", branchName, worktreePath, in.AgentID)
}

// findWorktreeForBranch parses 'git worktree list --porcelain' output to find
// the worktree path for a given branch. Returns "" if no worktree has that
// branch checked out.
func findWorktreeForBranch(ctx context.Context, repoDir, branch string) (string, error) {
	// Prune first so a worktree dir removed on disk (but not unregistered in git)
	// isn't returned as a stale join target pointing at a non-existent path.
	if pruneCmd, err := HardenedGitCommand(ctx, repoDir, "worktree", "prune"); err == nil {
		_ = pruneCmd.Run()
	}
	cmd, err := HardenedGitCommand(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git worktree list: %w", err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree list: %w", err)
	}
	var currentPath string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		}
		if strings.HasPrefix(line, "branch refs/heads/") {
			b := strings.TrimPrefix(line, "branch refs/heads/")
			if b == branch {
				return currentPath, nil
			}
		}
	}
	return "", nil
}

// prepareBaseForWorktrees configures a freshly cloned base checkout for
// worktree-per-agent use: detaches HEAD (so no branch is "owned" by the base),
// disables auto-gc, sets branch.autoSetupMerge=false, and excludes worktrees/
// from untracked file lists.
//
// branch.autoSetupMerge=false is a mitigation for hub-native worktree mode,
// where .git/config is mounted read-only into the agent container (see
// pkg/runtime/common.go's narrowGitAdminMounts): with config unwritable,
// commands that need to record a new tracking relationship (e.g. `checkout
// -b <local> <remote>/<branch>`, DWIM `switch <remote-branch>`) would
// otherwise fail outright because git cannot write the upstream it just
// computed. Disabling automatic upstream setup means those commands create a
// plain untracked local branch instead of failing. It does not help
// operations that write config for other reasons (`push -u`, `branch -m`,
// `branch --set-upstream-to`) — those still fail or partially apply with
// config read-only; that is a documented limitation of hub-native worktree
// mode, not a regression this setting is meant to cover.
func prepareBaseForWorktrees(ctx context.Context, hostPath string) error {
	if err := gitDetach(ctx, hostPath); err != nil {
		return err
	}

	if err := runHardenedGitConfig(ctx, hostPath, "gc.auto", "0"); err != nil {
		return err
	}
	if err := runHardenedGitConfig(ctx, hostPath, "branch.autoSetupMerge", "false"); err != nil {
		return err
	}

	return appendGitExclude(hostPath, "worktrees/")
}

// runHardenedGitConfig is a small helper for the legitimate broker-side
// `git config <key> <value>` writes in prepareBaseForWorktrees.
func runHardenedGitConfig(ctx context.Context, hostPath, key, value string) error {
	cmd, err := HardenedGitCommand(ctx, hostPath, "config", key, value)
	if err != nil {
		return err
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git config %s %s: %s", key, value, strings.TrimSpace(string(output)))
	}
	return nil
}

// gitDetach detaches HEAD in the repo at hostPath so the base checkout owns
// no branch. Tries 'git switch --detach' first, falls back to 'git checkout
// --detach' for older git versions.
func gitDetach(ctx context.Context, hostPath string) error {
	cmd, err := HardenedGitCommand(ctx, hostPath, "switch", "--detach")
	if err != nil {
		return err
	}
	if _, err := cmd.CombinedOutput(); err == nil {
		return nil
	}
	cmd, err = HardenedGitCommand(ctx, hostPath, "checkout", "--detach")
	if err != nil {
		return err
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git detach: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// appendGitExclude appends a pattern to .git/info/exclude if not already present.
func appendGitExclude(hostPath, pattern string) error {
	excludePath := filepath.Join(hostPath, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return fmt.Errorf("mkdir .git/info: %w", err)
	}
	data, _ := os.ReadFile(excludePath)
	// Exact line match — strings.Contains would false-positive on e.g.
	// "my-worktrees/" or "worktrees/agent-1" and skip appending the pattern.
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == strings.TrimSpace(pattern) {
			return nil
		}
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(pattern + "\n")
	return err
}

// sanitizeBranchName produces a git-safe branch name from an agent name.
func sanitizeBranchName(name string) string {
	// Replace characters invalid in git branch names.
	replacer := strings.NewReplacer(
		" ", "-", "/", "-", "\\", "-", "..", "-",
		"~", "-", "^", "-", ":", "-", "?", "-",
		"*", "-", "[", "-", "]", "-",
	)
	result := replacer.Replace(name)
	// Trim leading/trailing dashes and dots.
	result = strings.Trim(result, "-.")
	if result == "" {
		return "agent"
	}
	return result
}

// chownTarget returns the directory to recursively chown for a freshly
// provisioned workspace.
//
// Broker-side, the project root is the parent of the workspace dir (it also
// holds the shared-dirs siblings), so we chown the parent. But inside a k8s
// init container only the workspace dir itself is mounted (subPath), so its
// parent resolves to the filesystem root "/". Chowning "/" recursively is
// wrong — and a latent security hazard if the pod's security context is ever
// relaxed — so fall back to chowning the workspace dir itself in that case.
func chownTarget(hostPath string) string {
	parent := filepath.Dir(hostPath)
	if parent == "/" || parent == "." {
		return hostPath
	}
	return parent
}

// chownProjectTree sets ownership of the project root and its contents to the
// given UID/GID. This is a ONE-TIME operation done under the advisory lock
// during first provisioning (design §9.1). Per-start chown is NOT done for
// NFS (slow/racy over the network).
func chownProjectTree(ctx context.Context, projectRoot string, uid, gid int) error {
	// Use chown -R for recursive ownership change.
	cmd := exec.CommandContext(ctx, "chown", "-R", fmt.Sprintf("%d:%d", uid, gid), projectRoot)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("chown -R %d:%d %s: %s", uid, gid, projectRoot, strings.TrimSpace(string(output)))
	}
	return nil
}

// resolveUID returns the NFS UID to use for chown, defaulting to 1000.
func resolveUID(in ProvisionInput) int {
	if in.NFSUID != 0 {
		return in.NFSUID
	}
	return 1000
}

// resolveGID returns the NFS GID to use for chown, defaulting to 1000.
func resolveGID(in ProvisionInput) int {
	if in.NFSGID != 0 {
		return in.NFSGID
	}
	return 1000
}

// writeSentinel writes the provisioning sentinel file atomically using
// write-to-temp + rename. The sentinel's existence is the fast-path check
// that short-circuits re-provisioning.
func writeSentinel(path string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".scion-provisioned-*")
	if err != nil {
		return fmt.Errorf("create temp sentinel: %w", err)
	}
	tmpName := tmp.Name()

	// Write a timestamp for debugging.
	_, _ = fmt.Fprintf(tmp, "provisioned_at=%s\n", time.Now().UTC().Format(time.RFC3339))
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp sentinel: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename sentinel: %w", err)
	}
	return nil
}
