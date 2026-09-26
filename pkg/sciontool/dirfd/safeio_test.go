/*
Copyright 2026 The Scion Authors.
*/

package dirfd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------------- ReadFileNoFollow / ReadAtNoFollow ----------------

func TestReadFileNoFollow_NormalRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := ReadFileNoFollow(path, 1024)
	if err != nil {
		t.Fatalf("ReadFileNoFollow: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
}

func TestReadFileNoFollow_MissingFileReportsNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadFileNoFollow(filepath.Join(dir, "missing"), 1024)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

// TestReadFileNoFollow_RefusesSymlinkAtLeaf fails if the leaf open ever
// drops O_NOFOLLOW: a symlink swapped in at the target path must be refused
// rather than transparently read through.
func TestReadFileNoFollow_RefusesSymlinkAtLeaf(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ReadFileNoFollow(link, 1024)
	if err == nil {
		t.Fatal("expected an error reading through a symlink, got nil")
	}
}

// TestReadFileNoFollow_RefusesIntermediateSymlink fails if the walk to the
// leaf's parent ever stops checking anything but the final component.
func TestReadFileNoFollow_RefusesIntermediateSymlink(t *testing.T) {
	dir := t.TempDir()
	attacker := filepath.Join(dir, "attacker")
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	victim := filepath.Join(attacker, "victim")
	if err := os.WriteFile(victim, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	scionDir := filepath.Join(dir, ".scion")
	if err := os.Symlink(attacker, scionDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ReadFileNoFollow(filepath.Join(scionDir, "victim"), 1024)
	if err == nil {
		t.Fatal("expected an error walking through a symlinked intermediate directory, got nil")
	}
}

// TestReadFileNoFollow_RefusesHardlinkedRegularFile fails if the Nlink==1
// check is removed: a hardlink to an unrelated (possibly root-owned) file
// still looks like an ordinary regular file to a bare S_IFREG check.
func TestReadFileNoFollow_RefusesHardlinkedRegularFile(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original")
	if err := os.WriteFile(original, []byte("victim-content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	linked := filepath.Join(dir, "linked")
	if err := os.Link(original, linked); err != nil {
		t.Fatalf("hardlink: %v", err)
	}

	_, err := ReadFileNoFollow(linked, 1024)
	if !errors.Is(err, ErrNotSingleLinkRegular) {
		t.Fatalf("expected ErrNotSingleLinkRegular, got %v", err)
	}
}

// TestReadFileNoFollow_BoundsOversizedContent fails if the LimitReader cap
// is removed or widened: a file one byte over the limit must be refused,
// never buffered in full.
func TestReadFileNoFollow_BoundsOversizedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big")
	if err := os.WriteFile(path, make([]byte, 101), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := ReadFileNoFollow(path, 100)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}

	// Exactly at the limit must still succeed.
	if err := os.WriteFile(path, make([]byte, 100), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if _, err := ReadFileNoFollow(path, 100); err != nil {
		t.Fatalf("expected content exactly at the limit to succeed, got %v", err)
	}
}

// TestReadAtNoFollow_FifoDoesNotHang proves a FIFO with no writer is
// refused immediately rather than hanging the caller. This fails if
// O_NONBLOCK is dropped from the open, or if the non-regular-file check is
// removed so the code proceeds to a blocking read.
func TestReadAtNoFollow_FifoDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := ReadFileNoFollow(fifoPath, 1024)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error reading a FIFO, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReadFileNoFollow blocked on a FIFO with no writer")
	}
}

// ---------------- WriteFileNoFollow ----------------

func TestWriteFileNoFollow_NormalWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := WriteFileNoFollow(path, []byte(`{"a":1}`), 0o644, 0, 0); err != nil {
		t.Fatalf("WriteFileNoFollow: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Errorf("content = %q", data)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 0644", st.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly the final file to remain, got %v", entries)
	}
}

// TestWriteFileNoFollow_ChownsTempFdBeforeRename mirrors
// writeLimitsState's own equivalent test: a non-root test can't chown to an
// arbitrary uid, but chowning to its own current uid/gid is always
// permitted, which is enough to prove the fd-based call succeeds.
func TestWriteFileNoFollow_ChownsTempFdBeforeRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := WriteFileNoFollow(path, []byte("x"), 0o644, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("WriteFileNoFollow: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("expected only the final file to remain, got %v", entries)
	}
}

// TestWriteFileNoFollow_ChmodTargetsTempFdNotSwappedPath proves the mode is
// set via fchmod on the already-open temp fd, not by a path-based chmod
// that a symlink swapped into the temp file's directory entry could
// redirect. It replaces the temp file's directory entry with a symlink to
// an unrelated file in the window between write and chmod (via the
// package's own test hook, the same technique walk_test.go uses for the
// chown walk's races) and then asserts that unrelated file's mode was never
// touched. This fails if WriteFileNoFollow is reverted to a path-based
// os.Chmod(tmpPath, mode) call.
func TestWriteFileNoFollow_ChmodTargetsTempFdNotSwappedPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "root-owned-secret")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	finalPath := filepath.Join(dir, "agent-info.json")

	writeNoFollowPreChmodTestHook = func(tmpName string) {
		tmpPath := filepath.Join(dir, tmpName)
		if err := os.Remove(tmpPath); err != nil {
			t.Fatalf("remove temp: %v", err)
		}
		if err := os.Symlink(target, tmpPath); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	defer func() { writeNoFollowPreChmodTestHook = nil }()

	// The call may succeed or fail depending on what the final rename does
	// with the swapped-in symlink; either way, the target's permissions
	// must never change.
	_ = WriteFileNoFollow(finalPath, []byte(`{"a":1}`), 0o644, 0, 0)

	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("chmod followed the swapped-in symlink: target mode = %o, want unchanged 0600", st.Mode().Perm())
	}
}

// TestWriteFileNoFollow_RemovesTempOnChownFailure proves the temp file is
// cleaned up rather than left behind when a later step (here, an fchown to
// a uid this test process isn't permitted to use) fails after the file was
// already created.
func TestWriteFileNoFollow_RemovesTempOnChownFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can chown to any uid; this test needs the call to fail")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// uid 1 ("daemon" on most systems) is never the current test uid and a
	// non-root process cannot chown to it — that's the point.
	err := WriteFileNoFollow(path, []byte("x"), 0o644, 1, 1)
	if err == nil {
		t.Fatal("expected chown to an unpermitted uid to fail")
	}

	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("readdir: %v", rerr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected the temp file to be cleaned up after a failed write, got %v", entries)
	}
}

// ---------------- ReadUnderRootNoFollow ----------------

func TestReadUnderRootNoFollow_NormalRead(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(sub, "secret.txt")
	if err := os.WriteFile(path, []byte("sk-test"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := ReadUnderRootNoFollow(root, path, 1024)
	if err != nil {
		t.Fatalf("ReadUnderRootNoFollow: %v", err)
	}
	if string(data) != "sk-test" {
		t.Errorf("content = %q", data)
	}
}

// TestReadUnderRootNoFollow_RefusesSymlinkInRootPointingOutside fails if
// containment is checked only by comparing path strings: the symlink's own
// name sits inside root, but its target does not, and the fd-anchored walk
// must refuse it rather than follow it out.
func TestReadUnderRootNoFollow_RefusesSymlinkInRootPointingOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("leaked"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ReadUnderRootNoFollow(root, link, 1024)
	if err == nil {
		t.Fatal("expected an error reading a symlink whose name is inside root but whose target is not, got nil")
	}
}

// TestReadUnderRootNoFollow_RefusesParentEscape fails if the containment
// check is dropped or bypassed: a path that textually walks back out of
// root via ".." must be refused before any filesystem call, not resolved.
func TestReadUnderRootNoFollow_RefusesParentEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("leaked"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	escaping := filepath.Join(root, "..", filepath.Base(outside), "secret")
	_, err := ReadUnderRootNoFollow(root, escaping, 1024)
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected ErrPathEscapesRoot, got %v", err)
	}
}

func TestReadUnderRootNoFollow_BoundsOversizedContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big")
	if err := os.WriteFile(path, make([]byte, 101), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := ReadUnderRootNoFollow(root, path, 100)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

// TestReadUnderRootNoFollow_RefusesIntermediateSymlinkInsideRoot fails if
// only the leaf component is checked: an intermediate directory swapped for
// a symlink must also be refused, even though it never appears to escape
// root as a path string (the symlink's target might itself be some other
// directory inside root, or entirely outside it).
func TestReadUnderRootNoFollow_RefusesIntermediateSymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	attacker := filepath.Join(root, "attacker")
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	victim := filepath.Join(attacker, "victim")
	if err := os.WriteFile(victim, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Symlink(attacker, sub); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ReadUnderRootNoFollow(root, filepath.Join(sub, "victim"), 1024)
	if err == nil {
		t.Fatal("expected an error walking through a symlinked intermediate directory inside root, got nil")
	}
}

func TestRelUnderRoot_RejectsRootItself(t *testing.T) {
	root := t.TempDir()
	if _, err := relUnderRoot(root, root); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected ErrPathEscapesRoot for root itself, got %v", err)
	}
}

func TestRelUnderRoot_AcceptsNestedPath(t *testing.T) {
	root := t.TempDir()
	rel, err := relUnderRoot(root, filepath.Join(root, "a", "b"))
	if err != nil {
		t.Fatalf("relUnderRoot: %v", err)
	}
	want := filepath.Join("a", "b")
	if rel != want {
		t.Fatalf("rel = %q, want %q", rel, want)
	}
}

func TestRelUnderRoot_RejectsSiblingThatSharesPrefix(t *testing.T) {
	root := t.TempDir()
	sibling := root + "-sibling"
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	if !strings.HasPrefix(sibling, root) {
		t.Fatalf("test setup: expected %q to share a string prefix with %q", sibling, root)
	}
	if _, err := relUnderRoot(root, filepath.Join(sibling, "file")); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected ErrPathEscapesRoot for a sibling directory sharing root's string prefix, got %v", err)
	}
}
