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

package dirfd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withUmask002 runs the test under umask 002, as sciontool does for nfs
// shared-dir writers (ptone/scion#3155), restoring the previous umask.
func withUmask002(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o002)
	t.Cleanup(func() { syscall.Umask(old) })
}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

// Directories this package creates keep the requested mode under umask 002,
// so a staged secret's ~/.ssh is never group-writable.
func TestCreatedDirsIgnoreUmask002(t *testing.T) {
	withUmask002(t)
	root := t.TempDir()

	f, err := EnsureDirNoFollow(filepath.Join(root, "a"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := modeOf(t, filepath.Join(root, "a")).Perm(); got != 0o755 {
		t.Errorf("EnsureDirNoFollow mode = %o, want 755", got)
	}

	fd, under, err := EnsureDirNoFollowUnderRoot(root, filepath.Join(root, ".ssh", "keys"), 0o700, 0, 0)
	if err != nil || !under {
		t.Fatalf("EnsureDirNoFollowUnderRoot: under=%v err=%v", under, err)
	}
	_ = syscall.Close(fd)
	for _, p := range []string{".ssh", filepath.Join(".ssh", "keys")} {
		if got := modeOf(t, filepath.Join(root, p)).Perm(); got != 0o700 {
			t.Errorf("EnsureDirNoFollowUnderRoot %s mode = %o, want 700", p, got)
		}
	}

	tfd, err := EnsureDirTrustedAncestorFollow(filepath.Join(root, "t", "u"))
	if err != nil {
		t.Fatal(err)
	}
	_ = syscall.Close(tfd)
	if got := modeOf(t, filepath.Join(root, "t", "u")).Perm(); got != 0o755 {
		t.Errorf("EnsureDirTrustedAncestorFollow mode = %o, want 755", got)
	}
}

// A setgid bit inherited from the parent is kept; a pre-existing directory
// is never changed.
func TestCreatedDirsKeepInheritedSetgid(t *testing.T) {
	withUmask002(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o775|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	pre := filepath.Join(root, "pre")
	if err := os.Mkdir(pre, 0o777); err != nil { // 0775 after umask 002
		t.Fatal(err)
	}

	f, err := EnsureDirNoFollow(filepath.Join(root, "new"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	m := modeOf(t, filepath.Join(root, "new"))
	if m&os.ModeSetgid == 0 || m.Perm() != 0o755 {
		t.Errorf("new dir mode = %v, want setgid kept and perm 755", m)
	}

	f, err = EnsureDirNoFollow(pre, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := modeOf(t, pre).Perm(); got != 0o775 {
		t.Errorf("pre-existing dir mode = %o, want unchanged 775", got)
	}
}
