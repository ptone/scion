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

//go:build windows

package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/internal/testutil/wintest"
	"github.com/GoogleCloudPlatform/scion/pkg/util/privatefs"
)

// mustAcquireRegular is the positive control of the refusal tests: Acquire
// on a regular path in the same directory succeeds.
func mustAcquireRegular(t *testing.T, dir string) {
	t.Helper()
	l, err := Acquire(filepath.Join(dir, "regular.lock"), 0600, 0)
	if err != nil {
		t.Fatalf("control: Acquire of a regular lock file: %v", err)
	}
	l.Unlock()
}

func TestAcquire_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.lock")
	wintest.MustSymlink(t, target, link)
	if _, err := Acquire(link, 0600, 0); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("symlink: err = %v, want ErrNotRegular", err)
	}
	// A dangling symlink is refused too, and nothing is created behind it.
	dangling := filepath.Join(dir, "dangling.lock")
	wintest.MustSymlink(t, filepath.Join(dir, "absent"), dangling)
	if _, err := Acquire(dangling, 0600, 0); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("dangling symlink: err = %v, want ErrNotRegular", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "absent")); !os.IsNotExist(err) {
		t.Fatalf("Acquire created the dangling symlink's target: %v", err)
	}
	mustAcquireRegular(t, dir)
}

func TestAcquire_RefusesJunction(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	j := filepath.Join(dir, "junction.lock")
	wintest.MustJunction(t, target, j)
	if _, err := Acquire(j, 0600, 0); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("junction: err = %v, want ErrNotRegular", err)
	}
	if _, err := AcquireExisting(j, 0); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("junction (AcquireExisting): err = %v, want ErrNotRegular", err)
	}
	mustAcquireRegular(t, dir)
}

func TestAcquire_RefusesDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub.lock")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(sub, 0600, 0); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("directory: err = %v, want ErrNotRegular", err)
	}
	mustAcquireRegular(t, dir)
}

// Security lead ruling H: Acquire's handle does not share delete access,
// so a held lock file cannot be deleted (and re-created and locked by a
// third process).
func TestAcquire_HeldFileCannotBeDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	l, err := Acquire(path, 0600, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err == nil {
		t.Fatal("a held lock file was deleted")
	}
	if err := os.Rename(path, path+".moved"); err == nil {
		t.Fatal("a held lock file was renamed")
	}
	l.Unlock()
	// Control: the delete itself works once the handle is closed.
	if err := os.Remove(path); err != nil {
		t.Fatalf("delete after Unlock: %v", err)
	}
}

func TestAcquire_PrivateSDForMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	l, err := Acquire(path, 0600, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	user := wintest.UserSID(t)
	sd := wintest.ReadSD(t, path)
	if sd.Owner != user || !sd.Protected || len(sd.ACEs) != 2 || !sd.HasAllowACE(user) || !sd.HasAllowACE(wintest.SIDSystem) {
		t.Fatalf("lock file SD = %+v; want owner %s, protected, allow {user, SYSTEM} only", sd, user)
	}
}

func TestAcquire_InheritForMode0644(t *testing.T) {
	dir := t.TempDir()
	// The parent grants Users read, inheritably: an Inherit lock file
	// carries that ACE, a private one (0600, the control) does not.
	wintest.Icacls(t, dir, "/grant", "*"+wintest.SIDUsers+":(OI)(CI)R")
	path := filepath.Join(dir, "x.lock")
	l, err := Acquire(path, 0644, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	if sd := wintest.ReadSD(t, path); sd.Protected || !sd.HasAllowACE(wintest.SIDUsers) {
		t.Fatalf("0644 lock file SD = %+v; want the inherited Users ACE, not protected", sd)
	}
	priv := filepath.Join(dir, "p.lock")
	lp, err := Acquire(priv, 0600, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lp.Unlock()
	if sd := wintest.ReadSD(t, priv); !sd.Protected || sd.HasAllowACE(wintest.SIDUsers) {
		t.Fatalf("control: 0600 lock file SD = %+v; want protected, no Users ACE", sd)
	}
}

// The tunnelclient pattern: a file created with delete sharing is locked
// with LockFile and renamed while held, and the lock stays held under the
// new name.
func TestLockFile_ShareDeleteRename(t *testing.T) {
	dir := t.TempDir()
	tr, err := privatefs.OpenTree(dir, privatefs.TreeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()
	f, err := tr.Create(".x.lock.tmp", true, privatefs.Private)
	if err != nil {
		t.Fatal(err)
	}
	l, err := LockFile(f, 0)
	if err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	defer l.Unlock()
	if err := tr.Rename(".x.lock.tmp", "x.lock"); err != nil {
		t.Fatalf("rename while held: %v", err)
	}
	final := filepath.Join(dir, "x.lock")
	if _, err := AcquireExisting(final, 20*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("probe of the renamed held lock: %v, want ErrTimeout", err)
	}
	// The owner can remove its own name while holding the lock.
	if err := tr.Remove("x.lock"); err != nil {
		t.Fatalf("remove while held: %v", err)
	}
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		t.Fatalf("lock file after Remove: %v", err)
	}
}
