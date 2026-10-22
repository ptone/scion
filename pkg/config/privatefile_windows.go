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

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/util/privatefs"
)

// The Windows half of the private-file split (design win-cli §3.5, rows C9,
// C10, C11). Mode bits mean nothing on Windows (an existing file's Perm
// reads 0666 or 0444), so every decision here is made on an open handle
// through privatefs: the owner SID for C9, the real DACL for C10 and C11.

// currentUserOwns reports whether the migration may act on path: true only
// when the object at path is owned by the current user's SID. It is opened
// through privatefs.Tree.OpenNoFollow anchored at its directory, so a
// reparse point at path is refused (an error: the caller skips it, as unix
// skips a symlink it finds with Lstat), and the owner is read on that same
// handle. info is not used: its Sys() carries no owner on Windows.
func currentUserOwns(path string, _ os.FileInfo) (bool, error) {
	tree, err := privatefs.OpenTree(filepath.Dir(path), privatefs.TreeOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tree.Close() }()
	f, err := tree.OpenNoFollow(filepath.Base(path))
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	return privatefs.OwnedByCurrentUser(f)
}

// sourceProtection is the protection a copy of the existing object open as
// f gets (rows C10, C11): Private when f passes privatefs.CheckPrivate,
// otherwise Inherit. Protection follows the source's real ACL, never its
// meaningless Perm.
func sourceProtection(f *os.File) privatefs.Protection {
	if privatefs.CheckPrivate(f) == nil {
		return privatefs.Private
	}
	return privatefs.Inherit
}

// mkdirLikeLegacyRoot creates the canonical root scionHome/canonicalName
// (row C10, :472) in a Tree anchored at scionHome: Private when the legacy
// root scionHome/legacyName passes CheckPrivate, else Inherit. An existing
// canonical root is left as it is.
func mkdirLikeLegacyRoot(scionHome, legacyName, canonicalName string, _ os.FileInfo) error {
	tree, err := privatefs.OpenTree(scionHome, privatefs.TreeOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tree.Close() }()
	src, err := tree.OpenDir(legacyName)
	if err != nil {
		return err
	}
	p := sourceProtection(src)
	_ = src.Close()
	return tree.MkdirAll(canonicalName, p)
}

// copySettingsBackup writes backupPath, in the directory of path, as a new
// file holding a copy of the settings file at path (row C11): Private when
// path passes CheckPrivate, else Inherit. A private source is read through
// the verified handle. A partial copy is deleted through its own handle.
func copySettingsBackup(path, backupPath string, _ os.FileInfo) error {
	tree, err := privatefs.OpenTree(filepath.Dir(path), privatefs.TreeOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tree.Close() }()
	p := privatefs.Inherit
	var data []byte
	src, err := tree.OpenPrivate(filepath.Base(path), os.O_RDONLY)
	switch {
	case err == nil:
		p = privatefs.Private
		data, err = io.ReadAll(src)
		_ = src.Close()
	case errors.Is(err, privatefs.ErrNotPrivate):
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return err
	}
	if filepath.Dir(backupPath) != filepath.Dir(path) {
		return fmt.Errorf("settings backup %s is not in the directory of %s", backupPath, path)
	}
	f, err := tree.Create(filepath.Base(backupPath), true, p)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = tree.RemoveOpen(f)
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = tree.RemoveOpen(f)
		_ = f.Close()
		return err
	}
	return f.Close()
}
