/*
Copyright 2026 The Scion Authors.
*/

package dirfd

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// OpenDirNoFollow resolves path via OpenParentNoFollow and opens the leaf
// itself with O_DIRECTORY|O_NOFOLLOW — refusing (not following) a symlink at
// the leaf, and refusing anything that isn't a directory. Unlike
// EnsureDirNoFollow, it never creates path: a missing leaf is reported as an
// os.IsNotExist error, exactly like os.Open would, so callers that treat "no
// such directory" as a legitimate no-op (e.g. "nothing to clean up yet")
// keep that behaviour. The caller owns the returned fd and must close it.
func OpenDirNoFollow(path string) (*os.File, error) {
	dirFd, leaf, err := OpenParentNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Close(dirFd) }()

	return OpenAt(dirFd, leaf, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY, 0)
}

// ChownWalkTestHook, when non-nil, is invoked once right after this package
// opens a subdirectory encountered while walking (as a symlink-safe fd) and
// before it recurses into it. It receives the subdirectory's leaf name —
// purely informational, so a test can recognize "the directory it cares
// about" and act on that directory's entry in its parent (e.g. rename it
// away and plant a symlink in its place) — production code never derives
// any of its own operations from this name; every real fchownat/openat this
// package issues afterward stays relative to the fd it already holds, which
// is bound to the inode it was opened against and cannot be redirected by
// anything that happens to the directory's name in its parent afterward.
// This is what lets a test drive the exact "swap an intermediate directory
// for a symlink mid-walk" race deterministically instead of relying on a
// real, timing-dependent concurrent race. Always nil in production; tests
// must restore it to nil when done (it is a shared package-level var).
var ChownWalkTestHook func(name string)

// ChownTreeNoFollow walks root and everything beneath it, deciding for each
// entry — via shouldChown, given that entry's own (lstat, not stat) owning
// uid — whether to chown it to uid:gid. It never follows a symlink and never
// re-resolves a path once resolved: root itself is opened once via
// OpenDirNoFollow, and every subdirectory is opened relative to its own
// already-open parent directory fd (openat(O_DIRECTORY|O_NOFOLLOW)); every
// chown is fchownat(dirFd, name, uid, gid, AT_SYMLINK_NOFOLLOW), issued
// against that fd, never a full-path os.Lchown that would re-resolve every
// intermediate component (and could be redirected by a symlink swapped into
// one of them after the fact) each time it runs.
//
// A symlink entry is itself eligible for chowning (fchownat with
// AT_SYMLINK_NOFOLLOW chowns the symlink, never its target) but is never
// descended into. shouldChown is only ever asked about a real entry's own
// owning uid; the decision is made and acted on in the same fchownat call,
// with no separate "decide" step that a name swap could land in between.
//
// Returns the number of entries visited (including root itself) and the
// number actually chowned. An error opening or stat'ing root itself is
// returned; per-entry errors deeper in the tree (an entry vanished, or a
// race changed its type between the fstat and a recurse-open) are swallowed
// and the entry is skipped, matching chownTreeRootOwned's historical
// behaviour of logging and continuing rather than aborting the whole walk.
func ChownTreeNoFollow(root string, uid, gid int, shouldChown func(entryUID uint32) bool) (walked, changed int, err error) {
	rootFile, err := OpenDirNoFollow(root)
	if err != nil {
		return 0, 0, fmt.Errorf("dirfd: open %s: %w", root, err)
	}
	defer func() { _ = rootFile.Close() }()

	var rootSt unix.Stat_t
	if err := unix.Fstat(int(rootFile.Fd()), &rootSt); err != nil {
		return 0, 0, fmt.Errorf("dirfd: stat %s: %w", root, err)
	}
	walked++
	if shouldChown(rootSt.Uid) {
		// rootFile is an already-open fd bound to this exact inode: fchown
		// on it, never a path-based chown that could be redirected by
		// whatever root's own directory entry becomes afterward.
		if cerr := rootFile.Chown(uid, gid); cerr != nil {
			err = cerr
		} else {
			changed++
		}
	}

	w, c := chownWalkChildren(rootFile, uid, gid, shouldChown)
	walked += w
	changed += c
	return walked, changed, err
}

// chownWalkChildren lists dir's entries and, for each, fstat's it (via
// AT_SYMLINK_NOFOLLOW, never following) to decide whether to chown it and
// whether to recurse. dir is the sole *os.File wrapping its fd for its
// entire lifetime in this walk (opened once by the caller or by this
// function's own Openat below, closed exactly once by whichever of the two
// owns it) — never re-wrapped, so there is never a second *os.File whose GC
// finalizer could close the fd out from under the other.
func chownWalkChildren(dir *os.File, uid, gid int, shouldChown func(entryUID uint32) bool) (walked, changed int) {
	dirFd := int(dir.Fd())
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return 0, 0
	}

	for _, name := range names {
		var st unix.Stat_t
		if serr := unix.Fstatat(dirFd, name, &st, unix.AT_SYMLINK_NOFOLLOW); serr != nil {
			// Vanished or unreadable between listing and stat — skip.
			continue
		}
		walked++
		if shouldChown(st.Uid) {
			if cerr := syscall.Fchownat(dirFd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); cerr == nil {
				changed++
			}
		}

		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}

		childFd, oerr := syscall.Openat(dirFd, name, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if oerr != nil {
			// Raced out from under us (no longer a directory, or now a
			// symlink) — refuse rather than follow; skip.
			continue
		}
		child := os.NewFile(uintptr(childFd), name)
		if ChownWalkTestHook != nil {
			ChownWalkTestHook(name)
		}
		w, c := chownWalkChildren(child, uid, gid, shouldChown)
		walked += w
		changed += c
		_ = child.Close()
	}
	return walked, changed
}

// RemoveWalkTestHook, when non-nil, is invoked once right after this
// package opens a subdirectory it is about to empty and remove, before it
// lists/removes that subdirectory's own contents. Same purpose and same
// fd-holding guarantee as ChownWalkTestHook — see its doc comment. Always
// nil in production.
var RemoveWalkTestHook func(name string)

// RemoveContentsNoFollow removes every entry inside dir except those for
// which keep(name) returns true, without ever following a symlink or
// re-resolving a path: dir is an already-open fd, every removal is
// unlinkat(dirFd, name) (or unlinkat(dirFd, name, AT_REMOVEDIR) once a
// subdirectory's own contents are gone) relative to that fd, and every
// subdirectory is opened relative to its parent's held fd
// (openat(O_DIRECTORY|O_NOFOLLOW)) before being emptied the same way. This
// never re-resolves dir's path again after the caller opened it, so a
// symlink or directory swap planted at dir's own entry in its parent after
// that open cannot redirect anything this function does. It does not remove
// dir itself.
//
// Per-entry errors (an entry vanished, or a race changed its type between
// listing and the recurse-open) are swallowed and that entry is skipped
// rather than aborting the whole cleanup — the caller treats this
// best-effort, exactly like the historical os.RemoveAll-per-entry loop did
// (log.Debug on failure, keep going).
func RemoveContentsNoFollow(dir *os.File, keep func(name string) bool) (removed int, err error) {
	dirFd := int(dir.Fd())
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return 0, err
	}

	for _, name := range names {
		if keep != nil && keep(name) {
			continue
		}
		var st unix.Stat_t
		if serr := unix.Fstatat(dirFd, name, &st, unix.AT_SYMLINK_NOFOLLOW); serr != nil {
			continue
		}

		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			childFd, oerr := syscall.Openat(dirFd, name, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
			if oerr != nil {
				// No longer a plain directory (raced out from under us,
				// e.g. swapped for a symlink) — refuse rather than follow;
				// skip this entry entirely, leaving it in place.
				continue
			}
			child := os.NewFile(uintptr(childFd), name)
			if RemoveWalkTestHook != nil {
				RemoveWalkTestHook(name)
			}
			if _, rerr := RemoveContentsNoFollow(child, nil); rerr != nil {
				_ = child.Close()
				continue
			}
			_ = child.Close()
			if uerr := unix.Unlinkat(dirFd, name, unix.AT_REMOVEDIR); uerr != nil {
				continue
			}
		} else {
			if uerr := unix.Unlinkat(dirFd, name, 0); uerr != nil {
				continue
			}
		}
		removed++
	}
	return removed, nil
}
