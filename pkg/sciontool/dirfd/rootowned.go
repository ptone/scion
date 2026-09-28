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

	"golang.org/x/sys/unix"
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

// OpenNoFollowRootOwnedFile opens path as a regular file, applying the same
// root-owned-or-self-owned, not-group/other-writable check to its entire
// parent chain (OpenParentNoFollowRootOwned) and to the leaf itself that
// EnsureDirNoFollowRootOwned applies to a directory.
//
// path must already be free of symlink components by the time this is
// called — e.g. the output of filepath.EvalSymlinks — so this can safely use
// O_NOFOLLOW at every step without refusing a perfectly ordinary system
// layout where "/bin" or "/sbin" are themselves symlinks into "/usr" (Debian
// and most other modern distributions' merged-/usr layout): a caller that
// wants to allow a legitimate root-installed symlink chain (e.g. Debian's
// iptables, which resolves through /etc/alternatives) resolves it first,
// then verifies the real destination with this function — the destination,
// and every real directory leading to it, is what must be root-owned (or
// self-owned, on a runtime with no separate root/workload identity to
// protect against), never the symlink names along the way.
//
// This is pkg/sciontool/rootexec's own verification primitive: it is what
// makes resolving a bare command name against a fixed search path safe even
// though the resolved binary is often reached through one or more
// root-installed symlinks.
func OpenNoFollowRootOwnedFile(path string) (*os.File, error) {
	dirFd, leaf, err := OpenParentNoFollowRootOwned(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Close(dirFd) }()

	f, err := OpenAt(dirFd, leaf, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("dirfd: open %s: %w", path, err)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("dirfd: fstat %s: %w", path, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = f.Close()
		return nil, fmt.Errorf("dirfd: %s is not a regular file (mode %#o)", path, st.Mode&syscall.S_IFMT)
	}
	if verr := fstatRequireTrusted(int(f.Fd()), path, uint32(os.Geteuid())); verr != nil {
		_ = f.Close()
		return nil, verr
	}
	return f, nil
}

// AmbientTempDirTrusted reports whether path (typically os.TempDir()) is
// safe to pass to os.MkdirTemp as an ambient, not individually
// chain-verified, parent directory: either it carries the sticky bit (so
// only an entry's own owner — not merely anyone with write access to the
// directory — can rename or remove it, the property a hardened "/tmp"
// relies on), or it is owned by uid 0 and carries neither the group- nor
// other-write bit (chainIsTrusted's own rule, root's exclusive control).
//
// path itself is opened with O_DIRECTORY|O_NOFOLLOW, so a symlink planted at
// path's own name is refused (reported as untrusted) rather than followed,
// and the result is fstatted rather than trusted from a separate
// stat-by-path call. Its own ancestor chain is verified too, via
// OpenParentNoFollowRootOwned: a sticky bit on path itself is not enough if
// its parent is workload-writable, since the workload could then rename
// path (or an ancestor of it) out of the way and plant a symlink at the
// same name before path is ever used — the sticky bit only protects
// entries that already exist inside path, not path's own directory entry
// in its parent.
func AmbientTempDirTrusted(path string) bool {
	dirFd, leaf, err := OpenParentNoFollowRootOwned(path)
	if err != nil {
		return false
	}
	defer func() { _ = syscall.Close(dirFd) }()

	fd, err := syscall.Openat(dirFd, leaf, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer func() { _ = syscall.Close(fd) }()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return false
	}
	if st.Mode&syscall.S_ISVTX != 0 && st.Uid == 0 {
		return true
	}
	return chainIsTrusted(st.Uid, uint32(st.Mode), uint32(os.Geteuid()))
}

// maxSymlinkHops bounds VerifyRootOwnedExecutable's manual symlink-chain
// walk, generously beyond any real installation this codebase's search
// paths are expected to encounter (Debian's update-alternatives chains are
// two hops).
const maxSymlinkHops = 20

// VerifyRootOwnedExecutable verifies that candidate — and, if it is a
// symlink, every hop of its symlink chain down to the final destination —
// are each root-owned (or self-owned, on a runtime with no separate root/
// workload identity to protect against at all) and free of the group- and
// other-write bits, including each hop's own containing-directory chain
// (walked from "/" via OpenParentNoFollowRootOwned, exactly like a
// standalone file's). This is what makes it safe for a caller to go on to
// exec candidate itself, rather than the fully-resolved destination: once
// every hop and its own directory chain are proven under root's (or self's)
// exclusive control, the workload has no write access anywhere along the
// path a later, real execve() would re-walk, so there is no window between
// this check and that exec for anything to change.
//
// A legitimate root-installed symlink chain (Debian's "iptables" ->
// "/etc/alternatives/iptables" -> "xtables-nft-multi", a multi-call binary
// that dispatches on argv[0]'s own basename) is followed and verified hop
// by hop, never refused outright the way OpenNoFollowRootOwnedFile's single
// O_NOFOLLOW open refuses any symlink at its own leaf. A caller that needs
// a multi-call binary's dispatch to work needs candidate's OWN basename to
// survive into argv[0], which only holds if it goes on to exec candidate
// itself, not whatever this walk resolves it to — so this function never
// returns the resolved path, only whether exec'ing candidate is safe.
func VerifyRootOwnedExecutable(candidate string) error {
	selfUID := uint32(os.Geteuid())
	current := candidate
	visited := make(map[string]bool, maxSymlinkHops)

	for hop := 0; hop < maxSymlinkHops; hop++ {
		if visited[current] {
			return fmt.Errorf("dirfd: symlink loop resolving %s", candidate)
		}
		visited[current] = true

		dirFd, leaf, err := OpenParentNoFollowRootOwned(current)
		if err != nil {
			return fmt.Errorf("dirfd: %s: %w", candidate, err)
		}

		fd, openErr := syscall.Openat(dirFd, leaf, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if openErr == nil {
			var st syscall.Stat_t
			statErr := syscall.Fstat(fd, &st)
			_ = syscall.Close(fd)
			_ = syscall.Close(dirFd)
			if statErr != nil {
				return fmt.Errorf("dirfd: fstat %s: %w", current, statErr)
			}
			if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
				return fmt.Errorf("dirfd: %s is not a regular file (mode %#o)", current, st.Mode&syscall.S_IFMT)
			}
			if !chainIsTrusted(st.Uid, uint32(st.Mode), selfUID) {
				return fmt.Errorf("dirfd: %s is not root-owned (or self-owned) and free of group/other write (uid=%d mode=%#o)", current, st.Uid, st.Mode&0o7777)
			}
			return nil
		}
		if !errors.Is(openErr, syscall.ELOOP) {
			_ = syscall.Close(dirFd)
			return fmt.Errorf("dirfd: open %s: %w", current, openErr)
		}

		// current's leaf is a symlink: verify the symlink directory entry's
		// own OWNER (unix.Fstatat with AT_SYMLINK_NOFOLLOW is the
		// fd-relative equivalent of Lstat — a fresh workload-planted
		// symlink here would show the workload's own uid). A symlink's own
		// permission bits are not meaningful on Linux — the kernel reports
		// (and largely ignores) a fixed mode for every symlink regardless
		// of anything resembling a chmod — so only ownership is checked
		// here, unlike chainIsTrusted's combined uid+mode rule for a real
		// file or directory. Then read the target and continue the walk.
		var lst unix.Stat_t
		if err := unix.Fstatat(dirFd, leaf, &lst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			_ = syscall.Close(dirFd)
			return fmt.Errorf("dirfd: lstat %s: %w", current, err)
		}
		if lst.Uid != 0 && lst.Uid != selfUID {
			_ = syscall.Close(dirFd)
			return fmt.Errorf("dirfd: symlink %s is not owned by root (or self) (uid=%d)", current, lst.Uid)
		}
		buf := make([]byte, 4096)
		n, rlErr := unix.Readlinkat(dirFd, leaf, buf)
		_ = syscall.Close(dirFd)
		if rlErr != nil {
			return fmt.Errorf("dirfd: readlink %s: %w", current, rlErr)
		}
		target := string(buf[:n])
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(current), target)
		}
		current = filepath.Clean(target)
	}
	return fmt.Errorf("dirfd: too many symlink hops resolving %s (> %d)", candidate, maxSymlinkHops)
}

// ErrSymlinkChainNotOwnedByUID is returned by ReadFileNoFollowOwnedBy when
// resolving path's symlink chain reaches a hop — an intermediate symlink,
// or the final regular file it lands on — that is not owned by ownerUID.
var ErrSymlinkChainNotOwnedByUID = errors.New("dirfd: symlink chain is not owned by the expected uid")

// ReadFileNoFollowOwnedBy reads path exactly like ReadFileNoFollow when path
// is not a symlink. When path (or a symlink further down its chain) IS a
// symlink, it is followed — one hop at a time, re-walking from "/" for each
// hop the same way VerifyRootOwnedExecutable does for a root-owned chain —
// as long as EVERY hop, including the final destination, is owned by
// ownerUID. The instant any hop is owned by anyone else, the chain is
// longer than maxSymlinkHops, or the destination is not a single-link
// regular file, resolution is refused (not guessed at): it reports
// ErrSymlinkChainNotOwnedByUID or the underlying I/O error, never partial
// or best-effort content.
//
// Restarting from "/" for each hop is safe here for the same reason it is
// safe in VerifyRootOwnedExecutable, even though the directory chain
// leading to path is owned by ownerUID rather than root: every hop this
// function is willing to trust is independently re-verified — by fstat on
// the fd it just opened, at the moment it opens it — never from information
// a previous iteration cached. A race that swaps an intermediate directory
// between computing one hop's absolute path and resolving it can therefore
// only ever land this walk on something ALSO owned by ownerUID (which it
// then re-checks before trusting) or make the open/stat fail outright.
// Since ownerUID already owns and can rewrite everything in that directory
// tree without this function's help, such a race grants it no access it
// did not already have.
//
// This is deliberately not the same pattern ReadUnderRootNoFollow uses for
// env-overlay's from_file (see that function's own doc comment): that walk
// stays anchored to a single fd across every hop precisely because the
// boundary it protects (an allowed root) is one the CALLER controls, not
// necessarily one the path's owner controls, so a path re-lookup between
// hops there really could let a workload redirect root outside a boundary
// the workload does not own. Here the boundary IS "does ownerUID already
// own this," which every hop re-derives fresh from the kernel regardless of
// how it got there.
//
// This exists for exactly one case: a workload's own dotfile manager
// (chezmoi, stow, dotbot, ...) symlinking a config file such as
// ~/.gitconfig into its own managed store, where refusing every symlink
// unconditionally made root treat that legitimate setup as if the file
// were simply absent — silently starting every credential helper and git
// identity setting from empty instead.
func ReadFileNoFollowOwnedBy(path string, ownerUID uint32, max int64) ([]byte, error) {
	current := path
	visited := make(map[string]bool, maxSymlinkHops)

	for hop := 0; hop < maxSymlinkHops; hop++ {
		abs, err := filepath.Abs(current)
		if err != nil {
			return nil, fmt.Errorf("dirfd: resolve %s: %w", current, err)
		}
		abs = filepath.Clean(abs)
		if visited[abs] {
			return nil, fmt.Errorf("dirfd: symlink loop resolving %s", path)
		}
		visited[abs] = true

		dirFd, leaf, err := OpenParentNoFollow(abs)
		if err != nil {
			return nil, err
		}

		data, rerr := readRegularOwnedAt(dirFd, leaf, ownerUID, max)
		if rerr == nil {
			_ = syscall.Close(dirFd)
			return data, nil
		}
		if !isSymlinkAt(dirFd, leaf) {
			_ = syscall.Close(dirFd)
			return nil, rerr
		}

		// leaf is a confirmed symlink: verify the symlink directory entry's
		// own owner (the fd-relative equivalent of Lstat) before trusting
		// its target text at all — mirroring VerifyRootOwnedExecutable's
		// own per-hop ownership check.
		var lst unix.Stat_t
		if serr := unix.Fstatat(dirFd, leaf, &lst, unix.AT_SYMLINK_NOFOLLOW); serr != nil {
			_ = syscall.Close(dirFd)
			return nil, fmt.Errorf("dirfd: lstat %s: %w", abs, serr)
		}
		if lst.Uid != ownerUID {
			_ = syscall.Close(dirFd)
			return nil, fmt.Errorf("dirfd: symlink %s is owned by uid %d, not %d: %w", abs, lst.Uid, ownerUID, ErrSymlinkChainNotOwnedByUID)
		}
		target, terr := readlinkAt(dirFd, leaf)
		_ = syscall.Close(dirFd)
		if terr != nil {
			return nil, fmt.Errorf("dirfd: readlink %s: %w", abs, terr)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(abs), target)
		}
		current = filepath.Clean(target)
	}
	return nil, fmt.Errorf("dirfd: too many symlink hops resolving %s (> %d)", path, maxSymlinkHops)
}

// readRegularOwnedAt opens name relative to dirFd exactly like
// ReadAtNoFollow, but additionally requires the fd it just opened to be
// owned by ownerUID — checked via the same fstat that already proves
// single-link-regular, so there is no separate stat call and no window
// between checking ownership and reading content. Reports
// ErrSymlinkChainNotOwnedByUID (not ErrNotSingleLinkRegular) when the file
// is a single-link regular file but owned by someone else, so a caller can
// tell "wrong owner" apart from "wrong type".
func readRegularOwnedAt(dirFd int, name string, ownerUID uint32, max int64) ([]byte, error) {
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
	if st.Uid != ownerUID {
		return nil, fmt.Errorf("dirfd: %s is owned by uid %d, not %d: %w", name, st.Uid, ownerUID, ErrSymlinkChainNotOwnedByUID)
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
