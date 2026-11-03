//go:build linux

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

package agent

import "golang.org/x/sys/unix"

// renameNoReplace renames from to to under dirFd, failing with EEXIST when
// to exists. Filesystems without RENAME_NOREPLACE (many NFS clients) fail
// with EINVAL, and the caller falls back (renameIntoPlace).
func renameNoReplace(dirFd int, from, to string) error {
	return unix.Renameat2(dirFd, from, dirFd, to, unix.RENAME_NOREPLACE)
}
