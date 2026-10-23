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

//go:build linux

package migrate

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplaceAt renames without replacing an existing target:
// renameat2(RENAME_NOREPLACE), or, where the filesystem does not support
// it (NFS), a plain renameat, which still refuses a non-empty directory.
// Callers check that the target is absent first (under the project's
// provisioning lock where one applies).
func renameNoReplaceAt(oldDir int, oldName string, newDir int, newName string) error {
	err := unix.Renameat2(oldDir, oldName, newDir, newName, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		return unix.Renameat(oldDir, oldName, newDir, newName)
	}
	return err
}
