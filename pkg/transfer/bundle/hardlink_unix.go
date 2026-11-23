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

//go:build unix

package bundle

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

// maxExcludedHardlinkScanEntries caps the total number of directory entries
// inspected under excluded directory prefixes when collecting hard-linked
// excluded regular files. This keeps precheck and export bounded even when
// large cache trees (such as .cache or go/pkg/mod/cache) are excluded.
const maxExcludedHardlinkScanEntries = 4096

type fileIdentity struct {
	dev uint64
	ino uint64
}

type excludedInodeSet map[fileIdentity]struct{}

func fileIdentityIfHardlinked(info fs.FileInfo) (fileIdentity, bool) {
	if info == nil || !info.Mode().IsRegular() {
		return fileIdentity{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink <= 1 {
		return fileIdentity{}, false
	}
	return fileIdentity{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}

// collectExcludedInodes stats each excluded prefix inside root (and walks
// excluded directory prefixes up to maxExcludedHardlinkScanEntries total
// entries without following symlinks), recording the (dev, ino) of any regular
// file with nlink > 1 so hard links to excluded secrets at non-excluded paths
// are also excluded during the walk.
func collectExcludedInodes(root *os.Root, prefixes []string) excludedInodeSet {
	if root == nil || len(prefixes) == 0 {
		return nil
	}
	var set excludedInodeSet
	record := func(info fs.FileInfo) {
		id, ok := fileIdentityIfHardlinked(info)
		if !ok {
			return
		}
		if set == nil {
			set = make(excludedInodeSet)
		}
		set[id] = struct{}{}
	}

	remaining := maxExcludedHardlinkScanEntries
	var walkDir func(rel string)
	walkDir = func(rel string) {
		if remaining <= 0 {
			return
		}
		f, err := root.OpenFile(filepath.FromSlash(rel)+string(filepath.Separator), os.O_RDONLY, 0)
		if err != nil {
			return
		}
		entries, err := f.ReadDir(-1)
		_ = f.Close()
		if err != nil {
			return
		}
		for _, e := range entries {
			if remaining <= 0 {
				return
			}
			remaining--
			mt := e.Type()
			if mt&fs.ModeSymlink != 0 {
				continue
			}
			child := path.Join(rel, e.Name())
			if e.IsDir() {
				walkDir(child)
				continue
			}
			if mt.IsRegular() {
				if info, err := e.Info(); err == nil {
					record(info)
				}
			}
		}
	}

	for _, p := range prefixes {
		if p == "" {
			continue
		}
		fi, err := root.Lstat(filepath.FromSlash(p))
		if err != nil {
			continue
		}
		if fi.Mode().IsRegular() {
			record(fi)
		} else if fi.IsDir() {
			walkDir(p)
		}
	}
	return set
}

func (s excludedInodeSet) contains(d fs.DirEntry) bool {
	if len(s) == 0 {
		return false
	}
	info, err := d.Info()
	if err != nil {
		return false
	}
	id, ok := fileIdentityIfHardlinked(info)
	if !ok {
		return false
	}
	_, found := s[id]
	return found
}
