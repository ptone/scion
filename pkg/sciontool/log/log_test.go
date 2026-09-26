/*
Copyright 2026 The Scion Authors.
*/

package log

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenLogFileNoFollow_NormalWrite proves the hardened open still behaves
// exactly like the historical os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY,
// mode) call for the ordinary case: same resulting mode (compared against a
// control file created the historical way, rather than against the literal
// mode argument, since umask affects both identically) and content actually
// written.
func TestOpenLogFileNoFollow_NormalWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	controlPath := filepath.Join(dir, "control.log")

	f, err := openLogFileNoFollow(path, 0666)
	if err != nil {
		t.Fatalf("openLogFileNoFollow: %v", err)
	}
	if _, err := f.WriteString("hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	control, err := os.OpenFile(controlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		t.Fatalf("control OpenFile: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatalf("control close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	controlInfo, err := os.Stat(controlPath)
	if err != nil {
		t.Fatalf("control stat: %v", err)
	}
	if got, want := info.Mode().Perm(), controlInfo.Mode().Perm(); got != want {
		t.Errorf("mode = %o, want %o (matching the historical open call)", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello\n" {
		t.Errorf("content = %q, want %q", data, "hello\n")
	}
}

// TestOpenLogFileNoFollow_RefusesSymlink proves that a workload that has
// replaced the log path (e.g. $HOME/agent.log, which the scion user owns)
// with a symlink can't make root append to or create whatever it points
// at: openLogFileNoFollow must refuse, and the symlink's target must be
// left untouched.
func TestOpenLogFileNoFollow_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	if err := os.WriteFile(target, []byte("do-not-touch"), 0600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	path := filepath.Join(dir, "agent.log")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := openLogFileNoFollow(path, 0666); err == nil {
		t.Fatal("expected an error opening a symlinked log path, got nil")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "do-not-touch" {
		t.Errorf("symlink target was modified: %q", data)
	}
}

// TestOpenLogFileNoFollow_RefusesDirectory proves that a directory at the
// log path is refused via the explicit regular-file check, rather than
// relying only on the open(2) EISDIR a O_WRONLY open against a directory
// already happens to return.
func TestOpenLogFileNoFollow_RefusesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := openLogFileNoFollow(path, 0666); err == nil {
		t.Fatal("expected an error opening a directory as a log path, got nil")
	}
}
