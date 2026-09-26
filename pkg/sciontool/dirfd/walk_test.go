/*
Copyright 2026 The Scion Authors.
*/

package dirfd

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// ctimeSettle sleeps briefly so a subsequent chown(2) is guaranteed to bump
// ctime by a measurable amount. Some filesystems/clock sources this test
// suite runs on have coarse enough timestamp resolution that two syscalls
// issued back-to-back with no other work in between can otherwise land on
// the same recorded ctime even though a real ownership-changing chown ran.
func ctimeSettle() { time.Sleep(15 * time.Millisecond) }

// ctimeOf returns path's change time, as an opaque comparable value: chown(2)
// always bumps ctime even when it sets the same uid/gid a file already has
// (as this test's own uid/gid always are — an unprivileged process cannot
// chown to anyone else), so "ctime advanced" is a reliable proxy for "chown
// actually ran against this path" without needing real root.
func ctimeOf(t *testing.T, path string) syscall.Timespec {
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

func TestOpenDirNoFollow_MissingLeafIsNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := OpenDirNoFollow(filepath.Join(dir, "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected errors.Is(err, os.ErrNotExist), got %v", err)
	}
}

// TestOpenDirNoFollow_MissingIntermediateComponentIsNotExist proves the
// errors.Is-not-os.IsNotExist distinction called out in OpenDirNoFollow's
// doc comment: when the missing component is an intermediate directory (not
// path's own leaf), the error comes back wrapped via OpenParentNoFollow's
// fmt.Errorf, which os.IsNotExist does not see through but errors.Is does.
func TestOpenDirNoFollow_MissingIntermediateComponentIsNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := OpenDirNoFollow(filepath.Join(dir, "missing-parent", "leaf"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected errors.Is(err, os.ErrNotExist), got %v", err)
	}
	if os.IsNotExist(err) {
		t.Log("os.IsNotExist now also recognizes this — the doc comment's warning may be stale")
	}
}

func TestOpenDirNoFollow_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDirNoFollow(link); err == nil {
		t.Fatal("expected an error opening a symlinked leaf, got nil")
	}
}

// TestChownTreeNoFollow_ChownsMatchingEntriesOnly proves the basic walk +
// filter behaviour: every entry (root, subdirectories, files) is visited,
// shouldChown decides which ones actually get chowned, and the walked/
// changed counts reflect that.
func TestChownTreeNoFollow_ChownsMatchingEntriesOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "chown-me"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leave-me"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "chown-me"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := map[string]syscall.Timespec{}
	for _, p := range []string{"chown-me", "leave-me", "sub", "sub/chown-me"} {
		before[p] = ctimeOf(t, filepath.Join(root, p))
	}
	ctimeSettle()

	uid, gid := os.Getuid(), os.Getgid()
	walked, changed, err := ChownTreeNoFollow(root, uid, gid, func(uint32) bool {
		return true // stand-in for a real filter; exercised for real below.
	}, false, nil)
	if err != nil {
		t.Fatalf("ChownTreeNoFollow: %v", err)
	}
	// root + 4 entries.
	if walked != 5 {
		t.Errorf("walked = %d, want 5", walked)
	}
	if changed != 5 {
		t.Errorf("changed = %d, want 5", changed)
	}
	for _, p := range []string{"chown-me", "leave-me", "sub", "sub/chown-me"} {
		if ctimeOf(t, filepath.Join(root, p)) == before[p] {
			t.Errorf("%s: ctime did not advance, chown did not run", p)
		}
	}
}

// TestChownTreeNoFollow_FilterSkipsNonMatchingEntries proves shouldChown
// actually gates the chown, using ownership as the filter the way
// chownTreeRootOwned does (only uid==0 entries) — here inverted to "only
// entries NOT owned by the current uid" so the test's own files (which it
// owns) are provably left alone.
func TestChownTreeNoFollow_FilterSkipsNonMatchingEntries(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "file")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := ctimeOf(t, target)

	uid, gid := os.Getuid(), os.Getgid()
	walked, changed, err := ChownTreeNoFollow(root, uid, gid, func(entryUID uint32) bool {
		return entryUID != uint32(os.Getuid()) // never true for our own files
	}, false, nil)
	if err != nil {
		t.Fatalf("ChownTreeNoFollow: %v", err)
	}
	if walked != 2 {
		t.Errorf("walked = %d, want 2", walked)
	}
	if changed != 0 {
		t.Errorf("changed = %d, want 0 (filter should have skipped every entry)", changed)
	}
	if ctimeOf(t, target) != before {
		t.Error("file was chowned despite the filter returning false")
	}
}

// TestChownTreeNoFollow_SymlinkEntryNotFollowed proves a symlink INSIDE the
// tree pointing at a directory OUTSIDE the tree is never descended into —
// only the symlink itself is eligible for chowning (fchownat with
// AT_SYMLINK_NOFOLLOW chowns the link, never the target) — so the victim
// directory's own contents are completely untouched.
func TestChownTreeNoFollow_SymlinkEntryNotFollowed(t *testing.T) {
	root := t.TempDir()
	victim := t.TempDir()
	victimFile := filepath.Join(victim, "secret")
	if err := os.WriteFile(victimFile, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	victimBefore := ctimeOf(t, victimFile)
	victimDirBefore := ctimeOf(t, victim)

	link := filepath.Join(root, "escape")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	uid, gid := os.Getuid(), os.Getgid()
	if _, _, err := ChownTreeNoFollow(root, uid, gid, func(uint32) bool { return true }, false, nil); err != nil {
		t.Fatalf("ChownTreeNoFollow: %v", err)
	}

	if ctimeOf(t, victimFile) != victimBefore {
		t.Error("victim file inside the symlink target was touched")
	}
	if ctimeOf(t, victim) != victimDirBefore {
		t.Error("victim directory (the symlink's target) was chowned — the symlink was followed")
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected the entry to still be a symlink")
	}
}

// TestChownTreeNoFollow_SurvivesIntermediateDirSwapMidWalk is the class's
// core regression test: a scion-uid process observes the walk enter a real
// subdirectory it owns, then — before the walk finishes with that
// subdirectory — renames it away and plants a symlink to a victim directory
// in its place. The historical filepath.Walk+os.Lchown(path) implementation
// re-resolves the full path string on every Lchown call, so a deeper call
// would walk straight through the freshly planted symlink into the victim.
// The fd-based walk holds an open fd for the subdirectory from the moment it
// is entered, so nothing that happens to its *name* in the parent
// afterward can redirect any operation still in flight underneath it.
//
// The swap is injected via chownWalkTestHook, fired right after the walk
// opens the subdirectory's fd and before it processes that subdirectory's
// own entries — the same window the class's real, timing-dependent exploit
// needs, made deterministic exactly like writeEnvFileAfterWriteForTest
// does for the tmptoken unit's own race.
func TestChownTreeNoFollow_SurvivesIntermediateDirSwapMidWalk(t *testing.T) {
	root := t.TempDir()
	victim := t.TempDir()
	// The victim's file is deliberately named "inner", matching the real
	// subdirectory's own entry below: the historical bug reconstructs the
	// path string "root/cache/inner" and re-lstats/re-lchowns it, so it only
	// actually lands on the victim when the victim happens to have an entry
	// by the same name the walk was already expecting to find there.
	victimMarker := filepath.Join(victim, "inner")
	if err := os.WriteFile(victimMarker, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	victimMarkerBefore := ctimeOf(t, victimMarker)
	victimBefore := ctimeOf(t, victim)

	subdir := filepath.Join(root, "cache")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	innerFile := filepath.Join(subdir, "inner")
	if err := os.WriteFile(innerFile, []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
	}
	innerBefore := ctimeOf(t, innerFile)
	ctimeSettle()

	moved := filepath.Join(root, "cache.moved")
	var swapped bool
	chownWalkTestHook = func(name string) {
		if swapped || name != "cache" {
			return
		}
		swapped = true
		if err := os.Rename(subdir, moved); err != nil {
			t.Errorf("swap: rename: %v", err)
			return
		}
		if err := os.Symlink(victim, subdir); err != nil {
			t.Errorf("swap: symlink: %v", err)
		}
	}
	t.Cleanup(func() { chownWalkTestHook = nil })

	uid, gid := os.Getuid(), os.Getgid()
	if _, _, err := ChownTreeNoFollow(root, uid, gid, func(uint32) bool { return true }, false, nil); err != nil {
		t.Fatalf("ChownTreeNoFollow: %v", err)
	}
	if !swapped {
		t.Fatal("test hook never fired — test is not exercising the intended window")
	}

	// The victim must be completely untouched.
	if ctimeOf(t, victimMarker) != victimMarkerBefore {
		t.Error("victim marker file was chowned through the swapped-in symlink")
	}
	if ctimeOf(t, victim) != victimBefore {
		t.Error("victim directory was chowned through the swapped-in symlink")
	}

	// "cache" in root must still be the symlink the swap planted — the walk
	// must not have unlinked or replaced it.
	linkInfo, err := os.Lstat(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("expected root/cache to still be the symlink the swap planted")
	}

	// The real subdirectory, now sitting at its moved-away name, is the one
	// that should have kept being walked and chowned via the fd the walk
	// already held before the swap. root/cache is now the swapped-in
	// symlink, so the original file's current path is moved/inner.
	movedInnerFile := filepath.Join(moved, "inner")
	if ctimeOf(t, movedInnerFile) == innerBefore {
		t.Error("the original subdirectory's own content was not chowned — the walk lost its held fd across the swap")
	}
}

func TestRemoveContentsNoFollow_RemovesEverythingExceptKept(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "keep"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "nested", "f"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := OpenDirNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	removed, err := RemoveContentsNoFollow(dir, func(name string) bool { return name == "keep" }, nil)
	if err != nil {
		t.Fatalf("RemoveContentsNoFollow: %v", err)
	}
	if removed != 3 { // "a", "b", "sub" (nested contents don't count as top-level)
		t.Errorf("removed = %d, want 3", removed)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "keep" {
		t.Errorf("remaining entries = %v, want [keep]", names)
	}
}

func TestRemoveContentsNoFollow_RefusesSymlinkedSubdirEntry(t *testing.T) {
	root := t.TempDir()
	victim := t.TempDir()
	sentinel := filepath.Join(victim, "sentinel")
	if err := os.WriteFile(sentinel, []byte("do-not-delete"), 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	dir, err := OpenDirNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	// A symlink entry is not a directory (unix.S_IFDIR won't match its own
	// lstat), so it takes the non-directory unlinkat branch and is removed
	// as a link — but the victim it pointed at must survive.
	if _, err := RemoveContentsNoFollow(dir, nil, nil); err != nil {
		t.Fatalf("RemoveContentsNoFollow: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("victim sentinel file should survive: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("expected the symlink entry itself to be removed, lstat err = %v", err)
	}
}

// TestRemoveContentsNoFollow_SurvivesSubdirSwapMidWalk mirrors
// TestChownTreeNoFollow_SurvivesIntermediateDirSwapMidWalk for the delete
// walk N3 needs: a scion-uid process swaps a subdirectory already entered by
// the walk for a symlink to a victim directory before the walk finishes
// deleting that subdirectory's own contents. The victim must never be
// touched, and the walk must keep operating on the fd it already holds for
// the real (now moved-away) subdirectory.
func TestRemoveContentsNoFollow_SurvivesSubdirSwapMidWalk(t *testing.T) {
	root := t.TempDir()
	victim := t.TempDir()
	// Named "inner" to match the real subdirectory's own entry below — the
	// historical (pre-fd-based) bug this guards against re-joins
	// "gcloud-sub/inner" as a string and re-resolves it, so it only lands on
	// the victim when the victim happens to have a same-named entry.
	victimMarker := filepath.Join(victim, "inner")
	if err := os.WriteFile(victimMarker, []byte("do-not-delete"), 0o600); err != nil {
		t.Fatal(err)
	}

	subdir := filepath.Join(root, "gcloud-sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	innerFile := filepath.Join(subdir, "inner")
	if err := os.WriteFile(innerFile, []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(root, "gcloud-sub.moved")
	var swapped bool
	removeWalkTestHook = func(name string) {
		if swapped || name != "gcloud-sub" {
			return
		}
		swapped = true
		if err := os.Rename(subdir, moved); err != nil {
			t.Errorf("swap: rename: %v", err)
			return
		}
		if err := os.Symlink(victim, subdir); err != nil {
			t.Errorf("swap: symlink: %v", err)
		}
	}
	t.Cleanup(func() { removeWalkTestHook = nil })

	dir, err := OpenDirNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	if _, err := RemoveContentsNoFollow(dir, nil, nil); err != nil {
		t.Fatalf("RemoveContentsNoFollow: %v", err)
	}
	if !swapped {
		t.Fatal("test hook never fired — test is not exercising the intended window")
	}

	if _, err := os.Stat(victimMarker); err != nil {
		t.Fatalf("victim marker must survive: %v", err)
	}
	if entries, err := os.ReadDir(victim); err != nil || len(entries) != 1 {
		t.Errorf("victim directory contents changed: entries=%v err=%v", entries, err)
	}

	// root/gcloud-sub must still be the symlink the swap planted.
	linkInfo, err := os.Lstat(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("expected root/gcloud-sub to still be the symlink the swap planted")
	}

	// The real, moved-away subdirectory's own contents should have been
	// deleted via the fd the walk already held — proving it kept operating
	// on the original directory, not the swapped-in symlink. root/gcloud-sub
	// is now the swapped-in symlink, so the original file's current path is
	// moved/inner.
	movedInnerFile := filepath.Join(moved, "inner")
	if _, err := os.Stat(movedInnerFile); !os.IsNotExist(err) {
		t.Errorf("expected the original subdirectory's inner file to be removed, got err=%v", err)
	}
}

// TestChownTreeNoFollow_ReportsPerEntryChownFailureViaOnErr proves per-entry
// chown failures are surfaced through onErr rather than silently discarded.
// Chowning to uid 0 (root) as a non-root test process is guaranteed to fail
// with EPERM for both the root directory itself (surfaced via the returned
// err, unaffected by this change) and for a child entry (previously
// silently swallowed).
func TestChownTreeNoFollow_ReportsPerEntryChownFailureViaOnErr(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: chowning to uid 0 would trivially succeed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "child"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var onErrCalls []string
	_, _, err := ChownTreeNoFollow(root, 0, 0, func(uint32) bool { return true }, false, func(name string, e error) {
		onErrCalls = append(onErrCalls, name)
	})
	if err == nil {
		t.Fatal("expected chowning root to uid 0 as non-root to fail")
	}
	found := false
	for _, name := range onErrCalls {
		if name == "child" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected onErr to be called for \"child\", got calls: %v", onErrCalls)
	}
}

// TestChownTreeNoFollow_GuardHardlinksSkipsMultiplyLinkedRegularFile proves
// the hard-link guard: a workload can pre-plant a hard link to a file it
// does not own (hard-linking only needs write access to the directory the
// link is created in), so a regular file with more than one link is skipped
// rather than chowned when guardHardlinks is true, and reported via onErr.
func TestChownTreeNoFollow_GuardHardlinksSkipsMultiplyLinkedRegularFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "hardlink")
	if err := os.Link(target, link); err != nil {
		t.Fatal(err)
	}
	before := ctimeOf(t, target)
	ctimeSettle()

	reported := map[string]bool{}
	uid, gid := os.Getuid(), os.Getgid()
	_, changed, err := ChownTreeNoFollow(root, uid, gid, func(uint32) bool { return true }, true, func(name string, e error) {
		if e == ErrHardlinkedRegularFile {
			reported[name] = true
		}
	})
	if err != nil {
		t.Fatalf("ChownTreeNoFollow: %v", err)
	}
	if !reported["target"] || !reported["hardlink"] {
		t.Fatalf("expected onErr to report ErrHardlinkedRegularFile for both entries, got: %v", reported)
	}
	if changed != 1 {
		// root itself is still chowned normally (it isn't a regular file,
		// so the hard-link guard never applies to it) — only "target" and
		// "hardlink" (the two entries sharing the guarded inode) must be
		// skipped.
		t.Errorf("changed = %d, want 1 (root only — both hard-linked entries must be skipped)", changed)
	}
	if ctimeOf(t, target) != before {
		t.Error("target file's ctime advanced — it was chowned despite the hard-link guard")
	}
}

// TestChownTreeNoFollow_GuardHardlinksDisabledStillChownsMultiplyLinkedFile
// proves guardHardlinks=false preserves the historical (non-enforced)
// behaviour: a hard-linked regular file is chowned like any other entry.
func TestChownTreeNoFollow_GuardHardlinksDisabledStillChownsMultiplyLinkedFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "hardlink")
	if err := os.Link(target, link); err != nil {
		t.Fatal(err)
	}
	before := ctimeOf(t, target)
	ctimeSettle()

	uid, gid := os.Getuid(), os.Getgid()
	_, _, err := ChownTreeNoFollow(root, uid, gid, func(uint32) bool { return true }, false, nil)
	if err != nil {
		t.Fatalf("ChownTreeNoFollow: %v", err)
	}
	if ctimeOf(t, target) == before {
		t.Error("expected the hard-linked file to be chowned when guardHardlinks is false")
	}
}

// TestChownTreeNoFollow_DepthCapStopsDescendingAndReportsViaOnErr proves the
// depth cap: a directory at or beyond the cap is itself still visited and
// chowned, but the walk does not descend into it, and onErr is told why.
func TestChownTreeNoFollow_DepthCapStopsDescendingAndReportsViaOnErr(t *testing.T) {
	origDepth := maxWalkDepth
	maxWalkDepth = 2
	t.Cleanup(func() { maxWalkDepth = origDepth })

	root := t.TempDir()
	level1 := filepath.Join(root, "level1")
	level2 := filepath.Join(level1, "level2")
	tooDeepFile := filepath.Join(level2, "too-deep")
	if err := os.MkdirAll(level2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tooDeepFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tooDeepBefore := ctimeOf(t, tooDeepFile)
	ctimeSettle()

	var gotCutoff bool
	uid, gid := os.Getuid(), os.Getgid()
	walked, _, err := ChownTreeNoFollow(root, uid, gid, func(uint32) bool { return true }, false, func(name string, e error) {
		if e == ErrMaxWalkDepthExceeded {
			gotCutoff = true
		}
	})
	if err != nil {
		t.Fatalf("ChownTreeNoFollow: %v", err)
	}
	if !gotCutoff {
		t.Fatal("expected onErr to report ErrMaxWalkDepthExceeded")
	}
	// root, level1, level2 — not too-deep-file, which is beyond the cap.
	if walked != 3 {
		t.Errorf("walked = %d, want 3 (root, level1, level2 — not the file beyond the cap)", walked)
	}
	if ctimeOf(t, tooDeepFile) != tooDeepBefore {
		t.Error("expected the file beyond the depth cap to be left untouched")
	}
}

// TestRemoveContentsNoFollow_DepthCapStopsDescendingAndReportsViaOnErr
// mirrors the chown depth-cap test for the delete walk.
func TestRemoveContentsNoFollow_DepthCapStopsDescendingAndReportsViaOnErr(t *testing.T) {
	origDepth := maxWalkDepth
	maxWalkDepth = 2
	t.Cleanup(func() { maxWalkDepth = origDepth })

	root := t.TempDir()
	level1 := filepath.Join(root, "level1")
	level2 := filepath.Join(level1, "level2")
	tooDeepFile := filepath.Join(level2, "too-deep")
	if err := os.MkdirAll(level2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tooDeepFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := OpenDirNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	var gotCutoff bool
	if _, err := RemoveContentsNoFollow(dir, nil, func(name string, e error) {
		if e == ErrMaxWalkDepthExceeded {
			gotCutoff = true
		}
	}); err != nil {
		t.Fatalf("RemoveContentsNoFollow: %v", err)
	}
	if !gotCutoff {
		t.Fatal("expected onErr to report ErrMaxWalkDepthExceeded")
	}
	if _, err := os.Stat(tooDeepFile); err != nil {
		t.Errorf("expected the file beyond the depth cap to survive: %v", err)
	}
}
