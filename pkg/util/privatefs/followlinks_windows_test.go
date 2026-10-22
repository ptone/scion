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

package privatefs

// Phase 3b additions: TreeOptions.FollowLinks, the addendum A1 link-count
// refusal and ReplaceFollowing (rule I). Each refusal has its positive
// control in the same test.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/internal/testutil/wintest"
)

func followTree(t *testing.T, dir string) *Tree {
	t.Helper()
	tr, err := OpenTree(dir, TreeOptions{FollowLinks: true})
	if err != nil {
		t.Fatalf("OpenTree(%s, FollowLinks): %v", dir, err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// FollowLinks follows a junction, an absolute symlink and a relative
// symlink at a directory component (the dotfile-manager layouts), and the
// directory it creates through them is private. Control (the refusal side):
// the same walk in a tree without FollowLinks is refused.
func TestTree_FollowLinksDirectoryComponents(t *testing.T) {
	for _, kind := range []string{"junction", "symlink", "relative-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			anchor := filepath.Join(root, "home")
			real := filepath.Join(root, "dotfiles", "ssh")
			for _, d := range []string{anchor, real} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(anchor, ".ssh")
			switch kind {
			case "junction":
				wintest.MustJunction(t, real, link)
			case "symlink":
				wintest.MustSymlink(t, real, link)
			default:
				wintest.MustSymlink(t, filepath.Join("..", "dotfiles", "ssh"), link)
			}

			if err := treeAt(t, anchor).MkdirAll(".ssh/config.d", Private); !errors.Is(err, ErrReparsePoint) {
				t.Fatalf("without FollowLinks: %v, want ErrReparsePoint", err)
			}
			tr := followTree(t, anchor)
			if err := tr.MkdirAll(".ssh/config.d", Private); err != nil {
				t.Fatalf("FollowLinks MkdirAll through a %s: %v", kind, err)
			}
			if fi, err := os.Stat(filepath.Join(real, "config.d")); err != nil || !fi.IsDir() {
				t.Fatalf("config.d not created in the link's target: %v", err)
			}
			assertPrivateSD(t, filepath.Join(real, "config.d"))
			// The link's own leaf (".ssh" as the last component) is
			// followed too, and existing directories are left as they are.
			if err := tr.MkdirAll(".ssh", Private); err != nil {
				t.Fatalf("FollowLinks MkdirAll of the link itself: %v", err)
			}
		})
	}
}

// FollowLinks never follows a leaf FILE: a symlink at the leaf is still
// refused by Create. Control: a regular leaf in the same followed
// directory is created.
func TestTree_FollowLinksLeafFileStillRefused(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor := filepath.Join(root, "a")
	if err := os.MkdirAll(anchor, 0o700); err != nil {
		t.Fatal(err)
	}
	wintest.MustSymlink(t, outside, filepath.Join(anchor, "leaf"))
	tr := followTree(t, anchor)
	if _, err := tr.Create("leaf", false, Private); !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("Create of a symlinked leaf with FollowLinks: %v, want ErrReparsePoint", err)
	}
	f, err := tr.Create("ok", true, Private)
	if err != nil {
		t.Fatalf("control: regular leaf: %v", err)
	}
	_ = f.Close()
}

// Addendum A1: an existing leaf opened for writing with more than one name
// is refused, never repaired: OpenPrivate for writing and a non-exclusive
// Create. Control: an ordinary leaf with the same DACL passes both, and a
// read-only OpenPrivate of the linked file (no write) is not affected.
func TestWriteLeaf_RefusesHardLink(t *testing.T) {
	root := t.TempDir()
	tr := treeAt(t, root)
	linked := mustCreate(t, tr, "linked", Private, "x")
	other := filepath.Join(t.TempDir(), "second-name")
	if err := os.Link(linked, other); err != nil {
		t.Fatalf("hard link: %v", err)
	}
	ordinary := mustCreate(t, tr, "ordinary", Private, "x")
	dacl := privateDACL(wintest.UserSID(t))
	wintest.SetDACL(t, linked, dacl)
	wintest.SetDACL(t, ordinary, dacl)

	if _, err := tr.OpenPrivate("linked", os.O_WRONLY); !errors.Is(err, ErrHardLinked) {
		t.Fatalf("OpenPrivate(write) of a hard-linked file: %v, want ErrHardLinked", err)
	}
	if _, err := tr.OpenPrivate("linked", os.O_RDWR|os.O_TRUNC); !errors.Is(err, ErrHardLinked) {
		t.Fatalf("OpenPrivate(rdwr) of a hard-linked file: %v, want ErrHardLinked", err)
	}
	if _, err := tr.Create("linked", false, Private); !errors.Is(err, ErrHardLinked) {
		t.Fatalf("Create of a hard-linked file: %v, want ErrHardLinked", err)
	}
	if _, err := tr.Create("linked", false, Inherit); !errors.Is(err, ErrHardLinked) {
		t.Fatalf("Create(Inherit) of a hard-linked file: %v, want ErrHardLinked", err)
	}
	if b, err := os.ReadFile(other); err != nil || string(b) != "x" {
		t.Fatalf("the second name's content changed: %q, %v", b, err)
	}

	for _, flag := range []int{os.O_WRONLY, os.O_RDWR | os.O_TRUNC} {
		f, err := tr.OpenPrivate("ordinary", flag)
		if err != nil {
			t.Fatalf("control: OpenPrivate(%#x) of an ordinary file with the same DACL: %v", flag, err)
		}
		_ = f.Close()
	}
	f, err := tr.Create("ordinary", false, Private)
	if err != nil {
		t.Fatalf("control: Create of an ordinary file: %v", err)
	}
	_ = f.Close()
	if _, err := tr.ReadPrivate("linked", 0); err != nil {
		t.Fatalf("read-only access to the linked file: %v", err)
	}
}

// replaceDir returns a directory whose DACL is the user's alone (no
// foreign writer), like a default profile directory.
func replaceDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	u := wintest.UserSID(t)
	wintest.SetDACL(t, d, fmt.Sprintf("D:P(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)", u, wintest.SIDSystem, wintest.SIDAdministrators))
	return d
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Rule I: a new file is private; a user-owned too-open file is replaced by
// a private one (the old descriptor is not copied).
func TestReplaceFollowing_NewPrivateAndRepairsOwned(t *testing.T) {
	dir := replaceDir(t)
	p := filepath.Join(dir, "config")
	if err := ReplaceFollowing(p, []byte("one"), 0o644, ".tmp-"); err != nil {
		t.Fatalf("new file: %v", err)
	}
	assertPrivateSD(t, p)
	u := wintest.UserSID(t)
	wintest.SetDACL(t, p, fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;%s)", u, wintest.SIDUsers))
	if err := ReplaceFollowing(p, []byte("two"), 0o644, ".tmp-"); err != nil {
		t.Fatalf("user-owned too-open file: %v", err)
	}
	assertPrivateSD(t, p)
	if got := readString(t, p); got != "two" {
		t.Fatalf("content %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

// Rule I: a symlinked leaf is followed; the target is replaced (private)
// and the link is kept.
func TestReplaceFollowing_FollowsSymlinkLeaf(t *testing.T) {
	dir := replaceDir(t)
	real := filepath.Join(dir, "real-config")
	if err := os.WriteFile(real, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config")
	wintest.MustSymlink(t, real, link)
	if err := ReplaceFollowing(link, []byte("new"), 0o600, ".tmp-"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was not kept: %v %v", fi, err)
	}
	if got := readString(t, real); got != "new" {
		t.Fatalf("target content %q", got)
	}
	assertPrivateSD(t, real)
}

// Rule I refuse + control: a parent directory another principal can write
// to is refused (nothing written); without the foreign ACE it works.
func TestReplaceFollowing_RefusesForeignWritableParent(t *testing.T) {
	dir := replaceDir(t)
	p := filepath.Join(dir, "config")
	wintest.Icacls(t, dir, "/grant", "*"+wintest.SIDUsers+":(M)")
	err := ReplaceFollowing(p, []byte("x"), 0o600, ".tmp-")
	if !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("foreign-writable parent: %v, want ErrNotPrivate", err)
	}
	if _, serr := os.Lstat(p); !os.IsNotExist(serr) {
		t.Fatalf("a file was written: %v", serr)
	}
	wintest.Icacls(t, dir, "/remove:g", "*"+wintest.SIDUsers)
	if err := ReplaceFollowing(p, []byte("x"), 0o600, ".tmp-"); err != nil {
		t.Fatalf("control: %v", err)
	}
}

// Rule I refuse + control: a target owned by someone else is refused and
// left untouched; a user-owned target with the same DACL is replaced.
func TestReplaceFollowing_RefusesForeignOwnedTarget(t *testing.T) {
	dir := replaceDir(t)
	u := wintest.UserSID(t)
	open := fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FR;;;%s)", u, wintest.SIDUsers)
	foreign := filepath.Join(dir, "foreign")
	if err := os.WriteFile(foreign, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	wintest.SetDACL(t, foreign, open)
	wintest.SetOwner(t, foreign, wintest.SIDSystem)
	if err := ReplaceFollowing(foreign, []byte("new"), 0o600, ".tmp-"); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("foreign-owned target: %v, want ErrNotPrivate", err)
	}
	if got := readString(t, foreign); got != "old" {
		t.Fatalf("the foreign file changed: %q", got)
	}
	owned := filepath.Join(dir, "owned")
	if err := os.WriteFile(owned, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	wintest.SetDACL(t, owned, open)
	if err := ReplaceFollowing(owned, []byte("new"), 0o600, ".tmp-"); err != nil {
		t.Fatalf("control: user-owned target: %v", err)
	}
	assertPrivateSD(t, owned)
}

// Addendum A1 on the relaxed path: a hard-linked target is refused (not
// replaced); an ordinary target with the same DACL is replaced.
func TestReplaceFollowing_RefusesHardLinkedTarget(t *testing.T) {
	dir := replaceDir(t)
	linked := filepath.Join(dir, "linked")
	ordinary := filepath.Join(dir, "ordinary")
	for _, p := range []string{linked, ordinary} {
		if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		wintest.SetDACL(t, p, privateDACL(wintest.UserSID(t)))
	}
	if err := os.Link(linked, filepath.Join(t.TempDir(), "second")); err != nil {
		t.Fatalf("hard link: %v", err)
	}
	if err := ReplaceFollowing(linked, []byte("new"), 0o600, ".tmp-"); !errors.Is(err, ErrHardLinked) {
		t.Fatalf("hard-linked target: %v, want ErrHardLinked", err)
	}
	if got := readString(t, linked); got != "old" {
		t.Fatalf("the hard-linked file changed: %q", got)
	}
	if err := ReplaceFollowing(ordinary, []byte("new"), 0o600, ".tmp-"); err != nil {
		t.Fatalf("control: ordinary target: %v", err)
	}
}
