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

package hub

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// The harness-config bootstrap and import read a directory's config.yaml
// before the directory is collected: to pick the harness for the store, to
// bind the import name, and to check the provisioner. These reads follow the
// collection's rules, so they never see content the collection would not
// record: the directory must be a real directory (not a link), each file is
// read only if it is a regular file (Lstat, so a link reads as absent), and
// it is read through an os.Root opened at the directory, so nothing outside
// the directory is opened.

// errNotRegularLocalFile reports a harness-config file that is a link or not
// a regular file. It wraps fs.ErrNotExist: such a file reads as absent, as it
// does in the collected set.
var errNotRegularLocalFile = fmt.Errorf("a link or not a regular file: %w", fs.ErrNotExist)

// openLocalHarnessConfigDir opens an os.Root at dir after checking that dir
// is a real directory (not a link) and that the root is that directory.
func openLocalHarnessConfigDir(dir string) (*os.Root, error) {
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("harness-config directory not found: %w", err)
	}
	if before.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("harness-config directory %s is a link", dir)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("harness-config path is not a directory: %s", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	at, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(before, at) {
		_ = root.Close()
		return nil, fmt.Errorf("harness-config directory %s changed while it was opened", dir)
	}
	return root, nil
}

// readLocalHarnessConfigRootFile reads the top-level file name through root.
// A file that is a link or not a regular file reads as absent
// (errNotRegularLocalFile), and so does one replaced after the Lstat.
func readLocalHarnessConfigRootFile(root *os.Root, name string) ([]byte, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", name, errNotRegularLocalFile)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return nil, fmt.Errorf("%s: %w", name, errNotRegularLocalFile)
	}
	return io.ReadAll(f)
}

// readLocalHarnessConfigFile reads the top-level file name of the
// harness-config directory dir, confined as described above.
func readLocalHarnessConfigFile(dir, name string) ([]byte, error) {
	root, err := openLocalHarnessConfigDir(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readLocalHarnessConfigRootFile(root, name)
}

// loadLocalHarnessConfigDir is config.LoadHarnessConfigDir for the reads
// before collection: the same validation, sidecar handling and name rules,
// over config.yaml and the sidecar read confined to dir. A config.yaml that
// is a link or not a regular file fails the load.
func loadLocalHarnessConfigDir(dir string) (*config.HarnessConfigDir, error) {
	absPath, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}
	root, err := openLocalHarnessConfigDir(absPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return config.LoadHarnessConfigFromFiles(filepath.Base(absPath), absPath, func(name string) ([]byte, error) {
		return readLocalHarnessConfigRootFile(root, name)
	})
}
