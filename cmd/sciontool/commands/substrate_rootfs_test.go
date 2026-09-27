/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
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

// plantSetuidSudo creates a regular file at root/dir/sudo with the setuid,
// setgid, and sticky bits all set, standing in for an image that shipped a
// setuid-root sudo binary. os.Chmod's own special-bit handling is used
// directly (unlike a plain executable's permission bits, Go's os.Chmod does
// apply os.ModeSetuid/ModeSetgid/ModeSticky correctly), so no umask
// workaround is needed the way the plain-permission tests above need one.
func plantSetuidSudo(t *testing.T, root, dir string) string {
	t.Helper()
	fullDir := filepath.Join(root, dir)
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fullDir, "sudo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, os.ModeSetuid|os.ModeSetgid|os.ModeSticky|0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStripSudoSetuidBits_StripsSetuidSetgidStickyFromEveryCandidateDir
// proves stripSudoSetuidBits reaches every one of sudoCheckDirs, strips all
// three special bits (not just setuid), and leaves the file's other
// permission bits and content untouched.
func TestStripSudoSetuidBits_StripsSetuidSetgidStickyFromEveryCandidateDir(t *testing.T) {
	root := t.TempDir()
	var paths []string
	for _, dir := range sudoCheckDirs {
		paths = append(paths, plantSetuidSudo(t, root, dir))
	}

	if !stripSudoSetuidBits(root) {
		t.Fatal("stripSudoSetuidBits() = false, want true (planted setuid binaries)")
	}

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Errorf("%s: mode = %v, want setuid/setgid/sticky all cleared", path, info.Mode())
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("%s: perm = %o, want unchanged 0755", path, info.Mode().Perm())
		}
	}

	// Idempotent: a second call finds nothing left to strip.
	if stripSudoSetuidBits(root) {
		t.Error("stripSudoSetuidBits() second call = true, want false (nothing left to strip)")
	}
}

// TestStripSudoSetuidBits_RefusesSymlinkedLeaf proves a symlink planted at
// the "sudo" name itself is refused (O_NOFOLLOW on the leaf), not followed
// and chmod'd through to whatever it points at.
func TestStripSudoSetuidBits_RefusesSymlinkedLeaf(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "usr", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("victim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, os.ModeSetuid|0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "sudo")); err != nil {
		t.Fatal(err)
	}

	stripSudoSetuidBits(root)

	info, err := os.Lstat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetuid == 0 {
		t.Error("victim's setuid bit was cleared through a symlink; the leaf open must refuse to follow it")
	}
}

// TestRemoveSudoersGrants_RemovesBothNamesLeavesOthersAlone proves both
// grant files are removed, an unrelated file in the same directory survives,
// and a missing directory or missing individual grant is a benign no-op.
func TestRemoveSudoersGrants_RemovesBothNamesLeavesOthersAlone(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "sudoers.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range sudoersGrantNames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("scion ALL=(ALL) NOPASSWD:ALL\n"), 0o440); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(dir, "README")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0o440); err != nil {
		t.Fatal(err)
	}

	if !removeSudoersGrants(root) {
		t.Fatal("removeSudoersGrants() = false, want true (planted grants)")
	}
	for _, name := range sudoersGrantNames {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still exists after removeSudoersGrants, err=%v", name, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated file %s was removed or is inaccessible: %v", unrelated, err)
	}

	// Second call: nothing left to remove, no error, benign false.
	if removeSudoersGrants(root) {
		t.Error("removeSudoersGrants() second call = true, want false (nothing left)")
	}
	// A root with no /etc/sudoers.d at all is equally benign.
	if removeSudoersGrants(t.TempDir()) {
		t.Error("removeSudoersGrants() on a root with no sudoers.d = true, want false")
	}
}

// TestFixupRootfsForScion_StripsSudoSetuidAndRemovesSudoersGrantsOnEveryCall
// proves the sudo hardening runs as part of fixupRootfsForScion itself —
// the one function both of substrate-serve's rootfs-fixup call sites
// invoke (startup and the /bootstrap fallback) — and that it re-applies on
// every call rather than a one-shot, golden-boot-only action: the setuid
// bit and the sudoers grant are re-planted between two calls, standing in
// for a persisted rootfs that reverted them between bootstraps, and both
// calls strip/remove them again.
func TestFixupRootfsForScion_StripsSudoSetuidAndRemovesSudoersGrantsOnEveryCall(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	sudoersDir := filepath.Join(root, "etc", "sudoers.d")

	for i := 0; i < 2; i++ {
		sudoPath := plantSetuidSudo(t, root, "usr/bin")
		if err := os.MkdirAll(sudoersDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sudoersDir, "scion"), []byte("scion ALL=(ALL) NOPASSWD:ALL\n"), 0o440); err != nil {
			t.Fatal(err)
		}

		fixupRootfsForScion(root, home, 1000, 1000)

		info, err := os.Stat(sudoPath)
		if err != nil {
			t.Fatalf("call %d: stat sudo binary: %v", i, err)
		}
		if info.Mode()&os.ModeSetuid != 0 {
			t.Errorf("call %d: sudo binary still setuid after fixupRootfsForScion", i)
		}
		if _, err := os.Stat(filepath.Join(sudoersDir, "scion")); !os.IsNotExist(err) {
			t.Errorf("call %d: sudoers grant still present after fixupRootfsForScion, err=%v", i, err)
		}
	}
}

// TestFixupRootfsForScionUser_RunsSudoFixup proves the sudo hardening is
// reachable through fixupRootfsForScionUser — the function both real
// call sites (substrate_serve.go's startupRootfsFixup and
// bootstrapRootfsFixup package vars) actually invoke — not just through a
// direct call to fixupRootfsForScion that a real bootstrap would never
// make.
func TestFixupRootfsForScionUser_RunsSudoFixup(t *testing.T) {
	origLookup := scionUserLookup
	t.Cleanup(func() { scionUserLookup = origLookup })
	root := t.TempDir()
	home := filepath.Join(root, "home", "scion")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	scionUserLookup = func(string) (*user.User, error) {
		return &user.User{Uid: "1000", Gid: "1000", HomeDir: home}, nil
	}

	sudoPath := plantSetuidSudo(t, root, "usr/bin")

	fixupRootfsForScionUser(root)

	info, err := os.Stat(sudoPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Error("sudo binary still setuid after fixupRootfsForScionUser")
	}
}

// TestSudoHardeningOnlyReachableFromSubstrateServe is the parity guard for
// the sudo hardening: every other runtime this codebase targets (Docker,
// Kubernetes, Apple containers, rootless) keeps sudo and its setuid bit
// intact on purpose — root is not a security boundary there. The strip/
// removal (fixupRootfsForScionUser, called at both of substrate-serve's own
// rootfs-fixup call sites) and the fail-closed precondition
// (checkPrivilegeDropFeasible, wired only into substrate-serve's own
// PrivilegeDropChecker) must never be reachable from any other file in this
// package, which — unlike substrate_serve.go — is where a non-substrate
// runtime's own init path lives. This scans every non-test source file
// (excluding the two files that legitimately define/call these directly)
// for a call to either, so a future call site added anywhere else in this
// package fails this test rather than silently changing non-substrate
// runtime behavior.
func TestSudoHardeningOnlyReachableFromSubstrateServe(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// substrate_rootfs.go defines fixupRootfsForScionUser; init.go defines
	// checkPrivilegeDropFeasible; substrate_serve.go is the only legitimate
	// caller of either.
	allowed := map[string]bool{
		"substrate_rootfs.go": true,
		"init.go":             true,
		"substrate_serve.go":  true,
	}
	forbidden := []string{"fixupRootfsForScionUser(", "checkPrivilegeDropFeasible("}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		for _, call := range forbidden {
			if strings.Contains(content, call) {
				t.Errorf("%s calls %s — this must only ever be reachable from substrate-serve's own wiring, never a non-substrate runtime's code path",
					name, strings.TrimSuffix(call, "("))
			}
		}
	}
}

// TestFixupRootfsForScion_Enforced_RefusesAncestorSymlink is the core
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
