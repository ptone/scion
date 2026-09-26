/*
Copyright 2026 The Scion Authors.
*/

package dirfd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenParentNoFollow_NormalWrite(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	leafPath := filepath.Join(sub, "file")

	fd, leaf, err := OpenParentNoFollow(leafPath)
	if err != nil {
		t.Fatalf("OpenParentNoFollow: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if leaf != "file" {
		t.Errorf("leaf = %q, want %q", leaf, "file")
	}

	f, err := CreateExclAt(fd, leaf, 0o600)
	if err != nil {
		t.Fatalf("CreateExclAt: %v", err)
	}
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(leafPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
}

// TestOpenParentNoFollow_RefusesIntermediateSymlink proves the walk checks
// every path component, not just the final one: replacing an intermediate
// directory (the kind of thing a process that owns $HOME can always do to
// $HOME/.scion) with a symlink must make the walk fail instead of
// transparently following it into the symlink's target.
func TestOpenParentNoFollow_RefusesIntermediateSymlink(t *testing.T) {
	dir := t.TempDir()
	attacker := filepath.Join(dir, "attacker-dir")
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatalf("mkdir attacker: %v", err)
	}
	victim := filepath.Join(attacker, "victim")
	if err := os.WriteFile(victim, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	// ".scion" is normally a real directory under $HOME; here it's a
	// symlink to an attacker-controlled directory instead.
	scionDir := filepath.Join(dir, ".scion")
	if err := os.Symlink(attacker, scionDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	leafPath := filepath.Join(scionDir, "scion-token")
	if _, _, err := OpenParentNoFollow(leafPath); err == nil {
		t.Fatal("expected an error walking through a symlinked intermediate directory, got nil")
	}

	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(data) != "do-not-touch" {
		t.Errorf("victim was modified: %q", data)
	}
}

func TestOpenParentNoFollow_MissingParentErrors(t *testing.T) {
	dir := t.TempDir()
	leafPath := filepath.Join(dir, "does-not-exist", "file")
	if _, _, err := OpenParentNoFollow(leafPath); err == nil {
		t.Fatal("expected an error for a missing parent directory, got nil")
	}
}

func TestRefuseSymlinkOrNonRegularAt(t *testing.T) {
	dir := t.TempDir()
	fd, err := syscall.Open(dir, syscall.O_DIRECTORY|syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open dir: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()

	t.Run("missing is fine", func(t *testing.T) {
		if err := RefuseSymlinkOrNonRegularAt(fd, "missing"); err != nil {
			t.Errorf("expected nil for a missing entry, got %v", err)
		}
	})

	t.Run("regular file is fine", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "regular"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := RefuseSymlinkOrNonRegularAt(fd, "regular"); err != nil {
			t.Errorf("expected nil for a regular file, got %v", err)
		}
	})

	t.Run("symlink is refused", func(t *testing.T) {
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := RefuseSymlinkOrNonRegularAt(fd, "link"); err == nil {
			t.Error("expected an error for a symlink, got nil")
		}
	})

	t.Run("directory is refused", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := RefuseSymlinkOrNonRegularAt(fd, "subdir"); err == nil {
			t.Error("expected an error for a directory, got nil")
		}
	})

	t.Run("fifo is refused without blocking", func(t *testing.T) {
		fifoPath := filepath.Join(dir, "fifo")
		if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- RefuseSymlinkOrNonRegularAt(fd, "fifo") }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("expected an error for a fifo, got nil")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("RefuseSymlinkOrNonRegularAt blocked on a FIFO with no writer")
		}
	})
}
