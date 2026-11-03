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

// These tests exercise unix-only, Windows-gated agent storage code.
//go:build unix

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// migrateFixture is a pre-switch agent "test-agent" on a shared Layout with
// a docker runtime: its state in the broker-local external agents root, its
// work in <agentDir>/workspace, nothing of it on the share.
type migrateFixture struct {
	sl      *sharedLayoutStarter
	local   string // the broker-local workspace
	share   string // <AgentRoot>/workspace on the share
	agentRt string // <AgentRoot>
}

func newMigrateFixture(t *testing.T) *migrateFixture {
	t.Helper()
	sl := newSharedLayoutStarter(t, "docker")
	_, _, err := sl.start(localRuntimeModes[0].mutate)
	require.NoError(t, err, "fixture: first start")
	require.NoError(t, os.RemoveAll(filepath.Join(sl.root, "agents")))
	local := filepath.Join(sl.ext, "workspace")
	mustMkdirAll(t, local)
	agentRoot := filepath.Join(sl.root, "agents", "test-agent")
	// Plenty of space unless a test says otherwise.
	old := migrateFsAvail
	migrateFsAvail = func(int) (uint64, error) { return 1 << 40, nil }
	t.Cleanup(func() { migrateFsAvail = old })
	return &migrateFixture{sl: sl, local: local, share: filepath.Join(agentRoot, "workspace"), agentRt: agentRoot}
}

// seedRepo makes the broker-local workspace a git repository with two
// commits (unpushed: there is no remote), an uncommitted change, a staged
// file, an untracked file and an ignored file.
func (f *migrateFixture) seedRepo(t *testing.T) {
	t.Helper()
	git := func(args ...string) string {
		return runGitIn(t, f.local, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	}
	git("init", "-q")
	writeModeFile(t, filepath.Join(f.local, "tracked.txt"), "one\n", 0o644)
	writeModeFile(t, filepath.Join(f.local, ".gitignore"), "ignored.log\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "first")
	writeModeFile(t, filepath.Join(f.local, "tracked.txt"), "two\n", 0o644)
	git("commit", "-q", "-am", "second")
	writeModeFile(t, filepath.Join(f.local, "tracked.txt"), "uncommitted\n", 0o644)
	writeModeFile(t, filepath.Join(f.local, "staged.txt"), "staged\n", 0o644)
	git("add", "staged.txt")
	writeModeFile(t, filepath.Join(f.local, "untracked.txt"), "untracked\n", 0o644)
	writeModeFile(t, filepath.Join(f.local, "ignored.log"), "ignored\n", 0o644)
	mustMkdirAll(t, filepath.Join(f.local, "sub", "deeper"))
	writeModeFile(t, filepath.Join(f.local, "sub", "deeper", "x.bin"), "\x00\x01binary", 0o600)
}

func (f *migrateFixture) seedPlain(t *testing.T) {
	t.Helper()
	writeModeFile(t, filepath.Join(f.local, "work.txt"), "work\n", 0o644)
}

func writeModeFile(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(p, []byte(content), mode))
	require.NoError(t, os.Chmod(p, mode))
}

func notRunning(context.Context, string) (bool, error) { return false, nil }

func (f *migrateFixture) migrate(t *testing.T, dryRun bool, running AgentRunningFunc) WorkspaceMigrationResult {
	t.Helper()
	res, err := MigrateAgentWorkspaces(context.Background(), WorkspaceMigrationOptions{
		ProjectDir: f.sl.scionDir,
		Agents:     []string{"test-agent"},
		DryRun:     dryRun,
		Running:    running,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	return res[0]
}

// treeSnapshot lists a tree's entries (path, type, mode, link text or
// content), never following links, for before/after comparisons.
func migrateSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		line := rel + " " + info.Mode().String()
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			require.NoError(t, err)
			line += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			line += " " + string(data)
		}
		out = append(out, line)
		return nil
	}))
	sort.Strings(out)
	return out
}

func assertNoStaging(t *testing.T, f *migrateFixture) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(f.agentRt, migrationStagingName("test-agent")))
	assert.True(t, errors.Is(err, os.ErrNotExist), "no staging directory is left: %v", err)
}

func TestMigrateAgentWorkspaces_DryRunLists(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedRepo(t)
	before := migrateSnapshot(t, f.local)

	r := f.migrate(t, true, notRunning)
	assert.Equal(t, MigrationWouldMigrate, r.Status)
	assert.Equal(t, "test-agent", r.Agent)
	assert.Equal(t, f.local, r.Source)
	assert.Equal(t, f.share, r.Target)
	assert.Positive(t, r.SourceBytes)
	assert.Positive(t, r.SourceEntries)

	assert.Equal(t, before, migrateSnapshot(t, f.local), "a dry run changes nothing")
	assert.NoDirExists(t, filepath.Join(f.sl.root, "agents"), "a dry run writes nothing on the share")
	assert.NoFileExists(t, filepath.Join(f.sl.ext, migrateLockName), "a dry run takes no lock")
}

// A whole-project dry run (no agent named) lists the agents found in the
// broker-local agents root.
func TestMigrateAgentWorkspaces_DryRunSelectsAgents(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	res, err := MigrateAgentWorkspaces(context.Background(), WorkspaceMigrationOptions{
		ProjectDir: f.sl.scionDir, DryRun: true, Running: notRunning,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "test-agent", res[0].Agent)
	assert.Equal(t, MigrationWouldMigrate, res[0].Status)
}

func TestMigrateAgentWorkspaces_CopiesUncommittedUntrackedAndRemovesSource(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedRepo(t)
	before := migrateSnapshot(t, f.local)
	logBefore := runGitIn(t, f.local, "log", "--format=%H %s")
	statusBefore := runGitIn(t, f.local, "status", "--porcelain=v2", "--ignored")

	r := f.migrate(t, false, notRunning)
	require.Equal(t, MigrationMigrated, r.Status, r.Cause)

	assert.Equal(t, before, migrateSnapshot(t, f.share), "every entry, .git, uncommitted, untracked and ignored files included")
	assert.NoDirExists(t, f.local, "the broker-local workspace is removed after verification")
	assertNoStaging(t, f)
	// Test-only git on the fixture repository this test built.
	assert.Equal(t, logBefore, runGitIn(t, f.share, "log", "--format=%H %s"), "unpushed commits survive")
	assert.Equal(t, statusBefore, runGitIn(t, f.share, "status", "--porcelain=v2", "--ignored"))
}

// After migration, a resume on the docker runtime resolves the agent's
// workspace on the share and passes the unchanged workspace source guard.
func TestMigrateAgentWorkspaces_ThenStartPassesGuard(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	resume := func(o *api.StartOptions) {
		localRuntimeModes[0].mutate(o)
		o.FreshProvision = false
		o.RunID = "run-shared-2"
	}
	_, _, err := f.sl.start(resume)
	require.ErrorIs(t, err, ErrAgentWorkspaceOffShare, "fixture: refused before migration")

	r := f.migrate(t, false, notRunning)
	require.Equal(t, MigrationMigrated, r.Status, r.Cause)

	cfg, _, err := f.sl.start(resume)
	require.NoError(t, err, "the migrated agent starts")
	realWS, err := filepath.EvalSymlinks(f.share)
	require.NoError(t, err)
	assert.Equal(t, realWS, cfg.Workspace)
	data, err := os.ReadFile(filepath.Join(f.share, "work.txt"))
	require.NoError(t, err)
	assert.Equal(t, "work\n", string(data))
}

func TestMigrateAgentWorkspaces_RerunIsNoop(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedRepo(t)
	require.Equal(t, MigrationMigrated, f.migrate(t, false, notRunning).Status)
	after := migrateSnapshot(t, f.share)

	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationAlreadyMigrated, r.Status)
	assert.Equal(t, after, migrateSnapshot(t, f.share))
	assert.Equal(t, MigrationAlreadyMigrated, f.migrate(t, true, notRunning).Status)
}

func TestMigrateAgentWorkspaces_RefusesRunning(t *testing.T) {
	for name, running := range map[string]AgentRunningFunc{
		"running": func(context.Context, string) (bool, error) { return true, nil },
		"unknown": func(context.Context, string) (bool, error) { return false, errors.New("runtime unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newMigrateFixture(t)
			f.seedPlain(t)
			before := migrateSnapshot(t, f.local)
			r := f.migrate(t, false, running)
			assert.Equal(t, MigrationRefusedRunning, r.Status)
			assert.Equal(t, before, migrateSnapshot(t, f.local))
			assert.NoDirExists(t, filepath.Join(f.sl.root, "agents"), "nothing is created on the share")
		})
	}
}

// runningOnCall reports running from the n-th running check on.
func runningOnCall(n int32) AgentRunningFunc {
	var calls atomic.Int32
	return func(context.Context, string) (bool, error) { return calls.Add(1) >= n, nil }
}

// The running check immediately before the rename refuses an agent that
// started during the copy: no leaf, the source kept, the staging discarded.
func TestMigrateAgentWorkspaces_RunningCheckBeforeRename(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	before := migrateSnapshot(t, f.local)
	r := f.migrate(t, false, runningOnCall(2))
	assert.Equal(t, MigrationRefusedRunning, r.Status)
	assert.NoDirExists(t, f.share)
	assertNoStaging(t, f)
	assert.Equal(t, before, migrateSnapshot(t, f.local))
}

// An agent started after the rename keeps its broker-local workspace; a
// later run, once it is stopped and both are equal, removes it.
func TestMigrateAgentWorkspaces_StartedBeforeRemovalKeepsSource(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	r := f.migrate(t, false, runningOnCall(3))
	assert.Equal(t, MigrationMigratedSourceKept, r.Status)
	assert.DirExists(t, f.share)
	assert.DirExists(t, f.local)

	r = f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationAlreadyMigrated, r.Status)
	assert.NoDirExists(t, f.local)
}

func TestMigrateAgentWorkspaces_InjectedCopyFailureKeepsSource(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedRepo(t)
	before := migrateSnapshot(t, f.local)
	migrateCopyEntryHook = func(rel string) error {
		if rel == "sub/deeper/x.bin" {
			return errors.New("injected")
		}
		return nil
	}
	t.Cleanup(func() { migrateCopyEntryHook = nil })

	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationFailedCopy, r.Status)
	assert.Equal(t, before, migrateSnapshot(t, f.local), "the source is kept")
	assert.NoDirExists(t, f.share, "nothing is renamed into place")
	assertNoStaging(t, f)
}

// A staging directory left by an interrupted run is discarded and redone.
func TestMigrateAgentWorkspaces_LeftoverStagingIsRedone(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedRepo(t)
	before := migrateSnapshot(t, f.local)
	staging := filepath.Join(f.agentRt, migrationStagingName("test-agent"))
	mustMkdirAll(t, filepath.Join(staging, "half"))
	writeModeFile(t, filepath.Join(staging, "half", "partial"), "partial", 0o644)
	require.NoError(t, os.Chmod(filepath.Dir(f.agentRt), 0o2755))

	r := f.migrate(t, false, notRunning)
	require.Equal(t, MigrationMigrated, r.Status, r.Cause)
	assert.Equal(t, before, migrateSnapshot(t, f.share))
	assertNoStaging(t, f)
}

func TestMigrateAgentWorkspaces_LeafAndLocalConflictRefused(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	mustMkdirAll(t, f.share)
	require.NoError(t, os.Chmod(filepath.Dir(f.agentRt), 0o2755))
	writeModeFile(t, filepath.Join(f.share, "other.txt"), "other\n", 0o644)
	beforeLocal, beforeShare := migrateSnapshot(t, f.local), migrateSnapshot(t, f.share)

	for _, dry := range []bool{true, false} {
		r := f.migrate(t, dry, notRunning)
		assert.Equal(t, MigrationRefusedConflict, r.Status)
	}
	assert.Equal(t, beforeLocal, migrateSnapshot(t, f.local), "the local workspace is untouched")
	assert.Equal(t, beforeShare, migrateSnapshot(t, f.share), "the share workspace is untouched")
}

// A link in the workspace pointing outside it is recreated as a link with
// the same text, never followed: nothing outside is copied, and removing
// the source leaves the outside target as it was.
func TestMigrateAgentWorkspaces_SymlinkOutsideNotFollowed(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	outside := t.TempDir()
	writeModeFile(t, filepath.Join(outside, "secret"), "outside\n", 0o600)
	require.NoError(t, os.Symlink(outside, filepath.Join(f.local, "out-dir")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(f.local, "out-file")))

	r := f.migrate(t, false, notRunning)
	require.Equal(t, MigrationMigrated, r.Status, r.Cause)
	for name, want := range map[string]string{"out-dir": outside, "out-file": filepath.Join(outside, "secret")} {
		info, err := os.Lstat(filepath.Join(f.share, name))
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "%s stays a link", name)
		got, err := os.Readlink(filepath.Join(f.share, name))
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret"))
	require.NoError(t, err, "the outside target survives the source removal")
	assert.Equal(t, "outside\n", string(data))
	assert.Equal(t, []string{"secret -rw------- outside\n"}, migrateSnapshot(t, outside))
}

// Modes are copied (setuid and setgid dropped on files) and, for a root
// migrator, every entry gets the agent owner through descriptor or
// no-follow chowns.
func TestMigrateAgentWorkspaces_TargetOwnerAndMode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as a non-root user with a faked root euid")
	}
	f := newMigrateFixture(t)
	writeModeFile(t, filepath.Join(f.local, "plain"), "p", 0o640)
	writeModeFile(t, filepath.Join(f.local, "tool"), "#!/bin/sh\n", 0o4755)
	mustMkdirAll(t, filepath.Join(f.local, "private"))
	require.NoError(t, os.Chmod(filepath.Join(f.local, "private"), 0o750))
	require.NoError(t, os.Symlink("plain", filepath.Join(f.local, "link")))

	uid, gid := os.Getuid(), os.Getgid()
	var chowns atomic.Int32
	oldEuid, oldF, oldL := migrateGeteuid, migrateFchown, migrateLchownat
	migrateGeteuid = func() int { return 0 }
	migrateFchown = func(fd, u, g int) error {
		assert.Equal(t, uid, u)
		assert.Equal(t, gid, g)
		chowns.Add(1)
		return unix.Fchown(fd, u, g)
	}
	migrateLchownat = func(dirFd int, name string, u, g int) error {
		assert.Equal(t, uid, u)
		chowns.Add(1)
		return unix.Fchownat(dirFd, name, u, g, unix.AT_SYMLINK_NOFOLLOW)
	}
	t.Cleanup(func() { migrateGeteuid, migrateFchown, migrateLchownat = oldEuid, oldF, oldL })

	r := f.migrate(t, false, notRunning)
	require.Equal(t, MigrationMigrated, r.Status, r.Cause)
	assert.Equal(t, int32(4), chowns.Load(), "plain, tool, private and link are chowned")
	for name, want := range map[string]os.FileMode{"plain": 0o640, "tool": 0o755, "private": os.ModeDir | 0o750} {
		info, err := os.Lstat(filepath.Join(f.share, name))
		require.NoError(t, err)
		assert.Equal(t, want, info.Mode()&(os.ModeDir|os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky), name)
	}
}

// An entry owned by another user that the agent could not read refuses the
// agent (C2); one it could read is admitted.
func TestMigrateScan_UnreadableForeignEntryRefused(t *testing.T) {
	dir := t.TempDir()
	writeModeFile(t, filepath.Join(dir, "readable"), "r", 0o644)
	fd, err := openDirPath(dir)
	require.NoError(t, err)
	defer func() { _ = unix.Close(fd) }()
	other := os.Getuid() + 1

	_, err = scanTree(fd, &scanPolicy{source: true, agentUID: other, agentGID: -1})
	require.NoError(t, err, "a world-readable foreign entry is admitted")

	writeModeFile(t, filepath.Join(dir, "private"), "p", 0o600)
	_, err = scanTree(fd, &scanPolicy{source: true, agentUID: other, agentGID: -1})
	var o *migrateOutcome
	require.ErrorAs(t, err, &o)
	assert.Equal(t, MigrationRefusedUnsupported, o.status)
	assert.Contains(t, o.cause, "(1)", "a count only")
	assert.NotContains(t, o.cause, "private", "no workspace names")

	_, err = scanTree(fd, &scanPolicy{source: true, agentUID: other, agentGID: os.Getgid()})
	require.Error(t, err, "group read is required for the agent's gid; 0600 grants none")
}

func TestMigrateAgentWorkspaces_SpecialFileRefused(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	require.NoError(t, unix.Mkfifo(filepath.Join(f.local, "pipe"), 0o644))
	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationRefusedUnsupported, r.Status)
	assert.Equal(t, causeSpecialFile, r.Cause)
	assert.FileExists(t, filepath.Join(f.local, "work.txt"))
}

// A link planted at <AgentRoot>/workspace or at the staging name is never
// followed, and the run refuses with nothing changed (C3').
func TestMigrateAgentWorkspaces_PlantedLinkAtTargetRefused(t *testing.T) {
	for _, name := range []string{"workspace", migrationStagingName("test-agent")} {
		t.Run(name, func(t *testing.T) {
			f := newMigrateFixture(t)
			f.seedPlain(t)
			mustMkdirAll(t, f.agentRt)
			require.NoError(t, os.Chmod(filepath.Dir(f.agentRt), 0o2755))
			outside := t.TempDir()
			require.NoError(t, os.Symlink(outside, filepath.Join(f.agentRt, name)))

			r := f.migrate(t, false, notRunning)
			assert.Equal(t, MigrationRefusedConflict, r.Status)
			assert.Empty(t, migrateSnapshot(t, outside), "nothing is written through the link")
			info, err := os.Lstat(filepath.Join(f.agentRt, name))
			require.NoError(t, err)
			assert.NotZero(t, info.Mode()&os.ModeSymlink, "the link is not removed")
			assert.FileExists(t, filepath.Join(f.local, "work.txt"))
		})
	}
}

// A link swapped in at the staging name after it is created is not
// followed: the staging descriptor check refuses the run.
func TestMigrateAgentWorkspaces_LinkSwappedAfterMkdirNotFollowed(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	outside := t.TempDir()
	staging := filepath.Join(f.agentRt, migrationStagingName("test-agent"))
	migrateStagingCreatedHook = func() {
		require.NoError(t, os.Rename(staging, staging+".moved"))
		require.NoError(t, os.Symlink(outside, staging))
	}
	t.Cleanup(func() { migrateStagingCreatedHook = nil })

	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationRefusedConflict, r.Status)
	assert.Empty(t, migrateSnapshot(t, outside), "nothing is written through the link")
	assert.NoDirExists(t, f.share)
	assert.FileExists(t, filepath.Join(f.local, "work.txt"))
}

// The share's agents directory must be owned by the migrator and not
// group- or other-writable (C3').
func TestMigrateAgentWorkspaces_WritableAgentsParentRefused(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	mustMkdirAll(t, f.agentRt)
	require.NoError(t, os.Chmod(filepath.Dir(f.agentRt), 0o2775))
	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationRefusedUnsupported, r.Status)
	assert.Equal(t, causeTargetParentUnsafe, r.Cause)
	assert.NoDirExists(t, f.share)
}

func TestMigrateAgentWorkspaces_LowSpaceRefused(t *testing.T) {
	f := newMigrateFixture(t)
	f.seedPlain(t)
	migrateFsAvail = func(int) (uint64, error) { return 1024, nil }
	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationRefusedUnsupported, r.Status)
	assert.Equal(t, causeInsufficientSpace, r.Cause)
	assert.NoDirExists(t, f.share)
	assertNoStaging(t, f)
	assert.FileExists(t, filepath.Join(f.local, "work.txt"))
}

// Without RENAME_NOREPLACE (EINVAL, as on many NFS clients) the fallback
// renames only when the name is absent; a leaf that appeared before the
// rename is a conflict, on either path, and the source is kept.
func TestMigrateAgentWorkspaces_RenameFallback(t *testing.T) {
	einval := func(int, string, string) error { return unix.EINVAL }
	t.Run("fallback migrates", func(t *testing.T) {
		f := newMigrateFixture(t)
		f.seedPlain(t)
		old := migrateRenameNoReplace
		migrateRenameNoReplace = einval
		t.Cleanup(func() { migrateRenameNoReplace = old })
		r := f.migrate(t, false, notRunning)
		require.Equal(t, MigrationMigrated, r.Status, r.Cause)
		assert.FileExists(t, filepath.Join(f.share, "work.txt"))
	})
	for name, noReplace := range map[string]func(int, string, string) error{"fallback": einval, "noreplace": nil} {
		t.Run(name+" leaf appeared", func(t *testing.T) {
			f := newMigrateFixture(t)
			f.seedPlain(t)
			if noReplace != nil {
				old := migrateRenameNoReplace
				migrateRenameNoReplace = noReplace
				t.Cleanup(func() { migrateRenameNoReplace = old })
			}
			migrateBeforeRenameHook = func() { require.NoError(t, os.Mkdir(f.share, 0o755)) }
			t.Cleanup(func() { migrateBeforeRenameHook = nil })
			r := f.migrate(t, false, notRunning)
			assert.Equal(t, MigrationRefusedConflict, r.Status)
			assert.Empty(t, migrateSnapshot(t, f.share), "the empty leaf is not replaced")
			assertNoStaging(t, f)
			assert.FileExists(t, filepath.Join(f.local, "work.txt"))
		})
	}
}

// An empty broker-local workspace is left as it is, as Start leaves it.
func TestMigrateAgentWorkspaces_EmptyLocalSkipped(t *testing.T) {
	f := newMigrateFixture(t)
	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationSkipped, r.Status)
	assert.DirExists(t, f.local)
	assert.NoDirExists(t, filepath.Join(f.sl.root, "agents"))
}

// A broker-local workspace that cannot be read refuses the agent; it is
// never taken for empty, and is left as it is.
func TestMigrateAgentWorkspaces_UnreadableLocalRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 directory")
	}
	f := newMigrateFixture(t)
	f.seedPlain(t)
	require.NoError(t, os.Chmod(f.local, 0))
	t.Cleanup(func() { _ = os.Chmod(f.local, 0o755) })
	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationRefusedUnsupported, r.Status)
	assert.Equal(t, causeSourceUnreadable, r.Cause)
	assert.NoDirExists(t, filepath.Join(f.sl.root, "agents"))
	require.NoError(t, os.Chmod(f.local, 0o755))
	assert.FileExists(t, filepath.Join(f.local, "work.txt"))
}

// A workspace holding only provisioning artifacts is empty, as Start
// judges it, and is skipped.
func TestMigrateAgentWorkspaces_ProvisioningOnlyLocalSkipped(t *testing.T) {
	f := newMigrateFixture(t)
	mustMkdirAll(t, filepath.Join(f.local, ".scion"))
	r := f.migrate(t, false, notRunning)
	assert.Equal(t, MigrationSkipped, r.Status)
	assert.Equal(t, causeLocalWorkspaceEmpty, r.Cause)
	assert.DirExists(t, filepath.Join(f.local, ".scion"))
}

// The start refusal of a pre-switch agent names the migration as the remedy,
// keeps the recreate path, and names no path (the broker log has those).
func TestAgentWorkspaceOffShareNamesMigration(t *testing.T) {
	msg := ErrAgentWorkspaceOffShare.Error()
	assert.Contains(t, msg, "scion runtime-broker migrate-workspaces")
	assert.Contains(t, msg, "on the Runtime Broker host")
	assert.Contains(t, msg, "recreate the agent")
	assert.NotContains(t, msg, "/", "no path in the client text")
}

func TestAgentContainerLive(t *testing.T) {
	list := func(phase string) func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		return func(_ context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			assert.Equal(t, "a1", filter["scion.name"])
			return []api.AgentInfo{{Name: "a1", Phase: phase}}, nil
		}
	}
	for _, rtName := range []string{"docker", "podman"} {
		live, err := NewManager(&runtime.MockRuntime{NameFunc: func() string { return rtName }, ListFunc: list("running")}).AgentContainerLive(context.Background(), "a1", "")
		require.NoError(t, err)
		assert.True(t, live, rtName)
		live, err = NewManager(&runtime.MockRuntime{NameFunc: func() string { return rtName }, ListFunc: list("stopped")}).AgentContainerLive(context.Background(), "a1", "")
		require.NoError(t, err)
		assert.False(t, live, rtName)
	}
	_, err := NewManager(&runtime.MockRuntime{NameFunc: func() string { return "docker" }, ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		return nil, errors.New("down")
	}}).AgentContainerLive(context.Background(), "a1", "")
	require.Error(t, err, "a lookup error fails closed")
	_, err = NewManager(&runtime.MockRuntime{NameFunc: func() string { return "kubernetes" }}).AgentContainerLive(context.Background(), "a1", "")
	require.ErrorIs(t, err, errWorkspaceMigrationUnsupportedRuntime)
	assert.False(t, strings.Contains(err.Error(), "/"))
}
