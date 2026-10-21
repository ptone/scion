// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package privatefs creates, verifies and replaces private files: files
// that only the current user may read, such as tokens, credentials and
// per-user state.
//
// There are two implementations behind one API:
//
//   - privatefs_unix.go is today's POSIX call sequence (O_NOFOLLOW opens,
//     0600/0700 modes, the Perm&077 and owner-uid checks), moved here
//     unchanged.
//   - privatefs_windows.go is separate Windows code. It sets no mode bits
//     and calls no Chmod: on Windows, Chmod only toggles the read-only
//     attribute and protects nothing. A private file is created with a
//     protected, owner-only DACL in its security descriptor at creation
//     (never create-then-tighten). Privacy is verified on the open handle
//     by reading its DACL. Every component below a Tree's anchor is
//     opened relative to its parent handle, and any reparse point
//     (symlink, junction or other tag) is refused, never followed.
//
// ForMode is the only translation of POSIX mode bits into a Protection.
package privatefs

import (
	"errors"
	"io/fs"

	"github.com/GoogleCloudPlatform/scion/pkg/util/pathcanon"
)

// Protection is what a new file or directory gets.
type Protection int

const (
	// Inherit gives a new object the default protection. On unix that is
	// mode 0666 for files and 0777 for directories, less the umask. On
	// Windows it is the DACL inherited from the parent directory. Never
	// use it for a secret.
	Inherit Protection = iota
	// Private gives a new object owner-only access. On unix that is mode
	// 0600 for files and 0700 for directories. On Windows it is a
	// protected DACL granting full access to the current user and SYSTEM
	// only, with the current user set explicitly as the owner, applied in
	// the create call itself.
	Private
)

// ForMode is the ONLY translation of POSIX mode bits to a Protection: a
// mode granting nothing to group or others is Private, anything else is
// Inherit.
func ForMode(m fs.FileMode) Protection {
	if m&0o077 == 0 {
		return Private
	}
	return Inherit
}

// ErrReparsePoint is returned (wrapped) when a component at or below a
// Tree's anchor is a symlink, a junction or any other reparse point. On
// Windows such components are refused, never followed. On unix a
// symlinked leaf is refused through O_NOFOLLOW.
var ErrReparsePoint = errors.New("symlink, junction or other reparse point refused inside a private tree")

// ErrNotPrivate is returned (wrapped) by CheckPrivate, CheckNoForeignWriters
// and the reading operations when a file or directory is accessible to
// someone other than the current user (and, on Windows, SYSTEM and
// Administrators), or is owned by someone else.
var ErrNotPrivate = errors.New("not private")

// ErrNoACLs is returned (wrapped) when a Private object is to be created on
// a volume that cannot store access control lists (FAT, exFAT, some network
// shares), so the object could not be protected. There is no fallback.
var ErrNoACLs = errors.New("the volume does not support access control lists")

// TreeOptions configure OpenTree.
type TreeOptions struct {
	// CreateAnchor creates the anchor and any missing parent directories.
	// Created directories get Private protection; existing ones are left
	// as they are.
	CreateAnchor bool
}

// splitRel validates a path relative to a Tree's anchor and returns its
// components. rel is never absolute, and each component must pass
// pathcanon.ValidName, which also rejects "", "." and "..".
func splitRel(rel string) ([]string, error) {
	if rel == "" {
		return nil, &fs.PathError{Op: "privatefs", Path: rel, Err: errors.New("empty relative path")}
	}
	if isAbsLike(rel) {
		return nil, &fs.PathError{Op: "privatefs", Path: rel, Err: errors.New("path must be relative to the tree's anchor")}
	}
	var comps []string
	start := 0
	for i := 0; i <= len(rel); i++ {
		if i == len(rel) || isSeparator(rel[i]) {
			comps = append(comps, rel[start:i])
			start = i + 1
		}
	}
	for _, c := range comps {
		// ValidName rejects the empty component of "a//b" or "a/".
		if err := pathcanon.ValidName(c); err != nil {
			return nil, err
		}
	}
	return comps, nil
}
