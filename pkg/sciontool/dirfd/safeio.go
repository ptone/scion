/*
Copyright 2026 The Scion Authors.
*/

package dirfd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrNotSingleLinkRegular is returned by ReadAtNoFollow (and everything
// built on it) when the opened entry is not a regular file with exactly one
// hard link: a FIFO, a device, a directory, or a hardlink to an unrelated
// (possibly root-owned) file. A symlink at the leaf never reaches this
// check at all — O_NOFOLLOW on the open refuses it first (ELOOP) — but the
// same sentinel is used for both so callers have one thing to test for
// "this wasn't a plain file I can safely read", whichever way the refusal
// happened.
//
// Callers running in a root context are expected to treat this the way
// readServicesYAML's enforced branch already does: log the path and skip —
// not crash or fail hard — since a workload that owns the containing
// directory can always produce one of these instead of the file root
// expects, and root must not let that choice affect its own control flow.
var ErrNotSingleLinkRegular = errors.New("dirfd: not a single-link regular file")

// ErrTooLarge is returned when a read's content would exceed the caller-
// supplied byte limit. Detected via io.LimitReader(f, max+1): if that many
// bytes come back, the file is at least max+1 bytes long, so the read never
// buffers more than one byte past the limit before refusing it.
var ErrTooLarge = errors.New("dirfd: content exceeds size limit")

// ErrPathEscapesRoot is returned by ReadUnderRootNoFollow when path, once
// normalized, does not resolve to somewhere inside root — either because it
// requires a ".." to get there, or because filepath.Rel could not express
// it as a descendant of root at all. Refused before any filesystem call is
// made, since no amount of walking changes that answer.
var ErrPathEscapesRoot = errors.New("dirfd: path escapes root")

// ReadAtNoFollow opens name relative to dirFd for reading with
// O_NOFOLLOW|O_NONBLOCK — refusing (ELOOP), not following, a symlink
// planted at name, and never blocking the open even if name is a FIFO with
// no writer waiting (O_NONBLOCK makes open(2) return immediately instead of
// waiting for a writer to connect, which matters because this can run in
// root's own PID-1 process and must never hang it). It then fstats the
// result and requires a single-link regular file — ErrNotSingleLinkRegular
// otherwise, which also covers the FIFO case: a FIFO fails the S_IFREG
// check right here, before any read is ever attempted, so O_NONBLOCK's
// read-side semantics (which only prevent a blocking read, not guarantee
// one byte of data) never come into play. Finally it reads at most max+1
// bytes via io.LimitReader and reports ErrTooLarge if that bound is
// exceeded, the same "read one byte past the limit and check" pattern
// readServicesYAML uses.
//
// This is the shared core behind every bounded, no-follow read in this
// package: callers that already hold an open, symlink-safe directory fd —
// a root anchor from OpenDirNoFollow, or a caller doing its own component
// walk — call this directly; ReadFileNoFollow wraps it for the common
// "read one absolute path" case.
func ReadAtNoFollow(dirFd int, name string, max int64) ([]byte, error) {
	f, err := OpenAt(dirFd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("dirfd: open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()

	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return nil, fmt.Errorf("dirfd: stat %s: %w", name, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return nil, fmt.Errorf("dirfd: %s: %w", name, ErrNotSingleLinkRegular)
	}

	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("dirfd: read %s: %w", name, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("dirfd: %s: %w", name, ErrTooLarge)
	}
	return data, nil
}

// ReadFileNoFollow reads path the way readServicesYAML's enforced branch
// does: OpenParentNoFollow walks every directory component from "/" down
// with O_NOFOLLOW, so a symlinked intermediate directory is refused rather
// than followed, then ReadAtNoFollow opens and bounds-checks the leaf.
// path does not need to exist; a missing leaf or a missing intermediate
// directory both come back satisfying errors.Is(err, os.ErrNotExist), so
// callers that treat "absent" as "nothing to read" keep that behaviour.
func ReadFileNoFollow(path string, max int64) ([]byte, error) {
	dirFd, leaf, err := OpenParentNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Close(dirFd) }()

	return ReadAtNoFollow(dirFd, leaf, max)
}

// writeNoFollowPreChmodTestHook, when non-nil, fires in WriteFileNoFollow
// right after the temp file has been written and before it is chmod'd (and
// chowned, if requested). It receives the temp file's leaf name — purely
// informational, so a test can simulate "the temp file's directory entry
// was unlinked and replaced with a symlink to some unrelated file in
// exactly this window" and then prove the subsequent Chmod/Chown still
// land on the original temp file's inode, never on whatever the swapped-in
// symlink points to, because both calls are fd-based (fchmod/fchown on the
// already-open descriptor) rather than a path-based lookup of the name.
// Always nil in production; unexported — this package's own tests are the
// only thing that may set it. Same purpose and fd-immutability guarantee
// as walk.go's chownWalkTestHook family; see its doc comment.
var writeNoFollowPreChmodTestHook func(tmpName string)

// WriteFileNoFollow atomically replaces path's content the way
// writeLimitsState does: it resolves path's parent directory once via
// OpenParentNoFollow, creates a temp file in that directory with
// CreateExclAt (O_CREAT|O_EXCL|O_NOFOLLOW, so the temp name itself cannot
// already be a pre-planted symlink), writes data to it, sets its
// permission bits via Chmod on the open *os.File (fchmod on the fd — never
// a path-based os.Chmod, which a symlink swapped in at the temp path
// between create and chmod could redirect to an arbitrary target's
// permissions) and, when uid > 0, its ownership the same fd-based way
// before ever renaming it anywhere, then renames it into place with a
// single fd-relative RenameAt. The temp file is removed on any error path.
func WriteFileNoFollow(path string, data []byte, mode os.FileMode, uid, gid int) (err error) {
	dirFd, leaf, err := OpenParentNoFollow(path)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(dirFd) }()

	// PID + nanosecond timestamp is unique enough that CreateExclAt's
	// O_EXCL is only ever a defense against a pre-planted entry at this
	// name, not a collision this process itself needs to retry.
	tmpName := fmt.Sprintf(".%s.tmp-%d-%d", leaf, os.Getpid(), time.Now().UnixNano())
	tmpFile, err := CreateExclAt(dirFd, tmpName, 0o600)
	if err != nil {
		return fmt.Errorf("dirfd: create temp for %s: %w", path, err)
	}
	return writeTempAndRename(dirFd, tmpFile, tmpName, leaf, path, data, mode, uid, gid)
}

func writeTempAndRename(dirFd int, tmpFile *os.File, tmpName, leaf, path string, data []byte, mode os.FileMode, uid, gid int) (err error) {
	defer func() {
		if err != nil {
			_ = UnlinkAt(dirFd, tmpName)
		}
	}()

	if _, werr := tmpFile.Write(data); werr != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("dirfd: write temp for %s: %w", path, werr)
	}

	if writeNoFollowPreChmodTestHook != nil {
		writeNoFollowPreChmodTestHook(tmpName)
	}

	if cerr := tmpFile.Chmod(mode); cerr != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("dirfd: chmod temp for %s: %w", path, cerr)
	}
	if uid > 0 {
		if cerr := tmpFile.Chown(uid, gid); cerr != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("dirfd: chown temp for %s: %w", path, cerr)
		}
	}
	if cerr := tmpFile.Close(); cerr != nil {
		return fmt.Errorf("dirfd: close temp for %s: %w", path, cerr)
	}

	if rerr := RenameAt(dirFd, tmpName, leaf); rerr != nil {
		return fmt.Errorf("dirfd: rename into %s: %w", path, rerr)
	}
	return nil
}

// relUnderRoot resolves path and root to absolute form and reports the
// path relative to root, refusing (ErrPathEscapesRoot) anything that
// requires a ".." to get there, is root itself, or that filepath.Rel could
// not express as a descendant of root at all. This is a string-level
// pre-check only — ReadUnderRootNoFollow does not trust it for safety, only
// for turning path into the list of component names it walks one openat
// per component; containment itself is enforced by that walk never
// resolving a name outside the fd chain it holds, not by this check.
func relUnderRoot(root, path string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("dirfd: resolve root %s: %w", root, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("dirfd: resolve %s: %w", path, err)
	}
	rel, err := filepath.Rel(absRoot, absPath)
	escapes := err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
	if !escapes {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if part == "" || part == "." || part == ".." {
				escapes = true
				break
			}
		}
	}
	if escapes {
		return "", fmt.Errorf("dirfd: %s: %w", path, ErrPathEscapesRoot)
	}
	return rel, nil
}

// readUnderRootIntermediateCloseTestHook, when non-nil, fires in
// ReadUnderRootNoFollow immediately after an owned intermediate directory
// fd is closed — whether the next component's openat succeeded or not. It
// receives the exact fd number that was just closed, purely informational,
// so a test can immediately dup a sentinel onto that number (claiming it
// deterministically, rather than hoping the kernel's normal allocator
// happens to reuse it for something else first) and later prove this
// package's own deferred closer never touches that number again. Always
// nil in production; unexported — this package's own tests are the only
// thing that may set it. Same purpose as walk.go's chownWalkTestHook
// family; see its doc comment.
var readUnderRootIntermediateCloseTestHook func(closedFd int)

// ReadUnderRootNoFollow reads path, which must resolve to somewhere inside
// root, by walking from root down to path one component at a time with
// openat(..., O_NOFOLLOW): root itself is opened once via OpenDirNoFollow
// (refusing a symlink at root's own leaf too), and every remaining
// directory component is opened relative to the previous component's own
// already-open fd — never by re-parsing a path string — with
// O_DIRECTORY|O_NOFOLLOW, before the final component is read via
// ReadAtNoFollow's own O_NOFOLLOW|O_NONBLOCK open and bounded read.
//
// This makes containment itself symlink-safe, which a string-prefix or
// filepath.Rel check alone is not: a symlink whose own *name* sits inside
// root but whose target does not (root/link -> /etc/shadow) is refused
// (ELOOP) at the component that resolves "link", never followed, even
// though the string form "root/link" passes any textual containment check.
// relUnderRoot's job is only to turn path into the list of component names
// this function walks; every actual safety guarantee comes from resolving
// each of those names against an fd already anchored inside root, one
// openat per component, with no step that ever looks a name up starting
// from "/" again.
//
// There is no separate stat anywhere in this path: the file is fstat'd and
// read exactly once, from the same fd the walk verified, by
// ReadAtNoFollow.
func ReadUnderRootNoFollow(root, path string, max int64) ([]byte, error) {
	rel, err := relUnderRoot(root, path)
	if err != nil {
		return nil, err
	}

	rootFile, err := OpenDirNoFollow(root)
	if err != nil {
		return nil, fmt.Errorf("dirfd: open root %s: %w", root, err)
	}
	defer func() { _ = rootFile.Close() }()

	parts := strings.Split(rel, string(filepath.Separator))
	curFd := int(rootFile.Fd())
	ownsCur := false
	defer func() {
		if ownsCur {
			_ = syscall.Close(curFd)
		}
	}()

	for _, name := range parts[:len(parts)-1] {
		child, operr := syscall.Openat(curFd, name, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if ownsCur {
			_ = syscall.Close(curFd)
			if readUnderRootIntermediateCloseTestHook != nil {
				readUnderRootIntermediateCloseTestHook(curFd)
			}
			// Mark curFd's ownership released the instant it's closed, not
			// after the error check below: on an error return, the deferred
			// closer above would otherwise still see ownsCur==true and close
			// this same (already-closed) fd number a second time. In a
			// multi-threaded process — this walk can run in root's own
			// PID-1 — a second goroutine can have already been handed that
			// number back by the kernel between the two closes, so the
			// second close would silently close somebody else's fd instead
			// of erroring.
			ownsCur = false
		}
		if operr != nil {
			return nil, fmt.Errorf("dirfd: open %s: %w", name, operr)
		}
		curFd = child
		ownsCur = true
	}

	return ReadAtNoFollow(curFd, parts[len(parts)-1], max)
}
