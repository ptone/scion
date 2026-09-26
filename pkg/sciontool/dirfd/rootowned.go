/*
Copyright 2026 The Scion Authors.
*/

package dirfd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// trustedChainModeBits are the permission bits chainIsTrusted refuses on any
// component: group-write and other-write. There is deliberately no
// sticky-bit exemption. A sticky bit only makes a world-writable directory
// (e.g. a hardened "/tmp") resistant to one workload renaming or unlinking
// another workload's entries — it does nothing to stop a workload from
// creating a brand new entry there in the first place, which is all the
// directories this check protects need to resist.
const trustedChainModeBits = 0o022

// chainIsTrusted reports whether a single already-fstat'd component (an
// arbitrary node along a directory chain) is safe to treat as under this
// process's exclusive control: owned by uid 0, or — when this process is not
// itself running as root (a rootless/keep-id deployment, where PID 1 starts
// as the workload identity and there is no separate root/workload boundary
// to protect at all) — owned by selfUID, the caller's own effective uid.
// Either way, it must carry neither the group- nor the other-write bit.
//
// It says nothing about any OTHER component in the chain — callers combine
// every component's result themselves by requiring it of all of them. Kept
// as a pure function of already-fetched values (not one that stats anything
// itself), so it can be exercised by a test with fabricated ownership/mode
// values, the same way pkg/sciontool/hooks.NodeOwnership.rootProtected is
// tested, without requiring the test process itself to be root or to own a
// real root-owned directory.
func chainIsTrusted(uid uint32, mode uint32, selfUID uint32) bool {
	return (uid == 0 || uid == selfUID) && mode&trustedChainModeBits == 0
}

// OpenParentNoFollowRootOwned resolves path's parent directory exactly like
// OpenParentNoFollow — walking every component from "/" down with
// openat(2) O_DIRECTORY|O_NOFOLLOW, so a symlink planted at any component is
// refused rather than followed — but in addition fstats every already-open
// component, including "/" itself, and fails closed (see chainIsTrusted) the
// instant any of them is not safe to treat as under this process's exclusive
// control.
//
// This exists for a directory that must be root-only along its ENTIRE
// chain, not just at its own leaf: OpenParentNoFollow alone refuses a
// symlink swapped into an ancestor, but says nothing about an ancestor that
// is simply writable by everyone (e.g. an unhardened, non-sticky "/tmp"),
// which on its own is enough for a workload to rename an entry out of it and
// plant a symlink in its place. No component's mode is ever assumed from
// its name — not even "/" or "/run", both of which can legitimately vary
// across images and runtimes — every one is checked by fstat on its own
// already-open descriptor as the walk proceeds, which also closes the
// TOCTOU window a separate stat-by-path-then-open-by-path sequence would
// leave open.
func OpenParentNoFollowRootOwned(path string) (dirFd int, leaf string, err error) {
	selfUID := uint32(os.Geteuid())

	abs, err := filepath.Abs(path)
	if err != nil {
		return -1, "", fmt.Errorf("dirfd: resolve %s: %w", path, err)
	}
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) {
		return -1, "", fmt.Errorf("dirfd: refusing to resolve the root directory itself")
	}

	trimmed := strings.TrimPrefix(abs, string(filepath.Separator))
	parts := strings.Split(trimmed, string(filepath.Separator))
	leaf = parts[len(parts)-1]
	dirs := parts[:len(parts)-1]

	fd, err := syscall.Open(string(filepath.Separator), syscall.O_DIRECTORY|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", fmt.Errorf("dirfd: open /: %w", err)
	}
	if verr := fstatRequireTrusted(fd, string(filepath.Separator), selfUID); verr != nil {
		_ = syscall.Close(fd)
		return -1, "", verr
	}

	walked := string(filepath.Separator)
	for _, name := range dirs {
		child, oerr := syscall.Openat(fd, name, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(fd)
		if oerr != nil {
			return -1, "", fmt.Errorf("dirfd: open %s: %w", name, oerr)
		}
		walked = filepath.Join(walked, name)
		if verr := fstatRequireTrusted(child, walked, selfUID); verr != nil {
			_ = syscall.Close(child)
			return -1, "", verr
		}
		fd = child
	}
	return fd, leaf, nil
}

// fstatRequireTrusted fstats fd and fails closed via chainIsTrusted.
// displayPath is used only for the returned error's message.
func fstatRequireTrusted(fd int, displayPath string, selfUID uint32) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("dirfd: fstat %s: %w", displayPath, err)
	}
	if !chainIsTrusted(st.Uid, uint32(st.Mode), selfUID) {
		return fmt.Errorf("dirfd: %s is not root-owned (or self-owned) and free of group/other write (uid=%d mode=%#o)", displayPath, st.Uid, st.Mode&0o7777)
	}
	return nil
}

// EnsureDirNoFollowRootOwned behaves like EnsureDirNoFollow, but resolves its
// parent chain with OpenParentNoFollowRootOwned instead of OpenParentNoFollow
// (see that function's doc comment for what the extra check buys), and
// applies the identical fstat check to path's own leaf — once it exists,
// whether this call just created it or it was already there — before
// returning its fd. Fails closed: an ancestor or the leaf itself failing
// that check, or the leaf already existing as a symlink or non-directory, is
// returned as an error. There is never a fallback to any other location;
// callers that need one (e.g. an unhardened scratch directory) must not use
// this function.
func EnsureDirNoFollowRootOwned(path string, mode os.FileMode) (*os.File, error) {
	dirFd, leaf, err := OpenParentNoFollowRootOwned(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Close(dirFd) }()

	if err := syscall.Mkdirat(dirFd, leaf, uint32(mode)); err != nil && err != syscall.EEXIST {
		return nil, fmt.Errorf("dirfd: mkdir %s: %w", path, err)
	}

	f, err := OpenAt(dirFd, leaf, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if verr := fstatRequireTrusted(int(f.Fd()), path, uint32(os.Geteuid())); verr != nil {
		_ = f.Close()
		return nil, verr
	}
	return f, nil
}
