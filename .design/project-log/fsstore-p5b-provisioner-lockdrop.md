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
- `pkg/projectfs.Provisioner`:
  - `EnsureSharedLeaf` is the leaf primitive (the confined walk, mode 2775,
    default ACL);
  - `EnsureLeaf(Resolved)` uses it on a shared backend, and on the local
    backend creates the directory as before (0755);
  - `OpenStateDir` creates and checks the state directory and sweeps it;
  - `ProvisionSharedWorkspace` is the shared-plain provisioning of both the
    hub (at project create) and the broker (the backstop before a local
    container starts);
  - `Layout.ProjectTreeIDs` lists the project trees.
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
