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

// TestFixupRootfsForScion_NoopWhenAlreadyCorrect proves the doc comment's
// "idempotent and quiet: when neither condition needs fixing, this makes no
// changes" claim end to end, using the real (non-faked) fileOwnerUID and
// lchownFn: a t.TempDir()'s entries are owned by the test's own uid, not
// root, so nothing here should be touched at all. Combined with
// TestFixupRootfsForScion_ChownsRootOwnedHomeEntriesThenIsIdempotent above
// (which proves "logs nothing" by inspection: fixupRootfsForScion's only
// log.Info call is gated on rootChanged || homeChanged > 0, and that test
// already proves both are false/0 on a no-op pass), this covers the case
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
