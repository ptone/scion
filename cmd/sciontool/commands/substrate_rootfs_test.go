/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestCanSearchDir_OwnerFirst pins the kernel's own permission-check order
// that canSearchDir's doc comment claims: once a directory's owning uid
// matches the target uid, only the owner bit decides traversability —
// never falling through to the group or other bits, no matter how
// permissive they are. Every other canSearchDir-exercising test in this
// package uses directories the target uid doesn't own, so a mutation
// replacing the owner branch with the "other" bit check would survive
// them all; this is the one that pins the owner-first rule directly.
func TestCanSearchDir_OwnerFirst(t *testing.T) {
	const uid, gid = 1000, 1000
	cases := []struct {
		name string
		mode fs.FileMode
		want bool
	}{
		// Owner has no bits at all, but group (which also matches gid) and
		// other both have full rwx. The owner branch must still win.
		{"owner empty, group+other rwx (0o070)", fs.ModeDir | 0o070, false},
		{"owner empty, group+other rwx (0o077)", fs.ModeDir | 0o077, false},
		// Owner has read+write but not execute; other has execute. The
		// owner branch must still win (no execute bit there means false),
		// not fall through to other's execute bit.
		{"owner rw only, other x (0o601)", fs.ModeDir | 0o601, false},
		// Control: owner does have the execute bit.
		{"owner has x (0o100)", fs.ModeDir | 0o100, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := fakeFileInfo{mode: c.mode, uid: uid, gid: gid}
			if got := canSearchDir(info, uid, gid); got != c.want {
				t.Errorf("canSearchDir(mode=%v, owned by the target uid/gid) = %v, want %v", c.mode, got, c.want)
			}
		})
	}
}

// TestFixupRootfsForScion_WidensRestrictiveRoot proves condition (i) from
// fixupRootfsForScion's doc comment: a rootfs whose '/' comes up too
// restrictive to traverse (e.g. 0700, from bundle_linux.go's MkdirAll) gets
// widened to 0755. Removing this chmod entirely is one of the mutations this
// test exists to catch.
func TestFixupRootfsForScion_WidensRestrictiveRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	fixupRootfsForScion(root, home, 1000, 1000)

	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("root mode = %o, want 0755", got)
	}
}

// TestFixupRootfsForScion_LeavesAlreadyWideRootAlone proves the "only ever
// widen" half of the chmod's own doc comment, and doubles as the "remove the
// idempotency guard" mutation check for the root chmod specifically: if the
// perm&0o755 != 0o755 guard were deleted so the chmod ran unconditionally, a
// pre-existing 0777 would be narrowed to 0755 here, and this test would
// catch that.
func TestFixupRootfsForScion_LeavesAlreadyWideRootAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	fixupRootfsForScion(root, home, 1000, 1000)

	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Errorf("root mode = %o, want unchanged 0777", got)
	}
}

// TestFixupRootfsForScion_ChownsRootOwnedHomeEntriesThenIsIdempotent proves
// condition (ii): root-owned entries under home get chowned to the target
// uid/gid via chownTreeRootOwned, and a second pass — once a real chown(2)
// has actually taken effect — makes no further changes.
//
// This drives chownTreeRootOwned's own injectable fileOwnerUID/lchownFn
// (init.go) rather than real root-owned files, since this test process has
// neither. fileOwnerUID's fake reports every entry as root-owned (uid 0)
// until simulateFixed flips — standing in for what a real chown(2) would
// leave behind on disk, since chownTreeRootOwned itself only receives an
// fs.FileInfo per entry, not a mutable path-keyed store, and a real chown
// isn't available to this test.
//
// This is also the "remove the idempotency guard" mutation check for
// chownTreeRootOwned's own ownerUID != 0 skip: if that guard were deleted,
// the second pass below would still call lchownFn on every entry (since the
// fake's return value no longer gates anything), and the "no calls on the
// second pass" assertion would fail.
func TestFixupRootfsForScion_ChownsRootOwnedHomeEntriesThenIsIdempotent(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sub", "b"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		// Already correct: isolates this test to the home/chown behaviour,
		// independent of the root-chmod tests above.
		t.Fatal(err)
	}

	origOwner, origChown := fileOwnerUID, lchownFn
	t.Cleanup(func() { fileOwnerUID, lchownFn = origOwner, origChown })

	var simulateFixed bool
	var chownCalls []string
	fileOwnerUID = func(fs.FileInfo) (uint32, bool) {
		if simulateFixed {
			return 1000, true
		}
		return 0, true
	}
	lchownFn = func(path string, uid, gid int) error {
		chownCalls = append(chownCalls, path)
		return nil
	}

	fixupRootfsForScion(root, home, 1000, 1000)
	if len(chownCalls) == 0 {
		t.Fatal("first call: expected root-owned home entries to be chowned, got none")
	}

	simulateFixed = true
	chownCalls = nil
	fixupRootfsForScion(root, home, 1000, 1000)
	if len(chownCalls) != 0 {
		t.Errorf("second call: got %d chown calls, want 0 — a real chown(2) would already have fixed ownership by now", len(chownCalls))
	}
}

// TestFixupWorldWritableTmpDirSticky_SetsStickyOnWorldWritable proves
// condition (iii) from fixupRootfsForScion's doc comment: a world-writable
// directory missing the sticky bit (the observed Substrate /tmp state) gets
// set to 01777.
func TestFixupWorldWritableTmpDirSticky_SetsStickyOnWorldWritable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	if changed := fixupWorldWritableTmpDirSticky(dir); !changed {
		t.Fatal("expected fixupWorldWritableTmpDirSticky to report a change")
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 {
		t.Errorf("mode = %v, want the sticky bit set", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Errorf("perm = %o, want 0777", got)
	}
}

// TestFixupWorldWritableTmpDirSticky_LeavesAlreadyStickyAlone is the "only
// ever fix the missing-sticky case" idempotency check: a directory that
// already has the sticky bit (e.g. a real 1777 /tmp) is left completely
// alone, and fixupWorldWritableTmpDirSticky reports no change.
func TestFixupWorldWritableTmpDirSticky_LeavesAlreadyStickyAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}

	if changed := fixupWorldWritableTmpDirSticky(dir); changed {
		t.Error("expected no change for an already-sticky directory")
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 {
		t.Error("sticky bit should still be set")
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Errorf("perm = %o, want unchanged 0777", got)
	}
}

// TestFixupWorldWritableTmpDirSticky_LeavesNonWorldWritableAlone proves the
// fixup never widens or narrows a directory that isn't already
// world-writable — this is a defence-in-depth fixup for the observed
// 0777-no-sticky case specifically, not a general "make /tmp public" step.
func TestFixupWorldWritableTmpDirSticky_LeavesNonWorldWritableAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if changed := fixupWorldWritableTmpDirSticky(dir); changed {
		t.Error("expected no change for a non-world-writable directory")
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("perm = %o, want unchanged 0755", got)
	}
}

// TestFixupWorldWritableTmpDirSticky_MissingDirIsNoop proves a rootfs
// without the directory at all (every current test double, and any real
// rootfs image that lacks a /var/tmp) is handled as "nothing to fix", not
// an error.
func TestFixupWorldWritableTmpDirSticky_MissingDirIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	if changed := fixupWorldWritableTmpDirSticky(dir); changed {
		t.Error("expected no change for a missing directory")
	}
}

// TestFixupWorldWritableTmpDirSticky_RefusesSymlink proves that a symlink
// planted at the tmp-dir path (instead of a real directory) is refused via
// O_NOFOLLOW rather than followed to whatever it points at.
func TestFixupWorldWritableTmpDirSticky_RefusesSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target-dir")
	if err := os.Mkdir(target, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "tmp")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if changed := fixupWorldWritableTmpDirSticky(link); changed {
		t.Error("expected no change when the tmp-dir path is a symlink")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky != 0 {
		t.Error("symlink target must be untouched, but the sticky bit was set")
	}
}

// TestFixupRootfsForScion_FixesTmpAndVarTmpStickyBit proves
// fixupRootfsForScion itself drives the /tmp and /var/tmp fixup (not just
// the standalone helper), deriving both paths from its root parameter, and
// that the combined info log line's gate includes them (the "only when
// something changed" idempotency guard from the two-condition version of
// this test above).
func TestFixupRootfsForScion_FixesTmpAndVarTmpStickyBit(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	// Mkdir's mode argument is subject to umask, so chmod explicitly
	// afterwards to get the exact 0777-no-sticky starting condition this
	// test needs, the same way the root-chmod tests above do.
	tmpDir := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmpDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmpDir, 0o777); err != nil {
		t.Fatal(err)
	}
	varTmpDir := filepath.Join(root, "var", "tmp")
	if err := os.MkdirAll(varTmpDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(varTmpDir, 0o777); err != nil {
		t.Fatal(err)
	}

	fixupRootfsForScion(root, home, 1000, 1000)

	for _, dir := range []string{tmpDir, varTmpDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSticky == 0 {
			t.Errorf("%s: mode = %v, want the sticky bit set", dir, info.Mode())
		}
		if got := info.Mode().Perm(); got != 0o777 {
			t.Errorf("%s: perm = %o, want 0777", dir, got)
		}
	}

	// Idempotent: a second call makes no further changes and doesn't error.
	fixupRootfsForScion(root, home, 1000, 1000)
	for _, dir := range []string{tmpDir, varTmpDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o777 {
			t.Errorf("%s: perm = %o, want unchanged 0777 after the second call", dir, got)
		}
	}
}

// TestFixupRootfsForScion_NoopWhenAlreadyCorrect proves the doc comment's
// "idempotent: when neither condition needs fixing, this makes no changes"
// claim end to end, using the real (non-faked) fileOwnerUID and lchownFn: a
// t.TempDir()'s entries are owned by the test's own uid, not root, so
// nothing here should be touched at all. Combined with
// TestFixupRootfsForScion_ChownsRootOwnedHomeEntriesThenIsIdempotent above
// (which proves the info-on-change log line stays silent by inspection:
// fixupRootfsForScion's only log.Info call is gated on
// rootChanged || homeChanged > 0, and that test already proves both are
// false/0 on a no-op pass — the unconditional log.Debug line logs on every
// call, on purpose, and isn't part of this claim), this covers the case
// where they start out false/0 rather than becoming so after a first fix.
func TestFixupRootfsForScion_NoopWhenAlreadyCorrect(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	beforeUID, ok := fileOwnerUID(statFile(t, filepath.Join(home, "a")))
	if !ok {
		t.Fatal("could not read the test file's owning uid")
	}

	fixupRootfsForScion(root, home, 1000, 1000)

	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o755 {
		t.Errorf("root mode = %o, want unchanged 0755", got)
	}
	afterUID, ok := fileOwnerUID(statFile(t, filepath.Join(home, "a")))
	if !ok {
		t.Fatal("could not read the test file's owning uid")
	}
	if afterUID != beforeUID {
		t.Errorf("home entry's owning uid changed from %d to %d, want left alone (it wasn't root-owned)", beforeUID, afterUID)
	}
}

func statFile(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
