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

//go:build unix

package provision

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appendGitExclude never reads an over-cap exclude file: the pattern is
// appended without the duplicate check, after a newline because the file
// does not end with one, so the exclude is not lost.
func TestAppendGitExclude_OverCapAppendsWithoutRead(t *testing.T) {
	ws := t.TempDir()
	info := filepath.Join(ws, ".git", "info")
	require.NoError(t, os.MkdirAll(info, 0o755))
	exclude := filepath.Join(info, "exclude")
	require.NoError(t, os.WriteFile(exclude, []byte("worktrees/\n"), 0o644))
	const size = 64 << 20
	require.NoError(t, os.Truncate(exclude, size)) // sparse; the last byte is NUL

	require.NoError(t, appendGitExclude(ws, "/x"))
	fi, err := os.Stat(exclude)
	require.NoError(t, err)
	assert.Equal(t, int64(size+len("\n/x\n")), fi.Size())
	f, err := os.Open(exclude)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	tail := make([]byte, 4)
	_, err = f.ReadAt(tail, size)
	require.NoError(t, err)
	assert.Equal(t, "\n/x\n", string(tail))
}

// paddedGitdir is a valid "gitdir: <target>" line followed by enough
// whitespace to exceed the pointer cap; TrimSpace would accept it.
func paddedGitdir(target string) []byte {
	return []byte("gitdir: " + target + "\n" + strings.Repeat(" ", gitPointerMaxBytes))
}

// newLinkedWorktree makes <base>/.git/worktrees/<name> with a gitdir file
// pointing back to <base>/worktrees/<name>/.git, and that worktree's .git
// file; it returns the base and the worktree path.
func newLinkedWorktree(t *testing.T, name string) (base, wt string) {
	t.Helper()
	base = filepath.Join(t.TempDir(), "workspace")
	admin := filepath.Join(base, ".git", "worktrees", name)
	wt = filepath.Join(base, "worktrees", name)
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.MkdirAll(wt, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(wt, ".git")+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
	return base, wt
}

// The fixture is a real linked worktree for these checks.
func TestBoundedGitPointers_Fixture(t *testing.T) {
	base, wt := newLinkedWorktree(t, "agent-1")
	assert.True(t, IsRealWorktreeDir(wt, base))
	_, ok := worktreeAdminDir(wt, base)
	assert.True(t, ok)
	assert.Equal(t, linkedWorktreeOK, linkedWorktreeState(base, wt))
}

// An over-cap worktree .git file is not read, even when its content (after
// TrimSpace) is a valid pointer.
func TestIsRealWorktreeDir_OverCapGitFileRefused(t *testing.T) {
	base, wt := newLinkedWorktree(t, "agent-1")
	admin := filepath.Join(base, ".git", "worktrees", "agent-1")
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), paddedGitdir(admin), 0o644))
	assert.False(t, IsRealWorktreeDir(wt, base))
	_, ok := worktreeAdminDir(wt, base)
	assert.False(t, ok)
}

// An over-cap admin gitdir file is not read: the entry counts as a mismatch.
func TestLinkedWorktreeState_OverCapGitdirRefused(t *testing.T) {
	base, wt := newLinkedWorktree(t, "agent-1")
	gitdir := filepath.Join(base, ".git", "worktrees", "agent-1", "gitdir")
	require.NoError(t, os.WriteFile(gitdir, paddedGitdir(filepath.Join(wt, ".git"))[len("gitdir: "):], 0o644))
	assert.Equal(t, linkedWorktreeMismatch, linkedWorktreeState(base, wt))
}

// A symlinked worktree .git file is not followed by worktreeAdminDir (it
// was before; IsRealWorktreeDir already refused it), nor a symlinked gitdir.
func TestBoundedGitPointers_SymlinksNotFollowed(t *testing.T) {
	base, wt := newLinkedWorktree(t, "agent-1")
	admin := filepath.Join(base, ".git", "worktrees", "agent-1")
	elsewhere := filepath.Join(t.TempDir(), "dotgit")
	require.NoError(t, os.WriteFile(elsewhere, []byte("gitdir: "+admin+"\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(wt, ".git")))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(wt, ".git")))
	_, ok := worktreeAdminDir(wt, base)
	assert.False(t, ok, "a symlinked .git is not followed")
	assert.False(t, IsRealWorktreeDir(wt, base))

	base2, wt2 := newLinkedWorktree(t, "agent-2")
	gitdir := filepath.Join(base2, ".git", "worktrees", "agent-2", "gitdir")
	other := filepath.Join(t.TempDir(), "gitdir")
	require.NoError(t, os.WriteFile(other, []byte(filepath.Join(wt2, ".git")+"\n"), 0o644))
	require.NoError(t, os.Remove(gitdir))
	require.NoError(t, os.Symlink(other, gitdir))
	assert.Equal(t, linkedWorktreeMismatch, linkedWorktreeState(base2, wt2), "a symlinked gitdir is not followed")
}

// readSmallFileNoFollow: a FIFO never blocks and is refused; a device is
// refused; a file at the cap is read.
func TestReadSmallFileNoFollow(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))
	done := make(chan error, 1)
	go func() { _, err := readSmallFileNoFollow(fifo, gitPointerMaxBytes); done <- err }()
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("readSmallFileNoFollow blocked on a FIFO")
	}
	_, err := readSmallFileNoFollow("/dev/zero", gitPointerMaxBytes)
	assert.Error(t, err)

	ok := filepath.Join(dir, "ok")
	require.NoError(t, os.WriteFile(ok, make([]byte, gitPointerMaxBytes), 0o644))
	data, err := readSmallFileNoFollow(ok, gitPointerMaxBytes)
	require.NoError(t, err)
	assert.Len(t, data, gitPointerMaxBytes)
	require.NoError(t, os.WriteFile(ok, make([]byte, gitPointerMaxBytes+1), 0o644))
	_, err = readSmallFileNoFollow(ok, gitPointerMaxBytes)
	assert.ErrorIs(t, err, errFileTooLarge)
}
