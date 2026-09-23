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

package substrate

import (
	"errors"
	"os"
	"path/filepath"
)

var (
	errInvalidBootstrapPath    = errors.New("bootstrap file path must be a non-empty absolute path")
	errInvalidBootstrapContent = errors.New("bootstrap file content_b64 is not valid base64")
)

// mkdirAllTracked behaves like os.MkdirAll(dir, perm), but also returns the
// list of directories it actually created (topmost first), so the caller
// can chown exactly the new directories rather than walking into
// pre-existing ones (e.g. /home) that must keep their current ownership.
func mkdirAllTracked(dir string, perm os.FileMode) ([]string, error) {
	var toCreate []string
	d := filepath.Clean(dir)
	for {
		info, err := os.Stat(d)
		if err == nil {
			if !info.IsDir() {
				return nil, &os.PathError{Op: "mkdir", Path: d, Err: os.ErrExist}
			}
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		toCreate = append(toCreate, d)
		parent := filepath.Dir(d)
		if parent == d {
			// Reached the filesystem root without finding an existing
			// ancestor; let MkdirAll below surface the real error.
			break
		}
		d = parent
	}

	if err := os.MkdirAll(dir, perm); err != nil {
		return nil, err
	}

	// toCreate was collected bottom-up (deepest first); return top-down so
	// callers chown parents before children.
	created := make([]string, len(toCreate))
	for i, p := range toCreate {
		created[len(toCreate)-1-i] = p
	}
	return created, nil
}

// redactErr returns err's message unless it looks like it might carry file
// contents (which could be secret material). Bootstrap file errors here are
// always structural (bad path, bad base64, mkdir/write failure) and never
// include content_b64 or decoded bytes, so this currently just documents
// that invariant rather than performing scrubbing. Kept as a named seam so
// future error paths added here are reviewed against the same "no secrets
// in logs or errors" requirement (phase1-spec.md §2.1).
func redactErr(err error) error { return err }
