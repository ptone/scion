/*
Copyright 2026 The Scion Authors.
*/

package hooks

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// EnforcedHooksDir is the dedicated, root-owned directory substrate-serve's
// bootstrap handler redirects broker-delivered $HOME/.scion/hooks/ content
// into, in privilege-drop-enforced mode. Kept as a single named identifier —
// not derived from anything else — so switching it later (e.g. if the
// live rootfs stat of "/run" ever changes) is a one-line change shared by
// both the writer (pkg/sciontool/substrate's writeBootstrapFile) and the
// reader (this package's LifecycleManager registration in
// cmd/sciontool/commands/init.go).
//
// Deliberately separate from /etc/scion/hooks (LifecycleManager's own
// system-default fallback): that directory's contents come from the image,
// not from a per-agent bootstrap write, and mixing the two would make a
// bootstrap-delivered file indistinguishable from one baked into the image.
//
// A package var, not a const, purely so a test can point it at a throwaway
// directory (e.g. to exercise skipRefusedEntry's EnforcedHooksDir exception
// without writing to the real, root-owned "/run/scion/hooks"). Production
// code never reassigns it.
var EnforcedHooksDir = "/run/scion/hooks"

// ErrScriptRefused is wrapped into the error openScriptNoFollow returns when
// the leaf itself — never a directory-chain component, which fails a
// different way (classifyChainOpenError) and is never treated as skippable
// — is a symlink or not a regular file. LifecycleManager's runScriptHooks
// uses errors.Is against this sentinel to decide whether a refused entry
// under a workload-owned hooks directory should be logged and skipped
// (letting the event's remaining hooks still run) instead of aborting the
// whole event — see skipRefusedEntry's own doc comment for why, and why
// EnforcedHooksDir is excluded from that treatment.
var ErrScriptRefused = errors.New("hooks: script is a symlink or not a regular file")

// openChainNoFollow opens every path component from "/" down to (and
// including) dir, refusing to follow a symlink at any component
// (O_NOFOLLOW), and returns each level's ownership (root-to-leaf order)
// alongside the final directory's own open fd (caller must close it).
//
// This is what makes the ownership chain DecideExecAsRoot checks the exact
// directories the script is opened under a moment later — not a directory a
// concurrent process swapped for a symlink between an earlier stat-by-path
// and this call. Every open is relative to the previous, already-verified
// fd (openat, never a fresh path lookup from "/"), so there is no window
// between checking a component and using it.
func openChainNoFollow(dir string) (fd int, chain []NodeOwnership, err error) {
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) {
		return -1, nil, fmt.Errorf("hooks: %q is not an absolute path", dir)
	}

	const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

	currentFd, err := unix.Open("/", dirFlags, 0)
	if err != nil {
		return -1, nil, fmt.Errorf("hooks: open /: %w", err)
	}
	rootOwnership, err := fstatOwnership(currentFd)
	if err != nil {
		_ = unix.Close(currentFd)
		return -1, nil, fmt.Errorf("hooks: fstat /: %w", err)
	}
	chain = append(chain, rootOwnership)

	if clean == "/" {
		return currentFd, chain, nil
	}

	for _, comp := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		nextFd, openErr := unix.Openat(currentFd, comp, dirFlags, 0)
		_ = unix.Close(currentFd)
		if openErr != nil {
			return -1, nil, classifyChainOpenError(comp, openErr)
		}
		currentFd = nextFd
		ownership, statErr := fstatOwnership(currentFd)
		if statErr != nil {
			_ = unix.Close(currentFd)
			return -1, nil, fmt.Errorf("hooks: fstat %q: %w", comp, statErr)
		}
		chain = append(chain, ownership)
	}
	return currentFd, chain, nil
}

// openScriptNoFollow opens the leaf script by name, relative to the
// already-opened and verified parent directory fd, refusing to follow a
// symlink there (O_NOFOLLOW) and refusing anything that is not a regular
// file once opened. Returns the open fd (caller must close it) and its own
// ownership.
func openScriptNoFollow(parentFd int, name string) (fd int, ownership NodeOwnership, err error) {
	// Deliberately no O_CLOEXEC: the caller execs this exact fd via
	// /proc/self/fd/<n> (see execViaFd), which requires the fd to still be
	// open in the forked child at the moment it resolves that magic symlink
	// during its own execve(2). See execViaFd's doc comment.
	//
	// This does mean the fd is inheritable by any OTHER child this process
	// forks while it is open — every hook runs synchronously and
	// LifecycleManager forks nothing else concurrently, so in practice
	// nothing else ever inherits it, but that is an invariant of the
	// caller's control flow, not something this function enforces. If a
	// future caller ever forks concurrently while a hook is running, revisit
	// this rather than assuming it still holds.
	f, err := unix.Openat(parentFd, name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return -1, NodeOwnership{}, fmt.Errorf("hooks: script %q is a symlink; refusing: %w", name, ErrScriptRefused)
		}
		return -1, NodeOwnership{}, err
	}
	var raw unix.Stat_t
	if statErr := unix.Fstat(f, &raw); statErr != nil {
		_ = unix.Close(f)
		return -1, NodeOwnership{}, statErr
	}
	if raw.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(f)
		return -1, NodeOwnership{}, fmt.Errorf("hooks: %q is not a regular file; refusing: %w", name, ErrScriptRefused)
	}
	return f, ownershipFromStat(raw), nil
}

// fstatOwnership Fstats an already-open directory fd and converts the
// result to NodeOwnership.
func fstatOwnership(fd int) (NodeOwnership, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return NodeOwnership{}, err
	}
	return ownershipFromStat(st), nil
}

func ownershipFromStat(st unix.Stat_t) NodeOwnership {
	return NodeOwnership{UID: st.Uid, Perm: uint32(st.Mode) & 0o7777}
}

// execViaFd builds an *exec.Cmd that runs the file referenced by the
// already-open, already-verified fd — never displayPath, which is used only
// for argv[0]/logging and is never itself opened or resolved again.
//
// This is the fexecve(3)-equivalent trick for a language (Go) whose os/exec
// has no native fexecve: /proc/self/fd/<n> is a magic symlink the kernel
// resolves directly to the fd's own open file description, not through a
// further filesystem path lookup, so execve("/proc/self/fd/<n>") runs
// exactly the inode fd refers to regardless of what (if anything) now sits
// at the script's original path. fd must be open without O_CLOEXEC (see
// openScriptNoFollow) so the forked child — which inherits the parent's fd
// table at fork(2), before its own execve(2) — still has it open at the
// moment the kernel resolves that path during the exec syscall itself
// (path resolution happens before the "point of no return" where O_CLOEXEC
// descriptors are closed on a successful exec).
func execViaFd(fd int, displayPath string) *exec.Cmd {
	cmd := exec.Command("/proc/self/fd/" + strconv.Itoa(fd))
	cmd.Args = []string{displayPath}
	return cmd
}

// fdIsExecutable Fstats an already-open fd and reports whether any of the
// owner/group/other execute bits is set.
func fdIsExecutable(fd int) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	return st.Mode&0o111 != 0, nil
}

// closeFd closes a raw file descriptor opened by this file's helpers.
func closeFd(fd int) error {
	return unix.Close(fd)
}

// classifyChainOpenError turns the errno openat(O_DIRECTORY|O_NOFOLLOW)
// returns for "this component isn't usable" into a clear, content-free
// error. On Linux, a symlink or a plain file blocking the path can both
// surface as ENOTDIR (the kernel checks O_DIRECTORY before O_NOFOLLOW) or
// ELOOP depending on the exact component; either way the open has already
// been correctly refused, so this only affects the error's wording.
func classifyChainOpenError(comp string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("hooks: path component %q is a symlink or not a directory; refusing", comp)
	}
	return fmt.Errorf("hooks: open path component %q: %w", comp, err)
}
