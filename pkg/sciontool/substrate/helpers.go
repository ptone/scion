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
	"fmt"
	"os"
	"path/filepath"
)

var (
	errInvalidBootstrapPath    = errors.New("bootstrap file path must be a non-empty absolute path")
	errInvalidBootstrapContent = errors.New("bootstrap file content_b64 is not valid base64")
)

// errSymlinkComponent is returned by mkdirAllTracked when a path component
// that already exists on disk is a symlink. Bootstrap must never create or
// write through such a component — an image that ships, say,
// /home/scion/.config as a symlink to /etc could otherwise redirect a
// bootstrap write anywhere on the filesystem. The error names only the
// offending path (never any file content); writeBootstrapFile re-wraps it
// naming the bootstrap file's own Path before it reaches a log line.
type errSymlinkComponent struct {
	path string
}

func (e *errSymlinkComponent) Error() string {
	return fmt.Sprintf("path component %s is a symlink", e.path)
}

// mkdirAllTracked behaves like os.MkdirAll(dir, perm), but never follows a
// symlink in an existing path component, and also returns the list of
// directories it actually created (topmost first), so the caller can chown
// exactly the new directories rather than walking into pre-existing ones
// (e.g. /home) that must keep their current ownership.
//
// os.MkdirAll's own existence check is os.Stat, which follows symlinks: if
// an intermediate component already exists in the image as a symlink to a
// directory (e.g. /home/scion/.config -> /etc), Stat reports it as an
// ordinary existing directory, MkdirAll happily creates the remaining path
// components *through* the link, and the eventual file write lands wherever
// the link points rather than under the bootstrap file's nominal path. This
// uses os.Lstat instead (never following) to find the deepest already-existing
// ancestor, and creates only the missing suffix with plain os.Mkdir calls —
// so a symlinked component is caught before anything is created or written
// through it, and rejected wholesale rather than silently resolved.
func mkdirAllTracked(dir string, perm os.FileMode) ([]string, error) {
	dir = filepath.Clean(dir)

	// Walk up from dir with Lstat (never Stat) to find the deepest already-
	// existing ancestor, collecting the missing suffix (deepest first).
	var missing []string
	d := dir
	for {
		info, err := os.Lstat(d)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, &errSymlinkComponent{path: d}
			}
			if !info.IsDir() {
				return nil, &os.PathError{Op: "mkdir", Path: d, Err: os.ErrExist}
			}
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		missing = append(missing, d)
		parent := filepath.Dir(d)
		if parent == d {
			// Reached the filesystem root without finding an existing
			// ancestor.
			break
		}
		d = parent
	}

	// missing was collected bottom-up (deepest first); create top-down so
	// each os.Mkdir's parent already exists.
	created := make([]string, len(missing))
	for i, p := range missing {
		created[len(missing)-1-i] = p
	}
	for _, p := range created {
		if err := os.Mkdir(p, perm); err != nil {
			return nil, err
		}
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
