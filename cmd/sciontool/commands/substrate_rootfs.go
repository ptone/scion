/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

// fixupRootfsForScion corrects three observed conditions in a Substrate
// actor's rootfs that otherwise make dropping from root to the scion user
// impossible, or unsafe, no matter what capabilities the actor holds or how
// correctly SCION_HOST_UID/GID get applied:
//
//   - (i) '/' itself comes up too restrictive for the scion user to even
//     traverse into. agent-substrate/substrate's
//     internal/imagecache/bundle_linux.go (~lines 61-66) creates the
//     rootfs/upper/work directories with MkdirAll(..., 0o700), and the
//     actor's overlay root inherits the upperdir's own mode, so '/' comes
//     up 0700 root:root.
//   - (ii) image-layer files under $HOME are observed to be owned by uid 0
//     inside the actor, while runtime-created files are owned correctly;
//     the specific upstream mechanism is not identified. Either way the
//     effect is the same: the scion user can't read or write its own home
//     directory.
//   - (iii) /tmp and /var/tmp come up 0777 without the sticky bit, instead
//     of the 1777 their image layer (a Debian base) sets. The same
//     mode-bit-loss mechanism as (i) is suspected but not confirmed. A
//     shared, world-writable directory without the sticky bit lets any
//     process rename or delete another user's entries in it, and it also
//     turns off the kernel's protected_symlinks/protected_hardlinks
//     defenses, which only apply to sticky world-writable directories —
//     both matter here because this process, root, later creates and
//     opens predictable paths under /tmp on the scion user's behalf.
//
// It also corrects a fourth condition that isn't a rootfs oddity but a
// deliberate image posture this runtime alone can't tolerate:
//
//   - (iv) the image ships the scion user a passwordless sudo grant
//     (/etc/sudoers.d/scion, /etc/sudoers.d/scion-firewall — "scion
//     ALL=(ALL) NOPASSWD:ALL") on the assumption that sudo's own setuid bit
//     never survives into a running actor. On a runtime where root is a
//     security boundary, if that assumption is ever violated — a stale
//     golden template, or a rootfs mechanism that restores setuid the same
//     way it's observed restoring ownership in (ii) — the grant plus an
//     intact setuid bit is a single, no-password command away from full
//     root. Every other runtime this codebase targets keeps sudo and its
//     setuid bit intact on purpose (root is not a security boundary there),
//     so this half of the fixup, like the rest of this function, only ever
//     runs from Substrate's own call sites.
//
// root and home are parameters — not hardcoded to "/" and a resolved
// $HOME — purely so a test can point them at a t.TempDir() standing in for
// the real rootfs and home directory. /tmp and /var/tmp are derived from
// root the same way, so a test root without either subdirectory just skips
// that part of the fixup (see fixupWorldWritableTmpDirSticky's doc comment).
//
// Idempotent: when none of the three conditions need fixing, this makes no
// changes. It always logs one debug line with the home walk's duration and
// entry count — including on a no-op call, since the /bootstrap call site
// hits this on every actor start and is by design a no-op once startup has
// already fixed things up, so that cost would otherwise never be
// measurable at all — and logs one additional info line, with what
// changed, only when something actually did.
func fixupRootfsForScion(root, home string, uid, gid int) {
	start := time.Now()

	rootChanged := false
	if info, err := os.Stat(root); err != nil {
		log.Error("fixupRootfsForScion: failed to stat %s: %v", root, err)
	} else if perm := info.Mode().Perm(); perm&0o755 != 0o755 {
		// More restrictive than 0755: some bit 0755 needs is missing (e.g.
		// 0700). Only ever widen — a mode that's already at least as
		// permissive as 0755 (including wider, e.g. 0777) is left alone,
		// and ownership is never touched here.
		if err := os.Chmod(root, 0o755); err != nil {
			log.Error("fixupRootfsForScion: failed to chmod %s to 0755: %v", root, err)
		} else {
			rootChanged = true
		}
	}

	tmpChanged := fixupWorldWritableTmpDirSticky(filepath.Join(root, "tmp"))
	varTmpChanged := fixupWorldWritableTmpDirSticky(filepath.Join(root, "var", "tmp"))

	// PRIMARY control (condition (iv)): strip sudo's setuid/setgid/sticky
	// bits wherever a "sudo" binary is found under one of the fixed system
	// directories root-context code trusts (see the rootexec package's own
	// SearchPath) — the same directories, never a PATH search. This is what
	// checkPrivilegeDropFeasible's own sudo check (init.go) actually
	// enforces; the sudoers-grant removal below is defense in depth, not
	// the control that check relies on.
	sudoSetuidChanged := stripSudoSetuidBits(root)

	// SECONDARY control: remove the passwordless-sudo grants outright. Kept
	// even though the setuid strip above is what actually closes the
	// escalation (a non-setuid sudo binary can't do anything a grant alone
	// would use), because removing the grant costs nothing and a future
	// setuid restoration would otherwise still be paired with a live grant.
	sudoersChanged := removeSudoersGrants(root)

	// fixupRootfsForScion is substrate-only (see substrate_serve.go's call
	// site), so this always exercises chownTreeRootOwned's hardened,
	// no-follow branch — there is no non-substrate caller of this function
	// for whom byte-identical historical behaviour would need preserving.
	homeWalked, homeChanged, err := chownTreeRootOwned(home, uid, gid, true)
	if err != nil {
		log.Error("fixupRootfsForScion: failed to walk %s: %v", home, err)
	}

	// Unconditional, unlike the info line below: the /bootstrap call site
	// (RootfsFixup) hits this on every actor start and is a no-op by
	// design once the startup call has already fixed everything up, so
	// without this line that call's own cost — a full stat walk of $HOME —
	// would never be measurable at all.
	log.Debug("fixupRootfsForScion: walked %s in %s (%d entries, %d rechowned)",
		home, time.Since(start), homeWalked, homeChanged)

	if rootChanged || homeChanged > 0 || tmpChanged || varTmpChanged || sudoSetuidChanged || sudoersChanged {
		log.Info("fixupRootfsForScion: fixed up rootfs in %s (root chmod to 0755: %v, home entries rechowned: %d, tmp sticky bit set: %v, var/tmp sticky bit set: %v, sudo setuid stripped: %v, sudoers grants removed: %v)",
			time.Since(start), rootChanged, homeChanged, tmpChanged, varTmpChanged, sudoSetuidChanged, sudoersChanged)
	}
}

// sudoCheckDirs are the fixed system directories a "sudo" binary could
// legitimately live in — the same list rootexec.SearchPath uses, restated
// here rather than imported so this file (which builds without CGO
// concerns or any dependency beyond the standard library plus this
// package's own log helper) doesn't need to pull in pkg/sciontool/rootexec
// purely for four string literals both checkPrivilegeDropFeasible (below,
// same package) and this function need to agree on.
var sudoCheckDirs = []string{"usr/sbin", "usr/bin", "sbin", "bin"}

// stripSudoSetuidBits strips the setuid, setgid, and sticky special mode
// bits from every "sudo" binary found under root's copy of sudoCheckDirs.
// Returns whether anything was actually changed.
//
// Each candidate is opened with O_NOFOLLOW on the leaf component only (a
// directory symlink earlier in the path — e.g. a merged-/usr image's
// "/bin" -> "usr/bin" — is followed as normal; only a symlink AT "sudo"
// itself is refused, the same "resolve, then verify the real destination"
// shape rootexec.Resolve uses, except here the four candidate paths
// commonly resolve to the very same inode rather than four distinct
// binaries). fchmod runs on the open fd, never a path-based chmod, so a
// symlink swapped in between the open and the chmod can't redirect this
// call onto an arbitrary target's permissions. Anything that isn't a
// regular file (already a symlink refused above, or some other special
// file) is left alone.
func stripSudoSetuidBits(root string) (changed bool) {
	for _, dir := range sudoCheckDirs {
		if stripSetuidBitsNoFollow(filepath.Join(root, dir, "sudo")) {
			changed = true
		}
	}
	return changed
}

// specialModeBits are the setuid, setgid, and sticky bits — everything
// stripSetuidBitsNoFollow removes.
const specialModeBits = syscall.S_ISUID | syscall.S_ISGID | syscall.S_ISVTX

func stripSetuidBitsNoFollow(path string) bool {
	fd, err := syscall.Open(path, syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Error("fixupRootfsForScion: failed to open %s: %v", path, err)
		}
		return false
	}
	defer func() { _ = syscall.Close(fd) }()

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		log.Error("fixupRootfsForScion: failed to stat %s: %v", path, err)
		return false
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return false
	}
	perm := st.Mode & 0o7777
	if perm&specialModeBits == 0 {
		return false
	}
	if err := syscall.Fchmod(fd, perm&^uint32(specialModeBits)); err != nil {
		log.Error("fixupRootfsForScion: failed to strip setuid/setgid/sticky bits from %s: %v", path, err)
		return false
	}
	log.Info("fixupRootfsForScion: stripped setuid/setgid/sticky bits from %s", path)
	return true
}

// sudoersGrantNames are the passwordless-sudo grant files the image ships
// for the scion user, removed outright as defense in depth alongside the
// setuid strip above.
var sudoersGrantNames = []string{"scion", "scion-firewall"}

// removeSudoersGrants unlinks root's copy of every name in
// sudoersGrantNames under /etc/sudoers.d. The containing directory is
// opened once with O_DIRECTORY|O_NOFOLLOW (refusing a symlink planted at
// /etc/sudoers.d itself), and every removal is unlinkat against that one
// verified directory fd, by name — never a re-joined path string. A
// missing directory or a missing individual grant is treated as "already
// gone", not an error.
func removeSudoersGrants(root string) (changed bool) {
	dirPath := filepath.Join(root, "etc", "sudoers.d")
	dirFd, err := syscall.Open(dirPath, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Error("fixupRootfsForScion: failed to open %s: %v", dirPath, err)
		}
		return false
	}
	defer func() { _ = syscall.Close(dirFd) }()

	for _, name := range sudoersGrantNames {
		if err := syscall.Unlinkat(dirFd, name); err != nil {
			if !os.IsNotExist(err) {
				log.Error("fixupRootfsForScion: failed to remove %s/%s: %v", dirPath, name, err)
			}
			continue
		}
		log.Info("fixupRootfsForScion: removed %s/%s", dirPath, name)
		changed = true
	}
	return changed
}

// fixupWorldWritableTmpDirSticky sets a temp directory (/tmp or /var/tmp) to
// 01777 when it is world-writable but missing the sticky bit — condition
// (iii) in fixupRootfsForScion's doc comment. Returns whether it changed
// anything.
//
// It opens dir with O_DIRECTORY|O_NOFOLLOW, so a symlink planted at that
// path is refused (open fails) rather than followed, and it sets the mode
// with fchmod on the resulting fd rather than a path-based chmod, so a
// symlink swapped in between the open and the chmod can't redirect this at
// an arbitrary path — unlike the root chmod above, whose target ("/") is
// never attacker-controlled, /tmp's own entries are exactly what a
// workload can already write to.
//
// A missing directory, or one this process can't even open (e.g. a
// permissions oddity), is logged and treated as "nothing changed" rather
// than fatal: this is defence in depth alongside the token-writer hardening
// (WriteGitHubTokenFile and friends), not the only thing standing between a
// workload and root, so failing the whole rootfs fixup over it would cost
// more than it buys. Not every rootfs this runs against — including every
// test double — has a /tmp or /var/tmp, so a missing directory specifically
// is not even logged.
func fixupWorldWritableTmpDirSticky(dir string) bool {
	const worldWritable = 0o002
	const sticky = 0o1000

	fd, err := syscall.Open(dir, syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Error("fixupRootfsForScion: failed to open %s: %v", dir, err)
		}
		return false
	}
	defer func() { _ = syscall.Close(fd) }()

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		log.Error("fixupRootfsForScion: failed to stat %s: %v", dir, err)
		return false
	}

	perm := st.Mode & 0o7777
	if perm&worldWritable == 0 || perm&sticky != 0 {
		// Not world-writable, or already sticky: nothing to do.
		return false
	}

	// Add only the sticky bit, rather than forcing the full mode to 01777:
	// a dir already at, say, 0773 should end up 01773, not widened to 0777.
	if err := syscall.Fchmod(fd, perm|sticky); err != nil {
		log.Error("fixupRootfsForScion: failed to chmod %s to add the sticky bit: %v", dir, err)
		return false
	}
	return true
}

// fixupRootfsForScionUser resolves the image's "scion" user (through the
// injectable scionUserLookup — see its own doc comment) and runs
// fixupRootfsForScion against root and that user's own /etc/passwd home
// directory, uid and gid. If scion can't be resolved, it does nothing:
// checkPrivilegeDropFeasible performs the same lookup and fails the
// bootstrap closed on this later regardless, so there's no user to chown
// to here that would mean anything.
func fixupRootfsForScionUser(root string) {
	scionUser, err := scionUserLookup("scion")
	if err != nil {
		log.Debug("fixupRootfsForScion: scion user not found, skipping: %v", err)
		return
	}
	uid, uidErr := strconv.Atoi(scionUser.Uid)
	gid, gidErr := strconv.Atoi(scionUser.Gid)
	if uidErr != nil || gidErr != nil {
		log.Error("fixupRootfsForScion: scion user has an unparseable uid/gid, skipping")
		return
	}
	fixupRootfsForScion(root, scionUser.HomeDir, uid, gid)
	fixupEnforcedHooksDirChain()
}

// parentDirs returns every proper ancestor directory of path, from "/"
// down to (not including) path itself — e.g. parentDirs("/home/scion")
// returns ["/", "/home"]. Used by checkPrivilegeDropFeasible to enumerate
// exactly the directories a process must be able to search (execute)
// through to reach $HOME or the workspace path at all.
func parentDirs(path string) []string {
	var dirs []string
	dir := filepath.Dir(filepath.Clean(path))
	for {
		dirs = append([]string{dir}, dirs...)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}

// mergeDirLists concatenates directory lists into one, in first-seen order,
// without duplicates — e.g. multiple parentDirs() calls all include "/".
func mergeDirLists(lists ...[]string) []string {
	seen := make(map[string]bool)
	var merged []string
	for _, list := range lists {
		for _, d := range list {
			if !seen[d] {
				seen[d] = true
				merged = append(merged, d)
			}
		}
	}
	return merged
}

// canSearchDir reports whether a process running as uid/gid could search
// (execute into) a directory with the given info, computed purely from its
// mode and owning uid/gid — never by actually attempting the traversal as
// that user. Matches the owner/group/other bit in the same order the
// kernel's own permission check does: owner bit if uid owns the directory,
// else group bit if gid matches its group, else the other bit.
func canSearchDir(info fs.FileInfo, uid, gid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	mode := info.Mode()
	switch {
	case stat.Uid == uid:
		return mode&0o100 != 0
	case stat.Gid == gid:
		return mode&0o010 != 0
	default:
		return mode&0o001 != 0
	}
}

// homeOwnedAndWritable reports whether $HOME (info) is owned by uid and
// both writable and traversable (searchable) by its owner. Substrate
// always starts the actor with a single scion user and no legitimate
// secondary group write case, so this only ever checks the owner bits — a
// $HOME merely group- or other-writable would be a misconfiguration worth
// failing closed on, not a case to accept. Both write and execute are
// required (0o300): a directory can be "writable" (create/delete entries
// within it) yet still untraversable without its own execute bit, and
// checking write alone would pass a $HOME the scion user still can't
// reach, since canSearchDir's own checks only cover $HOME's parents, not
// $HOME itself.
func homeOwnedAndWritable(info fs.FileInfo, uid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return false
	}
	return info.Mode()&0o300 == 0o300
}
