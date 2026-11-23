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

package bundle

import (
	"archive/tar"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestT1_UnixModesAndSymlinks verifies owner-only modes (0600/0644/0755),
// setuid/setgid/sticky stripping, read-only directories, and internal relative
// symlinks survive round-trip intact.
func TestT1_UnixModesAndSymlinks(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	scratch := t.TempDir()

	files := map[string]os.FileMode{
		".ssh/id_ed25519": 0o600,
		"notes.txt":       0o644,
		"bin/run.sh":      0o755,
	}
	for rel, perm := range files {
		full := filepath.Join(srcDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("content:"+rel), perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, perm); err != nil {
			t.Fatal(err)
		}
	}

	// File with sticky bit set: sticky bit must be stripped on round-trip.
	stickyPath := filepath.Join(srcDir, "sticky.sh")
	if err := os.WriteFile(stickyPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stickyPath, 0o755|os.ModeSticky); err != nil {
		t.Fatal(err)
	}

	// Read-only directory containing a file (exercises deferred dir chmod).
	roDir := filepath.Join(srcDir, "rodir")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roDir, "inside.txt"), []byte("ok"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	// Internal relative symlinks: sibling and parent-staying-inside-root.
	if err := os.Symlink("notes.txt", filepath.Join(srcDir, "notes-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../notes.txt", filepath.Join(srcDir, "bin", "up-link")); err != nil {
		t.Fatal(err)
	}

	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srcRoot.Close() }()
	dstRoot, err := os.OpenRoot(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dstRoot.Close() }()

	store := newMemStore()
	lim := DefaultWorkspaceLimits()
	rm, err := Write(ctx, srcRoot, scratch, nil, lim, store)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Extract(ctx, dstRoot, scratch, rm, lim, store); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dstDir, "rodir"), 0o755) })

	for rel, wantPerm := range files {
		fi, err := os.Lstat(filepath.Join(dstDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("lstat %s: %v", rel, err)
		}
		if got := fi.Mode().Perm(); got != wantPerm {
			t.Errorf("%s mode = %04o, want %04o", rel, got, wantPerm)
		}
	}

	fiSticky, err := os.Lstat(filepath.Join(dstDir, "sticky.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fiSticky.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Errorf("special mode bits not stripped: %v", fiSticky.Mode())
	}
	if fiSticky.Mode().Perm() != 0o755 {
		t.Errorf("sticky.sh perm = %04o, want 0755", fiSticky.Mode().Perm())
	}

	fiRO, err := os.Lstat(filepath.Join(dstDir, "rodir"))
	if err != nil {
		t.Fatal(err)
	}
	if fiRO.Mode().Perm() != 0o555 {
		t.Errorf("rodir perm = %04o, want 0555", fiRO.Mode().Perm())
	}

	for link, wantTarget := range map[string]string{
		"notes-link":  "notes.txt",
		"bin/up-link": "../notes.txt",
	} {
		got, err := os.Readlink(filepath.Join(dstDir, filepath.FromSlash(link)))
		if err != nil || got != wantTarget {
			t.Errorf("readlink %s = %q (err=%v), want %q", link, got, err, wantTarget)
		}
	}
}

// TestT2_UnixSkipCounts verifies writer skips and counts sockets, FIFOs (without
// blocking), and absolute/escaping symlinks.
func TestT2_UnixSkipCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srcDir := t.TempDir()
	scratch := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "regular.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	// FIFO (must not block!)
	if err := syscall.Mkfifo(filepath.Join(srcDir, "my.fifo"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	// Unix domain socket (use short path under /tmp if needed; t.TempDir is short on Linux).
	sockPath := filepath.Join(srcDir, "my.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Escaping symlinks (absolute + relative escaping root)
	if err := os.Symlink("/scion-staging/secret", filepath.Join(srcDir, "abs-secret")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(srcDir, "sub", "rel-escape")); err != nil {
		t.Fatal(err)
	}

	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srcRoot.Close() }()

	store := newMemStore()
	rm, err := Write(ctx, srcRoot, scratch, nil, DefaultWorkspaceLimits(), store)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if rm.Skipped.FIFOs != 1 {
		t.Errorf("Skipped.FIFOs = %d, want 1", rm.Skipped.FIFOs)
	}
	if rm.Skipped.Sockets != 1 {
		t.Errorf("Skipped.Sockets = %d, want 1", rm.Skipped.Sockets)
	}
	if rm.Skipped.EscapingSymlinks != 2 {
		t.Errorf("Skipped.EscapingSymlinks = %d, want 2", rm.Skipped.EscapingSymlinks)
	}
}

// TestT3_UnixSymlinkedParentRefused verifies that an archive containing an
// internal symlink followed by a child write through that symlink is refused.
func TestT3_UnixSymlinkedParentRefused(t *testing.T) {
	ctx := context.Background()
	rm, store := buildRawTarGz(t, func(tw *tar.Writer) {
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "realdir/", Mode: 0o755})
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "linkdir", Linkname: "realdir", Mode: 0o777})
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "linkdir/evil.txt", Size: 4, Mode: 0o644})
		_, _ = tw.Write([]byte("evil"))
	})

	dstDir := t.TempDir()
	dstRoot, err := os.OpenRoot(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dstRoot.Close() }()

	scratch := t.TempDir()
	err = Extract(ctx, dstRoot, scratch, rm, DefaultWorkspaceLimits(), store)
	if !errors.Is(err, ErrSymlinkParent) {
		t.Fatalf("got err = %v, want ErrSymlinkParent", err)
	}
	if _, statErr := os.Stat(filepath.Join(dstDir, "realdir", "evil.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("evil.txt was written through symlink parent!")
	}
	assertDirEmpty(t, scratch)
}
