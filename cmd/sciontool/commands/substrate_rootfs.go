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

// fixupRootfsForScion corrects two observed conditions in a Substrate
// actor's rootfs that otherwise make dropping from root to the scion user
// impossible, no matter what capabilities the actor holds or how correctly
// SCION_HOST_UID/GID get applied:
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
//
// root and home are parameters — not hardcoded to "/" and a resolved
// $HOME — purely so a test can point them at a t.TempDir() standing in for
// the real rootfs and home directory.
//
// Idempotent and quiet: when neither condition needs fixing, this makes no
// changes and logs nothing. It logs exactly one info line, with what
// changed and how long it took, when something actually did.
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

	homeChanged, err := chownTreeRootOwned(home, uid, gid)
	if err != nil {
		log.Error("fixupRootfsForScion: failed to walk %s: %v", home, err)
	}

	if rootChanged || homeChanged > 0 {
		log.Info("fixupRootfsForScion: fixed up rootfs in %s (root chmod to 0755: %v, home entries rechowned: %d)",
			time.Since(start), rootChanged, homeChanged)
	}
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
// writable by its owner. Substrate always starts the actor with a single
// scion user and no legitimate secondary group write case, so this only
// ever checks the owner bit — a $HOME merely group- or other-writable
// would be a misconfiguration worth failing closed on, not a case to
// accept.
func homeOwnedAndWritable(info fs.FileInfo, uid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return false
	}
	return info.Mode()&0o200 != 0
}
