# Project storage phase 5b: one Provisioner, lock-litter GC on every open, workspace-root lock and marker dropped

**Date**: 2026-10-27
**Issues**: ptone/scion#5834 and ptone/scion#5836 (lifecycle phase ptone/scion#5833), tracking ptone/scion#5805
**Stacks on**: phase 5a (deletion owned by the authority)

## Summary

Before this phase, the provisioning sentinel and lock had two homes. The
Kubernetes init container kept them in the project's provisioning state
directory (`<subpath_root>/<project-id>/provision`), but every provisioner
also took a second lock in the workspace root, still read a sentinel left
there by older images, and added it to the checkout's git excludes. An init
container without the state-directory mount kept both in the workspace.
Leaf creation was spread over the hub and the brokers, each calling
`shareddirs.EnsureLeaf` directly, and `POST /shared-dirs` created nothing.

Now:

- The state directory is the only place the sentinel and lock live.
  Nothing reads, writes, locks or git-excludes anything in the workspace
  root, except a one-shot promotion step at hub start.
- Every Scion-owned directory is created through one Provisioner in
  `pkg/projectfs`.
- Lock litter is swept on every open of the state directory.

## Pieces

- `pkg/provision`:
  - `ProvisionInput.LegacyDir` is removed, with the ordered legacy-then-state
    lock pair and `excludeLegacySentinel`;
  - `SentinelDirs` is the sentinel directory alone;
  - `ProvisionShared` and `RemoveMountedWorktree` refuse a sentinel
    directory that is the workspace or inside it;
  - `RemoveMountedWorktree` takes only the state-directory lock (`""`
    means `ProjectStateDir(base)`);
  - a sentinel or lock entry in the workspace root is workspace content now
    (the clone refuses such a workspace, naming the entries);
  - `garbageCollectLockLitter` also runs on the shared-plain pre-lock fast
    path, and lists before probing the server clock, so an open with
    nothing to sweep writes nothing;
  - `PromoteLegacyMarker` is the promotion step (`legacy_promote.go`, the
    only file allowed to name the workspace-root marker and lock; a scan
    test over `pkg/`, `cmd/` and `extras/` enforces that).
- `sciontool provision`: shared-plain and worktree-per-agent fail without
  `SCION_PROVISION_STATE_DIR` ("this pod has no provisioning state
  directory; the runtime broker must be upgraded"), before anything is
  created.
- `pkg/projectfs` (store-free leaf primitives):
  - `EnsureSharedLeaf` is the leaf primitive (the confined walk, mode 2775,
    default ACL);
  - `Resolved.EnsureLeaf` uses it on a shared backend, and on the local
    backend creates the directory as before (0755);
  - `Layout.ProvisionStateDir` resolves `<ProjectRoot>/provision`, and
    `Layout.ProjectTreeIDs` lists the project trees.
- `pkg/projectfs/provisioner.Provisioner`:
  - `EnsureLeaf` (over `Resolved.EnsureLeaf`);
  - `OpenStateDir` creates and checks the state directory and sweeps it;
  - `ProvisionSharedWorkspace` is the shared-plain provisioning of both the
    hub (at project create) and the broker (the backstop before a local
    container starts).
- Why the Provisioner is a separate package: `pkg/config`, and through it
  the conduit relay, imports `pkg/projectfs`, and the relay must never reach
  `pkg/store` (`TestRelayCodeUsesOnlyRegistryStore`). `pkg/provision` imports
  `pkg/store`, so what needs it lives in `pkg/projectfs/provisioner`, and
  `pkg/projectfs` keeps depending only on `pkg/api` and `pkg/shareddirs`.
  The first CI run caught a version that had the Provisioner in
  `pkg/projectfs`.
- Hub:
  - `provisionSharedWorkspace`, the workspace leaf in `populateAgentConfig`
    and the shared-dir write path go through the Provisioner;
  - `POST /shared-dirs` creates the leaf where the hub can resolve it (best
    effort; first use still creates it otherwise);
  - `startLegacyProvisionPromotion` runs the promotion in the background at
    every start, with one log line per project it changes or fails on and a
    summary line.
- Broker and agent manager:
  - the backstop uses the Provisioner;
  - `nfsSharedCheckoutProvisioned`, `worktreeBaseIsProvisioned` and
    `sharedWorkspaceProvisioned` read the state directory only;
  - a worktree removal that cannot use the state directory leaves the
    worktree in place;
  - every leaf creation calls `projectfs.EnsureSharedLeaf`.

## The promotion step

The step runs for every project tree on the hub's shared workspace Layout.
It acts on a workspace with a workspace-root marker, its exclude line, or
workspace-root lock litter. Under the project's provisioning locks it:

1. writes the state-directory sentinel when only the workspace-root marker
   (a regular file) says the workspace is provisioned;
2. removes the marker and its `/.scion-provisioned` exclude line;
3. removes workspace-root lock litter older than twice the stale threshold,
   probing the server clock in the state directory.

A live workspace-root lock is never removed. A tree with nothing to promote
is skipped without a lock or a write, so a second run changes nothing. For
worktree-per-agent projects with live worktrees, the next start reuses the
base: no clone and no recursive chown.

## Upgrade notes (documented in `hosted/ha/kubernetes.md`)

- Agent images from before the provisioning state directory provision again
  on every Pod start and must be rebuilt.
- Pods built by a broker without state-directory support fail provisioning
  until the broker is upgraded.
- Workspace-root entries the promotion keeps (a live lock, or entries an
  older image writes again) block a first clone until removed.

## Not changed

- The non-git create-time bootstrap (`resolveGCSWorkspaceDir`,
  `applySharedBootstrap`, `AppliedConfig.WorkspaceStoragePath`) stays, per
  design v9. Its "workspace in use" check now reads the state-directory
  sentinel only.
- `config.EnsureSharedDirs` (local backend, CLI) still creates directories
  itself, with the same mode.

## Verification

See the PR's acceptance criteria section for the test names.

## Merge-forward onto phase 5a's final head (2026-10-27)

P5b moved from its original base (phase 5a at eb51fb5a4) to phase 5a's
merge-forward head 607bfcfd. That head is phase 5a plus phase 3b e3e8208c,
plus main 807e67c5.

Conflicts: main split code into `_unix`/`_other` files for the Windows build
of `./cmd/scion`, and three P5b edits sat in moved code:
`ensureNFSLeafClaim` (now in `nfs_leaf_unix.go`), `nfsRemovalStateDir` (now
in `nfs_removal_state_unix.go`) and the migration's state-directory leaf (now
in `project_storage_migrate_unix.go`). Each was resolved by taking
upstream's split and re-applying the P5b change in the moved code.

Windows build: the same pattern covers P5b's own new code.
- `pkg/projectfs/leaf_other.go` makes `EnsureSharedLeaf`,
  `Resolved.EnsureLeaf` and `Layout.ProjectTreeIDs` fail with
  `errors.ErrUnsupported`.
- `Layout.ProvisionStateDir` and `provisionDir` move to files built on every
  platform.
- `pkg/projectfs/provisioner` builds everywhere; its methods fail with
  `ErrUnsupported` off unix.

The no-compile checks are recorded with the merge: the Windows identifier
and method scans, `GOOS=windows go list` over `./cmd/scion` and the Windows
test package list, the duplicate-identifier scan, and gofmt.

## Bounded reads of agent-writable files in the promotion step

The workspace root is agent-writable, so the promotion must not trust what
it finds there.
- The workspace's `.git/info/exclude` is read only through `openGitExclude`
  (each component opened with `O_NOFOLLOW`, `O_NONBLOCK`, and a regular-file
  check on the opened descriptor). A file above 1 MiB is skipped with a
  warning, and the read itself goes through a limited reader. A symlink, a
  FIFO or a device is never followed or read.
- The workspace root is listed in chunks, and only lock-litter names are
  kept, at most 4096 per pass (`lockLitterEntries`, shared with the
  recurring state-directory sweep). A root with very many entries is never
  held in memory as a whole.
