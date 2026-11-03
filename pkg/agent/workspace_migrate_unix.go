//go:build unix

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
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/GoogleCloudPlatform/scion/pkg/provision"
	"github.com/GoogleCloudPlatform/scion/pkg/shareddirs"
)

// migrateMaxDepth caps the directory depth the migration walks, as
// shareddirs caps its delete walk: a workspace's contents come from an
// agent.
const migrateMaxDepth = 4096

// migrateSpaceMinMargin is the least free space left on the share's
// filesystem beyond the copy's estimated size.
const migrateSpaceMinMargin = 64 << 20

// migrateLockName is the broker-local lock file in the agent's directory
// that keeps two migrations of the same agent apart.
const migrateLockName = ".migrate.lock"

// Seams for tests. Production never overrides them outside tests.
var (
	// migrateGeteuid decides whether copied entries are chowned (only a
	// root migrator can give them the agent owner).
	migrateGeteuid = unix.Geteuid
	// migrateFchown and migrateLchownat give a copied entry its owner.
	migrateFchown   = unix.Fchown
	migrateLchownat = func(dirFd int, name string, uid, gid int) error {
		return unix.Fchownat(dirFd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
	}
	// migrateFsAvail is the free space, in bytes, of the filesystem fd is on.
	migrateFsAvail = fsAvailBytes
	// migrateRenameNoReplace renames from to to under dirFd without
	// replacing an existing to (renameNoReplace).
	migrateRenameNoReplace = renameNoReplace
	// migrateRenameAt is the plain rename the fallback uses.
	migrateRenameAt = func(dirFd int, from, to string) error { return unix.Renameat(dirFd, from, dirFd, to) }
	// migrateCopyEntryHook, when set, runs before each entry is copied (rel
	// is its path in the workspace) and fails the copy with its error.
	migrateCopyEntryHook func(rel string) error
	// migrateStagingCreatedHook, when set, runs right after the staging
	// directory is created and before it is opened from <AgentRoot>.
	migrateStagingCreatedHook func()
	// migrateBeforeRenameHook, when set, runs after the last running check
	// and before the rename into place.
	migrateBeforeRenameHook func()
)

// workspaceMigrationSupported reports whether this platform can migrate.
func workspaceMigrationSupported() error { return nil }

// fsAvailBytes is the space available to the caller on fd's filesystem.
func fsAvailBytes(fd int) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec,unconvert // Statfs_t field types vary by platform; both are non-negative.
}

// migrateOutcome ends one agent's migration with a status and fixed cause.
type migrateOutcome struct {
	status WorkspaceMigrationStatus
	cause  string
}

func (o *migrateOutcome) Error() string { return string(o.status) + ": " + o.cause }

func outcome(status WorkspaceMigrationStatus, cause string) *migrateOutcome {
	return &migrateOutcome{status: status, cause: cause}
}

// migrateAgentWorkspace runs one agent's migration (MigrateAgentWorkspaces):
//
//  1. Source. The broker-local agent directory and its workspace are opened
//     without following links. An exclusive lock on <agentDir>/.migrate.lock
//     (broker-local) is held for the rest of the run (not in a dry run). A
//     missing workspace, or one Start treats as empty (isWorkspaceEmptyDir),
//     is skipped and left as it is, as Start leaves it.
//  2. Running check, through the runtime (opts.Running).
//  3. Scan. The source manifest is built; a special file, an entry owned by
//     another user that the agent cannot read, a linked git worktree or a
//     tree over migrateMaxDepth refuses the agent.
//  4. Target. <AgentRoot>'s parent on the share must be owned by the
//     migrator and not group- or other-writable; <AgentRoot> is opened from
//     it without following links and must be a directory owned by the
//     migrator or the agent owner. Every later step goes through that
//     descriptor. An existing workspace leaf equal to the source (the full
//     manifest) is already migrated, and the source is removed (after a
//     running check); a differing one, or a link or non-directory at either
//     name, is a conflict and nothing is touched. A leftover staging
//     directory is discarded (a no-follow, contained delete).
//  5. A dry run stops here: nothing has been written.
//  6. Space: the share's filesystem must hold the source's estimated size
//     plus a margin.
//  7. Staging. <AgentRoot>/.migrating-<agent> is created with the NFS leaf
//     helper (ensureNFSLeafClaim: no-follow walk, the leaf mode and default
//     ACL), opened from the <AgentRoot> descriptor without following links,
//     and must be the same, empty directory, owned by the migrator. The
//     copy writes only through that descriptor.
//  8. Copy, then verify: the staging manifest must equal the source's.
//  9. Running check, immediately before the rename.
//  10. Rename: staging to workspace under the <AgentRoot> descriptor, never
//     replacing an existing entry (renameat2 RENAME_NOREPLACE; where that is
//     unsupported, as on many NFS clients, an fstatat on the same
//     descriptor must show the name absent just before a plain renameat).
//  11. Running check, then removal of the broker-local workspace (a no-follow,
//     contained delete anchored at the broker-local agents root).
//
// Every failure before step 10 keeps the source and discards the staging
// directory (or leaves it for the next run to discard).
func migrateAgentWorkspace(ctx context.Context, p workspaceMigrationPlan, dryRun bool, running AgentRunningFunc, res WorkspaceMigrationResult) WorkspaceMigrationResult {
	m := &workspaceMigration{ctx: ctx, p: p, dryRun: dryRun, running: running, res: res}
	defer m.close()
	err := m.run()
	var o *migrateOutcome
	switch {
	case err == nil:
	case errors.As(err, &o):
		m.res.Status, m.res.Cause = o.status, o.cause
	default:
		// An unexpected error: the agent is refused with a fixed cause; the
		// detail goes to the log only, without workspace names.
		slog.Warn("workspace migration failed", "agent", p.agent, "status", MigrationFailedCopy)
		m.res.Status, m.res.Cause = MigrationFailedCopy, causeCopyFailed
	}
	return m.res
}

// workspaceMigration is one agent's run state: the descriptors it holds.
type workspaceMigration struct {
	ctx     context.Context
	p       workspaceMigrationPlan
	dryRun  bool
	running AgentRunningFunc
	res     WorkspaceMigrationResult

	agentsFd, agentFd, lockFd, srcFd int
	parentFd, agentRootFd            int
	opened                           []*int
}

func (m *workspaceMigration) hold(fd *int) { m.opened = append(m.opened, fd) }

func (m *workspaceMigration) close() {
	for _, fd := range m.opened {
		if *fd >= 0 {
			_ = unix.Close(*fd)
			*fd = -1
		}
	}
}

func (m *workspaceMigration) checkRunning() error {
	live, err := m.running(m.ctx, m.p.agent)
	if err != nil {
		return outcome(MigrationRefusedRunning, causeRunningUnknown)
	}
	if live {
		return outcome(MigrationRefusedRunning, causeRunning)
	}
	return nil
}

func (m *workspaceMigration) run() error {
	m.agentsFd, m.agentFd, m.lockFd, m.srcFd, m.parentFd, m.agentRootFd = -1, -1, -1, -1, -1, -1
	for _, fd := range []*int{&m.srcFd, &m.agentRootFd, &m.parentFd, &m.lockFd, &m.agentFd, &m.agentsFd} {
		m.hold(fd)
	}

	// 1. Source.
	var err error
	if m.agentsFd, err = openDirPath(m.p.agentsRoot); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return outcome(MigrationSkipped, causeNoLocalWorkspace)
		}
		return err
	}
	if m.agentFd, err = openDirAt(m.agentsFd, m.p.agent); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return outcome(MigrationSkipped, causeNoLocalWorkspace)
		}
		return outcome(MigrationRefusedUnsupported, causeSourceNotDir)
	}
	if !m.dryRun {
		if err := m.lock(); err != nil {
			return err
		}
	}
	if m.srcFd, err = openDirAt(m.agentFd, provision.AgentWorkspaceDir); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return m.noSource()
		}
		return outcome(MigrationRefusedUnsupported, causeSourceNotDir)
	}
	if isWorkspaceEmptyDir(m.p.source()) {
		return outcome(MigrationSkipped, causeLocalWorkspaceEmpty)
	}

	// 2. Running check.
	if err := m.checkRunning(); err != nil {
		return err
	}

	// 3. Scan.
	var srcRoot unix.Stat_t
	if err := unix.Fstat(m.srcFd, &srcRoot); err != nil {
		return err
	}
	owner := migrationOwner(m.p.owner, srcRoot)
	src, err := scanTree(m.srcFd, &scanPolicy{agentUID: int(srcRoot.Uid), agentGID: int(srcRoot.Gid), owner: owner, source: true})
	if err != nil {
		return err
	}
	m.res.SourceBytes, m.res.SourceEntries = src.bytes, len(src.entries)-1

	// 4. Target.
	leafExists, stagingExists, err := m.openTarget(owner)
	if err != nil {
		return err
	}
	if leafExists {
		return m.existingLeaf(src, owner)
	}

	// 5. Dry run.
	if m.dryRun {
		m.res.Status = MigrationWouldMigrate
		return nil
	}
	if stagingExists {
		if err := m.discardStaging(); err != nil {
			return outcome(MigrationFailedCopy, causeCopyFailed)
		}
	}

	// 6. Space.
	if m.agentRootFd >= 0 {
		if err := checkMigrationSpace(m.agentRootFd, src); err != nil {
			return err
		}
	} else if err := checkMigrationSpace(m.parentFd, src); err != nil {
		return err
	}

	// 7. Staging.
	if err := m.ensureAgentRoot(owner); err != nil {
		return err
	}
	stagingFd, err := m.createStaging()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stagingFd) }()

	// 8. Copy, then verify.
	c := &treeCopier{owner: owner}
	if err := c.copyTree(stagingFd, m.srcFd, ".", 0); err != nil {
		_ = m.discardStaging()
		var o *migrateOutcome
		if errors.As(err, &o) {
			return o
		}
		return outcome(MigrationFailedCopy, causeCopyFailed)
	}
	dst, err := scanTree(stagingFd, &scanPolicy{owner: owner})
	if err != nil || !src.equal(dst) {
		_ = m.discardStaging()
		return outcome(MigrationFailedVerify, causeVerifyMismatch)
	}

	// 9. Running check, immediately before the rename.
	if err := m.checkRunning(); err != nil {
		_ = m.discardStaging()
		return err
	}
	if migrateBeforeRenameHook != nil {
		migrateBeforeRenameHook()
	}

	// 10. Rename into place.
	if err := m.renameIntoPlace(stagingFd); err != nil {
		return err
	}

	// 11. Running check, then removal of the source.
	return m.removeSource(MigrationMigrated)
}

// lock takes the broker-local migration lock of the agent, never following
// a link at its name.
func (m *workspaceMigration) lock() error {
	fd, err := unix.Openat(m.agentFd, migrateLockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return outcome(MigrationRefusedConflict, causeLocked)
	}
	m.lockFd = fd
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return outcome(MigrationRefusedConflict, causeLocked)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return outcome(MigrationRefusedConflict, causeLocked)
	}
	return nil
}

// noSource reports an agent with no broker-local workspace: already
// migrated when its workspace is on the share, otherwise nothing to do.
func (m *workspaceMigration) noSource() error {
	fd, existed, err := openTreeDir(m.p.hostBase, path.Join(filepath.ToSlash(m.p.agentRootRel), provision.AgentWorkspaceDir))
	if err == nil && existed {
		_ = unix.Close(fd)
		m.res.Status = MigrationAlreadyMigrated
		return nil
	}
	return outcome(MigrationSkipped, causeNoLocalWorkspace)
}

// openTarget opens <AgentRoot>'s parent and, when it exists, <AgentRoot>,
// checking both (step 4), and reports whether the workspace leaf and a
// staging directory exist under it. A link or non-directory at either name
// is a conflict.
func (m *workspaceMigration) openTarget(owner shareddirs.Owner) (leafExists, stagingExists bool, err error) {
	rel := filepath.ToSlash(m.p.agentRootRel)
	parentFd, existed, err := openTreeDir(m.p.hostBase, path.Dir(rel))
	if err != nil {
		return false, false, outcome(MigrationRefusedUnsupported, causeTargetParentUnsafe)
	}
	if !existed {
		// Nothing of this project's agents is on the share yet: the
		// parent is created with <AgentRoot> (ensureAgentRoot). Check
		// space against the deepest existing directory.
		hostFd, err := openDirPath(m.p.hostBase)
		if err != nil {
			return false, false, outcome(MigrationRefusedUnsupported, causeTargetParentUnsafe)
		}
		m.parentFd = hostFd
		return false, false, nil
	}
	m.parentFd = parentFd
	var st unix.Stat_t
	if err := unix.Fstat(parentFd, &st); err != nil {
		return false, false, err
	}
	if int(st.Uid) != unix.Geteuid() || st.Mode&0o022 != 0 {
		return false, false, outcome(MigrationRefusedUnsupported, causeTargetParentUnsafe)
	}
	fd, err := openDirAt(parentFd, path.Base(rel))
	if errors.Is(err, unix.ENOENT) {
		return false, false, nil
	}
	if err != nil {
		return false, false, outcome(MigrationRefusedConflict, causeTargetNotDir)
	}
	m.agentRootFd = fd
	if err := unix.Fstat(fd, &st); err != nil {
		return false, false, err
	}
	if int(st.Uid) != unix.Geteuid() && (owner.UID <= 0 || int(st.Uid) != owner.UID) {
		return false, false, outcome(MigrationRefusedUnsupported, causeTargetParentUnsafe)
	}
	if leafExists, err = dirEntryIsDir(fd, provision.AgentWorkspaceDir); err != nil {
		return false, false, err
	}
	if stagingExists, err = dirEntryIsDir(fd, migrationStagingName(m.p.agent)); err != nil {
		return false, false, err
	}
	return leafExists, stagingExists, nil
}

// dirEntryIsDir reports whether name exists under dirFd as a directory. A
// link or any other type there is a conflict, never followed.
func dirEntryIsDir(dirFd int, name string) (bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(dirFd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return false, outcome(MigrationRefusedConflict, causeTargetNotDir)
	}
	return true, nil
}

// existingLeaf handles an agent whose workspace leaf is already on the
// share: equal to the source under the full manifest, the source is
// removed (or would be, in a dry run); otherwise a conflict.
func (m *workspaceMigration) existingLeaf(src *treeManifest, owner shareddirs.Owner) error {
	leafFd, err := openDirAt(m.agentRootFd, provision.AgentWorkspaceDir)
	if err != nil {
		return outcome(MigrationRefusedConflict, causeTargetNotDir)
	}
	defer func() { _ = unix.Close(leafFd) }()
	leaf, err := scanTree(leafFd, &scanPolicy{owner: owner})
	if err != nil || !src.equal(leaf) {
		return outcome(MigrationRefusedConflict, causeLeafDiffers)
	}
	if m.dryRun {
		m.res.Status = MigrationAlreadyMigrated
		return nil
	}
	return m.removeSource(MigrationAlreadyMigrated)
}

// ensureAgentRoot creates <AgentRoot> when it does not exist, with the NFS
// leaf helper Start uses for the agent's directory (ensureNFSLeafClaim),
// and opens and checks it as openTarget does.
func (m *workspaceMigration) ensureAgentRoot(owner shareddirs.Owner) error {
	if m.agentRootFd >= 0 {
		return nil
	}
	if _, _, err := ensureNFSLeafClaim(m.p.hostBase, m.p.agentRootRel); err != nil {
		return outcome(MigrationRefusedUnsupported, causeTargetParentUnsafe)
	}
	_ = unix.Close(m.parentFd)
	m.parentFd = -1
	leafExists, stagingExists, err := m.openTarget(owner)
	if err != nil {
		return err
	}
	if m.agentRootFd < 0 || leafExists || stagingExists {
		return outcome(MigrationRefusedConflict, causeRenameConflict)
	}
	return nil
}

// createStaging creates the staging directory (step 7) and returns its
// descriptor, opened from <AgentRoot>'s and checked to be the directory the
// helper created: same inode, empty, owned by the migrator.
func (m *workspaceMigration) createStaging() (int, error) {
	stagingRel := filepath.Join(m.p.agentRootRel, migrationStagingName(m.p.agent))
	fd, existed, err := shareddirs.EnsureLeaf(m.p.hostBase, stagingRel)
	if err != nil {
		return -1, outcome(MigrationFailedCopy, causeCopyFailed)
	}
	var created unix.Stat_t
	statErr := unix.Fstat(fd, &created)
	_ = shareddirs.CloseFd(fd)
	if existed || statErr != nil {
		return -1, outcome(MigrationRefusedConflict, causeStagingRaced)
	}
	if migrateStagingCreatedHook != nil {
		migrateStagingCreatedHook()
	}
	sfd, err := openDirAt(m.agentRootFd, migrationStagingName(m.p.agent))
	if err != nil {
		return -1, outcome(MigrationRefusedConflict, causeStagingRaced)
	}
	var st unix.Stat_t
	if err := unix.Fstat(sfd, &st); err != nil || st.Dev != created.Dev || st.Ino != created.Ino ||
		int(st.Uid) != unix.Geteuid() || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(sfd)
		return -1, outcome(MigrationRefusedConflict, causeStagingRaced)
	}
	names, err := readDirNamesFd(sfd)
	if err != nil || len(names) != 0 {
		_ = unix.Close(sfd)
		return -1, outcome(MigrationRefusedConflict, causeStagingRaced)
	}
	return sfd, nil
}

// discardStaging removes the staging directory with the no-follow,
// contained delete (shareddirs.DeleteTree): a link at its name is refused,
// never followed.
func (m *workspaceMigration) discardStaging() error {
	_, err := shareddirs.DeleteTree(m.p.hostBase, filepath.Join(m.p.agentRootRel, migrationStagingName(m.p.agent)))
	return err
}

// renameIntoPlace renames the staging directory to the workspace leaf under
// the <AgentRoot> descriptor without replacing an existing entry (step 10).
// The staging descriptor must still name the directory at the staging name.
func (m *workspaceMigration) renameIntoPlace(stagingFd int) error {
	staging := migrationStagingName(m.p.agent)
	var want, got unix.Stat_t
	if err := unix.Fstat(stagingFd, &want); err != nil {
		return outcome(MigrationFailedCopy, causeCopyFailed)
	}
	if err := unix.Fstatat(m.agentRootFd, staging, &got, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		got.Dev != want.Dev || got.Ino != want.Ino {
		return outcome(MigrationRefusedConflict, causeStagingRaced)
	}
	err := migrateRenameNoReplace(m.agentRootFd, staging, provision.AgentWorkspaceDir)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		// No RENAME_NOREPLACE here (common on NFS). A plain rename would
		// replace an existing empty directory, so the name must be absent
		// at an fstatat on the same descriptor immediately before it. Start
		// does not create the leaf while a non-empty broker-local workspace
		// remains (checkLocalRuntimeAgentWorkspace); the window between the
		// two calls is disclosed in the flip procedure.
		var st unix.Stat_t
		if serr := unix.Fstatat(m.agentRootFd, provision.AgentWorkspaceDir, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(serr, unix.ENOENT) {
			_ = m.discardStaging()
			return outcome(MigrationRefusedConflict, causeRenameConflict)
		}
		err = migrateRenameAt(m.agentRootFd, staging, provision.AgentWorkspaceDir)
	}
	if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
		_ = m.discardStaging()
		return outcome(MigrationRefusedConflict, causeRenameConflict)
	}
	if err != nil {
		_ = m.discardStaging()
		return outcome(MigrationFailedCopy, causeCopyFailed)
	}
	return nil
}

// removeSource removes the broker-local workspace after a running check
// (step 11), with the no-follow, contained delete anchored at the
// broker-local agents root: <agentsRoot>/<agent>/workspace, every component
// opened without following links, entries unlinked, never followed.
func (m *workspaceMigration) removeSource(done WorkspaceMigrationStatus) error {
	if err := m.checkRunning(); err != nil {
		return outcome(MigrationMigratedSourceKept, causeStartedBeforeRemoval)
	}
	// Re-check that the workspace name still names the directory scanned.
	var want, got unix.Stat_t
	if err := unix.Fstat(m.srcFd, &want); err != nil {
		return outcome(MigrationMigratedSourceKept, causeRemovalFailed)
	}
	if err := unix.Fstatat(m.agentFd, provision.AgentWorkspaceDir, &got, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		got.Dev != want.Dev || got.Ino != want.Ino {
		return outcome(MigrationMigratedSourceKept, causeRemovalFailed)
	}
	if _, err := shareddirs.DeleteTree(m.p.agentsRoot, path.Join(m.p.agent, provision.AgentWorkspaceDir)); err != nil {
		return outcome(MigrationMigratedSourceKept, causeRemovalFailed)
	}
	m.res.Status = done
	return nil
}

// migrationOwner is the owner copied entries get: the Layout's owner when
// it names a non-root uid and gid, otherwise the owner of the broker-local
// workspace root (the agent's own uid and gid on this broker).
func migrationOwner(layout shareddirs.Owner, srcRoot unix.Stat_t) shareddirs.Owner {
	if layout.UID > 0 && layout.GID > 0 {
		return layout
	}
	return shareddirs.Owner{UID: int(srcRoot.Uid), GID: int(srcRoot.Gid)}
}

// chownsEntries reports whether copied entries are chowned to owner: only a
// root migrator can, and only to a non-root owner (as
// shareddirs.EnsureLeafOwned decides for a leaf).
func chownsEntries(owner shareddirs.Owner) bool {
	return owner.UID > 0 && owner.GID > 0 && migrateGeteuid() == 0
}

// checkMigrationSpace refuses a copy the share's filesystem cannot hold
// with a margin: the source's bytes, a block per entry, and 5% (at least
// migrateSpaceMinMargin) more.
func checkMigrationSpace(fd int, src *treeManifest) error {
	need := uint64(src.bytes) + uint64(len(src.entries))*4096 //nolint:gosec // non-negative sizes
	margin := need / 20
	if margin < migrateSpaceMinMargin {
		margin = migrateSpaceMinMargin
	}
	avail, err := migrateFsAvail(fd)
	if err != nil || avail < need+margin {
		return outcome(MigrationRefusedUnsupported, causeInsufficientSpace)
	}
	return nil
}

// ---- descriptors -----------------------------------------------------------

// openDirPath opens a configuration-derived directory path.
func openDirPath(p string) (int, error) {
	return unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// openDirAt opens name under dirFd as a directory, never following a link.
func openDirAt(dirFd int, name string) (int, error) {
	return unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// openTreeDir opens <hostBase>/<rel>, every component of rel without
// following links. existed is false when a component is missing.
func openTreeDir(hostBase, rel string) (fd int, existed bool, err error) {
	base, err := openDirPath(hostBase)
	if err != nil {
		return -1, false, err
	}
	cur := base
	for _, comp := range splitSlash(rel) {
		next, err := openDirAt(cur, comp)
		_ = unix.Close(cur)
		if errors.Is(err, unix.ENOENT) {
			return -1, false, nil
		}
		if err != nil {
			return -1, false, err
		}
		cur = next
	}
	return cur, true, nil
}

func splitSlash(rel string) []string {
	var out []string
	for _, c := range strings.Split(path.Clean(rel), "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

// readDirNamesFd lists the names in the directory dirFd, sorted, through a
// duplicate of the descriptor (dirFd stays open).
func readDirNamesFd(dirFd int) ([]string, error) {
	dup, err := unix.Dup(dirFd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "dir") //nolint:gosec // a valid descriptor from Dup
	defer func() { _ = f.Close() }()
	if _, err := unix.Seek(dup, 0, io.SeekStart); err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// ---- manifest --------------------------------------------------------------

// manifestEntry is one entry of a tree: its path, type, size, mode, owner,
// link text and content hash. Modification times are not part of it (they
// are copied, but a directory's changes as entries are created in it).
type manifestEntry struct {
	rel      string
	typ      uint32
	size     int64
	perm     uint32
	uid, gid int
	link     string
	sum      [sha256.Size]byte
}

// treeManifest is a tree's manifest, in walk order, and its total size.
type treeManifest struct {
	entries []manifestEntry
	bytes   int64
}

// equal reports whether two manifests list the same entries.
func (a *treeManifest) equal(b *treeManifest) bool {
	if a == nil || b == nil || len(a.entries) != len(b.entries) {
		return false
	}
	for i := range a.entries {
		if a.entries[i] != b.entries[i] {
			return false
		}
	}
	return true
}

// scanPolicy says how scanTree records a tree.
type scanPolicy struct {
	// source marks the broker-local workspace: entries are checked (special
	// files, unreadable foreign-owned entries, a linked worktree) and
	// recorded with the mode and owner the copy gives them.
	source bool
	// agentUID and agentGID are the agent's identity on the source.
	agentUID, agentGID int
	// owner is the copy's owner; recorded only when chownsEntries.
	owner shareddirs.Owner

	unreadable int
}

// copiedPerm is the mode a copy gives an entry of type typ with mode mode:
// a file loses setuid, setgid and sticky; a directory keeps sticky.
func copiedPerm(typ, mode uint32) uint32 {
	switch typ {
	case unix.S_IFDIR:
		return mode & 0o1777
	case unix.S_IFLNK:
		return 0
	default:
		return mode & 0o777
	}
}

// agentCanRead reports whether the agent (uid, gid) could read the entry
// before the migration: it owns it, or the entry grants read (and, for a
// directory, search) to its group with the agent's gid, or to others.
func (sp *scanPolicy) agentCanRead(st *unix.Stat_t) bool {
	typ := uint32(st.Mode) & unix.S_IFMT //nolint:unconvert // Mode's type varies by platform
	if typ == unix.S_IFLNK || int(st.Uid) == sp.agentUID {
		return true
	}
	need := uint32(0o4)
	if typ == unix.S_IFDIR {
		need = 0o5
	}
	mode := uint32(st.Mode) //nolint:unconvert // Mode's type varies by platform
	if mode&need == need {
		return true
	}
	return int(st.Gid) == sp.agentGID && (mode>>3)&need == need
}

// scanTree builds the manifest of the tree at rootFd, never following a
// link. The root's own mode and owner are not recorded (the workspace
// leaf's are the leaf helper's).
func scanTree(rootFd int, sp *scanPolicy) (*treeManifest, error) {
	m := &treeManifest{}
	m.entries = append(m.entries, manifestEntry{rel: ".", typ: unix.S_IFDIR})
	if sp.source {
		var st unix.Stat_t
		err := unix.Fstatat(rootFd, ".git", &st, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil && uint32(st.Mode)&unix.S_IFMT == unix.S_IFREG { //nolint:unconvert // Mode's type varies by platform
			return nil, outcome(MigrationRefusedUnsupported, causeLinkedWorktree)
		}
	}
	if err := sp.walk(rootFd, ".", 0, m); err != nil {
		return nil, err
	}
	if sp.source && sp.unreadable > 0 {
		slog.Warn("workspace migration refused: entries owned by another user that the agent cannot read", "count", sp.unreadable)
		return nil, outcome(MigrationRefusedUnsupported, fmt.Sprintf("%s (%d)", causeUnreadableForeign, sp.unreadable))
	}
	return m, nil
}

func (sp *scanPolicy) walk(dirFd int, rel string, depth int, m *treeManifest) error {
	if depth >= migrateMaxDepth {
		return outcome(MigrationRefusedUnsupported, causeTooDeep)
	}
	names, err := readDirNamesFd(dirFd)
	if err != nil {
		return err
	}
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(dirFd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		typ := uint32(st.Mode) & unix.S_IFMT //nolint:unconvert // Mode's type varies by platform
		e := manifestEntry{rel: path.Join(rel, name), typ: typ, uid: -1, gid: -1}
		if sp.source {
			switch typ {
			case unix.S_IFDIR, unix.S_IFREG, unix.S_IFLNK:
			default:
				return outcome(MigrationRefusedUnsupported, causeSpecialFile)
			}
			if !sp.agentCanRead(&st) {
				sp.unreadable++
			}
			e.perm = copiedPerm(typ, uint32(st.Mode)) //nolint:unconvert // Mode's type varies by platform
			if chownsEntries(sp.owner) {
				e.uid, e.gid = sp.owner.UID, sp.owner.GID
			}
		} else {
			e.perm = uint32(st.Mode) & 0o7777 //nolint:unconvert // Mode's type varies by platform
			if typ == unix.S_IFLNK {
				e.perm = 0
			}
			if chownsEntries(sp.owner) {
				e.uid, e.gid = int(st.Uid), int(st.Gid)
			}
		}
		switch typ {
		case unix.S_IFLNK:
			if e.link, err = readlinkAt(dirFd, name); err != nil {
				return err
			}
		case unix.S_IFREG:
			e.size = st.Size
			m.bytes += st.Size
			if e.sum, err = hashFileAt(dirFd, name, &st); err != nil {
				return err
			}
		}
		m.entries = append(m.entries, e)
		if typ == unix.S_IFDIR {
			sub, err := openDirAt(dirFd, name)
			if err != nil {
				return err
			}
			err = sp.walk(sub, e.rel, depth+1, m)
			_ = unix.Close(sub)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// readlinkAt reads the text of the link name under dirFd.
func readlinkAt(dirFd int, name string) (string, error) {
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(dirFd, name, buf)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
}

// openFileAt opens the regular file name under dirFd for reading, never
// following a link and never blocking on a FIFO swapped in, and checks it
// is the file want describes.
func openFileAt(dirFd int, name string, want *unix.Stat_t) (*os.File, error) {
	fd, err := unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || uint32(st.Mode)&unix.S_IFMT != unix.S_IFREG || st.Dev != want.Dev || st.Ino != want.Ino { //nolint:unconvert // Mode's type varies by platform
		_ = unix.Close(fd)
		return nil, outcome(MigrationFailedCopy, causeCopyFailed)
	}
	return os.NewFile(uintptr(fd), name), nil //nolint:gosec // a valid descriptor from Openat
}

func hashFileAt(dirFd int, name string, st *unix.Stat_t) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := openFileAt(dirFd, name, st)
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// ---- copy ------------------------------------------------------------------

// treeCopier copies a workspace into the staging directory.
type treeCopier struct {
	owner shareddirs.Owner
}

// copyTree copies the entries of srcDir into dstDir (both descriptors),
// never following a link on either side: directories recursively, regular
// files by content, links by their text. A directory's mode and times are
// set after its entries are written.
func (c *treeCopier) copyTree(dstDir, srcDir int, rel string, depth int) error {
	if depth >= migrateMaxDepth {
		return outcome(MigrationRefusedUnsupported, causeTooDeep)
	}
	names, err := readDirNamesFd(srcDir)
	if err != nil {
		return err
	}
	for _, name := range names {
		entryRel := path.Join(rel, name)
		if migrateCopyEntryHook != nil {
			if err := migrateCopyEntryHook(entryRel); err != nil {
				return err
			}
		}
		var st unix.Stat_t
		if err := unix.Fstatat(srcDir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch uint32(st.Mode) & unix.S_IFMT { //nolint:unconvert // Mode's type varies by platform
		case unix.S_IFDIR:
			err = c.copyDir(dstDir, srcDir, name, entryRel, &st, depth)
		case unix.S_IFREG:
			err = c.copyFile(dstDir, srcDir, name, &st)
		case unix.S_IFLNK:
			err = c.copyLink(dstDir, srcDir, name, &st)
		default:
			err = outcome(MigrationRefusedUnsupported, causeSpecialFile)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *treeCopier) copyDir(dstDir, srcDir int, name, rel string, st *unix.Stat_t, depth int) error {
	if err := unix.Mkdirat(dstDir, name, 0o700); err != nil {
		return err
	}
	src, err := openDirAt(srcDir, name)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(src) }()
	var sst unix.Stat_t
	if err := unix.Fstat(src, &sst); err != nil || sst.Dev != st.Dev || sst.Ino != st.Ino {
		return outcome(MigrationFailedCopy, causeCopyFailed)
	}
	dst, err := openDirAt(dstDir, name)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dst) }()
	if err := c.copyTree(dst, src, rel, depth+1); err != nil {
		return err
	}
	if chownsEntries(c.owner) {
		if err := migrateFchown(dst, c.owner.UID, c.owner.GID); err != nil {
			return err
		}
	}
	if err := unix.Fchmod(dst, copiedPerm(unix.S_IFDIR, uint32(st.Mode))); err != nil { //nolint:unconvert // Mode's type varies by platform
		return err
	}
	return setTimesAt(dstDir, name, st)
}

func (c *treeCopier) copyFile(dstDir, srcDir int, name string, st *unix.Stat_t) error {
	src, err := openFileAt(srcDir, name, st)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	fd, err := unix.Openat(dstDir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	dst := os.NewFile(uintptr(fd), name) //nolint:gosec // a valid descriptor from Openat
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if chownsEntries(c.owner) {
		if err := migrateFchown(fd, c.owner.UID, c.owner.GID); err != nil {
			_ = dst.Close()
			return err
		}
	}
	if err := unix.Fchmod(fd, copiedPerm(unix.S_IFREG, uint32(st.Mode))); err != nil { //nolint:unconvert // Mode's type varies by platform
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return setTimesAt(dstDir, name, st)
}

func (c *treeCopier) copyLink(dstDir, srcDir int, name string, st *unix.Stat_t) error {
	target, err := readlinkAt(srcDir, name)
	if err != nil {
		return err
	}
	if err := unix.Symlinkat(target, dstDir, name); err != nil {
		return err
	}
	if chownsEntries(c.owner) {
		if err := migrateLchownat(dstDir, name, c.owner.UID, c.owner.GID); err != nil {
			return err
		}
	}
	return setTimesAt(dstDir, name, st)
}

// setTimesAt gives name under dirFd st's access and modification times,
// never following a link.
func setTimesAt(dirFd int, name string, st *unix.Stat_t) error {
	ts := []unix.Timespec{st.Atim, st.Mtim}
	return unix.UtimesNanoAt(dirFd, name, ts, unix.AT_SYMLINK_NOFOLLOW)
}
