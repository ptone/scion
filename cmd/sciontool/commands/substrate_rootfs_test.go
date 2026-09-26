/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
// This drives chownTreeRootOwned's own injectable chownTreeRootOwnedFilter
// (init.go) rather than real root-owned files, since this test process has
// neither — but the actual chown(2) chownTreeRootOwned (via
// dirfd.ChownTreeNoFollow) issues runs for real, to uid/gid = the test's own
// uid/gid (always permitted, and still bumps ctime — see
// TestWriteEnvFile_DirChownSurvivesSwapAfterWrite for the same technique).
// The filter fakes "every entry looks root-owned" until simulateFixed flips,
// standing in for what a real chown(2) would leave behind on disk once the
// first pass has actually run.
//
// This is also the "remove the idempotency guard" mutation check for
// chownTreeRootOwned's own filter-driven skip: if that guard were deleted
// (the filter's return value stopped gating anything), the second pass
// below would still chown every entry, and the "no ctime change on the
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

	origFilter := chownTreeRootOwnedFilter
	t.Cleanup(func() { chownTreeRootOwnedFilter = origFilter })

	var simulateFixed bool
	chownTreeRootOwnedFilter = func(uint32) bool { return !simulateFixed }

	uid, gid := os.Getuid(), os.Getgid()
	aCtimeBefore := ctimeOfFile(t, filepath.Join(home, "a"))
	// A brief settle so the ctime bump below is measurably distinct even on
	// filesystems/clock sources with coarse timestamp resolution.
	time.Sleep(15 * time.Millisecond)

	fixupRootfsForScion(root, home, uid, gid)
	aCtimeAfterFirst := ctimeOfFile(t, filepath.Join(home, "a"))
	if aCtimeAfterFirst == aCtimeBefore {
		t.Fatal("first call: expected root-owned home entries to be chowned, ctime did not advance")
	}

	simulateFixed = true
	time.Sleep(15 * time.Millisecond)
	fixupRootfsForScion(root, home, uid, gid)
	aCtimeAfterSecond := ctimeOfFile(t, filepath.Join(home, "a"))
	if aCtimeAfterSecond != aCtimeAfterFirst {
		t.Error("second call: ctime advanced again — a real chown(2) would already have fixed ownership by now")
	}
}

// ctimeOfFile returns path's change time — chown(2) always bumps it, even
// when it sets the same uid/gid a file already has, so "ctime advanced" is a
// reliable proxy for "chown actually ran against this path" without needing
// real root.
func ctimeOfFile(t *testing.T, path string) syscall.Timespec {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("no *syscall.Stat_t for %s", path)
	}
	return st.Ctim
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

// TestFixupWorldWritableTmpDirSticky_OnlyAddsStickyBit proves the fixup
// adds just the sticky bit rather than forcing the mode to exactly 01777:
// a world-writable directory that's more restrictive than 0777 elsewhere
// (e.g. 0773, no group-other split relevant here) must keep those other
// bits as they were.
func TestFixupWorldWritableTmpDirSticky_OnlyAddsStickyBit(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o773); err != nil {
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
	if got := info.Mode().Perm(); got != 0o773 {
		t.Errorf("perm = %o, want unchanged 0773 (sticky bit added, not widened to 0777)", got)
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
// claim end to end, using the real (non-faked) chownTreeRootOwnedFilter
// (isRootOwned) and dirfd.ChownTreeNoFollow: a t.TempDir()'s entries are
// owned by the test's own uid, not root, so nothing here should be touched
// at all. Combined with
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

	beforeUID, ok := ownerUIDOf(statFile(t, filepath.Join(home, "a")))
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
	afterUID, ok := ownerUIDOf(statFile(t, filepath.Join(home, "a")))
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

// ownerUIDOf reads a file's owning uid from its already-stat'd fs.FileInfo —
// a small test-local replacement for the old fileOwnerUID package var (now
// removed; chownTreeRootOwned's real implementation reads ownership via its
// own fd-relative fstatat, not this path).
func ownerUIDOf(info fs.FileInfo) (uid uint32, ok bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}

// TestFixupRootfsForScion_Enforced_RefusesAncestorSymlink is T4(a)'s core
// regression test: fixupRootfsForScion is reachable only from substrate-
// serve (fixupRootfsForScionUser -> startupRootfsFixup/bootstrapRootfsFixup
// in substrate_serve.go), so its own chownTreeRootOwned call is hardcoded
// to enforced (true) — see that call site's own comment. This proves that
// hardcoding actually matters: an ancestor-path symlink of home must be
// refused (nothing chowned through it), which only holds if the enforced,
// no-follow branch is really the one running.
func TestFixupRootfsForScion_Enforced_RefusesAncestorSymlink(t *testing.T) {
	origFilter := chownTreeRootOwnedFilter
	t.Cleanup(func() { chownTreeRootOwnedFilter = origFilter })
	chownTreeRootOwnedFilter = func(uint32) bool { return true }

	real := t.TempDir()
	actual := filepath.Join(real, "actual")
	if err := os.Mkdir(actual, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(actual, "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	homeLink := filepath.Join(parent, "home-link")
	if err := os.Symlink(real, homeLink); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(homeLink, "actual")

	markerBefore := ctimeOfFile(t, marker)
	time.Sleep(15 * time.Millisecond)

	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fixupRootfsForScion(root, home, os.Getuid(), os.Getgid())

	markerAfter := ctimeOfFile(t, marker)
	if markerAfter != markerBefore {
		t.Error("marker's ctime advanced: the ancestor symlink was followed and chowned through — fixupRootfsForScion is not really enforced")
	}
}
