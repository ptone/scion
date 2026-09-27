/*
Copyright 2026 The Scion Authors.
*/

package dirfd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestChainIsTrusted exercises the pure ownership/mode predicate directly,
// with fabricated uid/mode values — no real filesystem involved. This is
// the only way to cover "owned by neither root nor self" and "owned by root
// but group/other-writable" deterministically: a real component that fails
// the ownership half would require actually being a second, unrelated uid,
// which an unprivileged test process cannot fabricate on a real file.
func TestChainIsTrusted(t *testing.T) {
	const self = 1000
	tests := []struct {
		name    string
		uid     uint32
		mode    uint32
		selfUID uint32
		want    bool
	}{
		{"root owned, mode 0755", 0, 0o755, self, true},
		{"self owned, mode 0755", self, 0o755, self, true},
		{"self owned, mode 0700", self, 0o700, self, true},
		{"third party owned, mode 0755", 1234, 0o755, self, false},
		{"root owned, group writable", 0, 0o775, self, false},
		{"root owned, other writable", 0, 0o757, self, false},
		{"self owned, group writable", self, 0o775, self, false},
		{"self owned, other writable", self, 0o757, self, false},
		{"root owned, sticky and world writable (e.g. /tmp)", 0, 0o1777, self, false},
		{"root owned, sticky only, not writable", 0, 0o1755, self, true},
		{"third party owned, world writable", 1234, 0o777, self, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chainIsTrusted(tt.uid, tt.mode, tt.selfUID); got != tt.want {
				t.Errorf("chainIsTrusted(uid=%d, mode=%#o, self=%d) = %v, want %v", tt.uid, tt.mode, tt.selfUID, got, tt.want)
			}
		})
	}
}

// selfOwnedTrustedDir creates a fresh, self-owned, non-group/other-writable
// directory to anchor a chain-verification test's "happy path" component(s)
// under. It deliberately does NOT use t.TempDir() (which resolves under
// os.TempDir(), i.e. "/tmp" on every system this suite runs on — world-
// writable by design, so it fails the very check under test on its own,
// before the test's own fixture is ever reached). Anchoring instead under
// this process's real home directory means every ancestor from "/" down is
// either root-owned (e.g. "/", "/home") or owned by this process's own uid
// (the home directory itself) and not group/other-writable — exactly the
// condition OpenParentNoFollowRootOwned requires — without needing real
// root privilege to construct.
//
// Skips the test if $HOME can't be resolved or isn't usable for this. The
// created directory is removed via t.Cleanup.
func selfOwnedTrustedDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("cannot resolve a real home directory to anchor a root/self-owned chain under")
	}
	dir, err := os.MkdirTemp(home, ".dirfd-rootowned-test-*")
	if err != nil {
		t.Skipf("cannot create a test directory under %s: %v", home, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestOpenParentNoFollowRootOwned_RefusesWorldWritableAncestor proves the
// walk fails closed against a REAL ancestor that is merely writable by
// everyone else — not a symlink, not even wrong-owner, just too permissive
// — the exact shape of the original vulnerability (a workload-writable
// parent letting an entry be renamed/replaced out from under root).
func TestOpenParentNoFollowRootOwned_RefusesWorldWritableAncestor(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	writable := filepath.Join(base, "writable")
	if err := os.Mkdir(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	// os.Mkdir's mode argument is masked by the process umask; force the
	// exact bits this test needs regardless of what umask happens to be.
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatal(err)
	}

	_, _, err := OpenParentNoFollowRootOwned(filepath.Join(writable, "leaf"))
	if err == nil {
		t.Fatal("expected OpenParentNoFollowRootOwned to refuse a world-writable ancestor, got nil error")
	}
}

// TestOpenParentNoFollowRootOwned_RefusesGroupWritableAncestor is the
// group-write half of the same property.
func TestOpenParentNoFollowRootOwned_RefusesGroupWritableAncestor(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	writable := filepath.Join(base, "group-writable")
	if err := os.Mkdir(writable, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o775); err != nil {
		t.Fatal(err)
	}

	_, _, err := OpenParentNoFollowRootOwned(filepath.Join(writable, "leaf"))
	if err == nil {
		t.Fatal("expected OpenParentNoFollowRootOwned to refuse a group-writable ancestor, got nil error")
	}
}

// TestOpenParentNoFollowRootOwned_SucceedsOnTrustedChain proves the happy
// path: a real chain that is entirely root- or self-owned and free of
// group/other write succeeds, returning a usable parent fd.
func TestOpenParentNoFollowRootOwned_SucceedsOnTrustedChain(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	nested := filepath.Join(base, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	fd, leaf, err := OpenParentNoFollowRootOwned(filepath.Join(nested, "leaf"))
	if err != nil {
		t.Fatalf("OpenParentNoFollowRootOwned: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if leaf != "leaf" {
		t.Errorf("leaf = %q, want %q", leaf, "leaf")
	}
}

// TestEnsureDirNoFollowRootOwned_CreatesAndVerifiesLeaf proves
// EnsureDirNoFollowRootOwned creates a missing leaf under a trusted chain,
// at the requested mode, and that a second call against the now-existing
// leaf succeeds idempotently (Mkdirat's EEXIST is tolerated).
func TestEnsureDirNoFollowRootOwned_CreatesAndVerifiesLeaf(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	target := filepath.Join(base, "private")

	f, err := EnsureDirNoFollowRootOwned(target, 0o700)
	if err != nil {
		t.Fatalf("EnsureDirNoFollowRootOwned (create): %v", err)
	}
	_ = f.Close()

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("target is not a directory: %v", fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %#o, want 0700", perm)
	}

	f2, err := EnsureDirNoFollowRootOwned(target, 0o700)
	if err != nil {
		t.Fatalf("EnsureDirNoFollowRootOwned (idempotent second call): %v", err)
	}
	_ = f2.Close()
}

// TestEnsureDirNoFollowRootOwned_RefusesSymlinkedLeaf proves a pre-existing
// symlink at the leaf name is refused outright, never followed and never
// silently replaced.
func TestEnsureDirNoFollowRootOwned_RefusesSymlinkedLeaf(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	victim := filepath.Join(base, "victim")
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "leaf")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureDirNoFollowRootOwned(target, 0o700); err == nil {
		t.Fatal("expected EnsureDirNoFollowRootOwned to refuse a symlinked leaf, got nil error")
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("leaf is no longer a symlink — it must be refused, not replaced")
	}
}

// TestEnsureDirNoFollowRootOwned_RefusesPreExistingBadModeLeaf proves the
// leaf's own fstat-and-check — not just the parent chain — is what actually
// runs: a leaf that already exists, under an entirely trusted chain, but is
// itself group/other-writable, must still be refused rather than handed
// back as a usable directory.
func TestEnsureDirNoFollowRootOwned_RefusesPreExistingBadModeLeaf(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	target := filepath.Join(base, "leaf")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Chmod after creation, not via Mkdir's own (umask-masked) mode
	// argument, to force the exact world-writable bits this test needs and
	// to exercise the re-check against an already-existing directory
	// (the create path's own Mkdirat call gets EEXIST here, tolerated —
	// this test is about what happens after that).
	if err := os.Chmod(target, 0o777); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureDirNoFollowRootOwned(target, 0o700); err == nil {
		t.Fatal("expected EnsureDirNoFollowRootOwned to refuse an existing, group/other-writable leaf, got nil error")
	}
}

// TestOpenParentNoFollowRootOwned_RefusesSymlinkedAncestor proves an
// ancestor component that is a symlink — even one that points at an
// otherwise entirely trusted, self-owned directory — is refused by the
// O_NOFOLLOW open at that component, never silently followed into the
// directory it happens to point at.
func TestOpenParentNoFollowRootOwned_RefusesSymlinkedAncestor(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	_, _, err := OpenParentNoFollowRootOwned(filepath.Join(link, "leaf"))
	if err == nil {
		t.Fatal("expected OpenParentNoFollowRootOwned to refuse a symlinked ancestor even when its target is trusted, got nil error")
	}
}

// TestOpenNoFollowRootOwnedFile_RejectsSymlinkChainThroughUntrustedDir
// proves OpenNoFollowRootOwnedFile — which pkg/sciontool/rootexec calls only
// after already resolving a candidate's own symlinks with
// filepath.EvalSymlinks, per its own doc comment — still refuses a
// destination reached through a directory that is not root- or self-owned
// and free of group/other write. This is the property rootexec.Resolve
// actually depends on: following a legitimate root-installed symlink chain
// (e.g. Debian's iptables via /etc/alternatives) must not become a way to
// smuggle a workload-writable directory into the trusted result merely
// because a symlink pointed through it first.
func TestOpenNoFollowRootOwnedFile_RejectsSymlinkChainThroughUntrustedDir(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	untrusted := filepath.Join(base, "untrusted")
	if err := os.Mkdir(untrusted, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(untrusted, 0o777); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(untrusted, "binary")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho planted\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A caller resolves the full symlink chain (filepath.EvalSymlinks)
	// before calling this function; simulate that by passing the fully
	// resolved real path directly, exactly as this function's own doc
	// comment requires.
	if _, err := OpenNoFollowRootOwnedFile(target); err == nil {
		t.Fatal("expected OpenNoFollowRootOwnedFile to refuse a file reached through a group/other-writable directory, got nil error")
	}
}

// TestOpenNoFollowRootOwnedFile_AcceptsTrustedRegularFile is the happy path:
// a regular, executable file under an entirely trusted chain is accepted
// and returned open.
func TestOpenNoFollowRootOwnedFile_AcceptsTrustedRegularFile(t *testing.T) {
	base := selfOwnedTrustedDir(t)
	target := filepath.Join(base, "binary")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	f, err := OpenNoFollowRootOwnedFile(target)
	if err != nil {
		t.Fatalf("OpenNoFollowRootOwnedFile: %v", err)
	}
	_ = f.Close()
}

// TestEnsureDirNoFollowRootOwned_RootOwnedChain is the genuine root-only
// happy path: with a real root-owned, non-writable ancestor chain (this
// process's own euid IS 0 in that case, so chainIsTrusted's uid==0 branch is
// what's actually exercised, not the uid==self escape valve every other
// test in this file relies on). Skips unless running as root.
func TestEnsureDirNoFollowRootOwned_RootOwnedChain(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create and verify a genuinely root-owned (uid 0) chain")
	}
	base, err := os.MkdirTemp("/root", "dirfd-rootowned-test-*")
	if err != nil {
		t.Skipf("could not create a fixture under /root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(base, "private")
	f, err := EnsureDirNoFollowRootOwned(target, 0o700)
	if err != nil {
		t.Fatalf("EnsureDirNoFollowRootOwned: %v", err)
	}
	defer func() { _ = f.Close() }()
}
