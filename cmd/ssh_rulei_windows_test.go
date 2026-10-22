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

package cmd

// Security-lead conditions on the I1 correction (review r1 finding 1;
// brief addendum (a)-(d)): ensureSSHDir makes no location decision, so
// rule I on the resolved handles carries the property alone. These tests
// pin that down for every caller of ensureSSHDir, for the name validation
// on its path, and for the link-count rule on every ssh/vscode write leaf.
// Each refusal has its positive control in the same test.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/internal/testutil/wintest"
	"github.com/GoogleCloudPlatform/scion/pkg/tunnelclient"
	"github.com/GoogleCloudPlatform/scion/pkg/util/privatefs"
)

// foreignWritableDir returns a directory outside any home that Users may
// modify, and a function that takes that right away again.
func foreignWritableDir(t *testing.T) (string, func()) {
	t.Helper()
	d := privateHome(t)
	wintest.Icacls(t, d, "/grant", "*"+wintest.SIDUsers+":(OI)(CI)(M)")
	return d, func() {
		wintest.Icacls(t, d, "/remove:g", "*"+wintest.SIDUsers)
		setUserOnlyDirDACL(t, d)
	}
}

func vscodeUpdateFor(t *testing.T, agent string) vscodeUpdate {
	t.Helper()
	block, err := renderSSHHostBlockKnown(winSSHExe, "https://h.example", "h", "p", agent, vscodeKnownHostsRef)
	require.NoError(t, err)
	return vscodeUpdate{host: "scion.h.p." + agent, block: block,
		key: mustNormKey(t, testSSHPublicKey(t)), endpoint: tunnelclient.NormalizeEndpoint("https://h.example")}
}

// Addendum (a) and (d): for EVERY caller of ensureSSHDir, a link (junction)
// whose target directory another principal can write to is followed and
// then REFUSED by rule I, as a returned error, with nothing written there.
// Control: once the foreign right is removed, the same call succeeds.
func TestSSHDirCallers_LinkTargetFailingRuleIRefused(t *testing.T) {
	cases := []struct {
		name   string
		link   string   // relative to home; its parent is created first
		leaves []string // files the call would write in the link target
		call   func(t *testing.T, home string) error
	}{
		{"ensureSSHIncludeLine ~/.ssh", ".ssh", []string{"config"}, func(t *testing.T, home string) error {
			_, err := ensureSSHIncludeLine(sshPaths{home: home})
			return err
		}},
		{"writeSSHHubPlan ~/.scion/ssh", ".scion/ssh", []string{"known_hosts-hub"}, func(t *testing.T, home string) error {
			p := sshPaths{home: home}
			return writeSSHHubPlan(p, sshPlan(p))
		}},
		{"writeSSHHubPlan ~/.ssh/config.d", ".ssh/config.d", []string{"scion-hub"}, func(t *testing.T, home string) error {
			p := sshPaths{home: home}
			return writeSSHHubPlan(p, sshPlan(p))
		}},
		{"updateVSCodeFiles ~/.scion/vscode", ".scion/vscode", []string{"ssh_config", "known_hosts"}, func(t *testing.T, home string) error {
			_, err := updateVSCodeFiles(vscodePaths{home: home}, vscodeUpdateFor(t, "a1"))
			return err
		}},
		{"ensureVSCodeSettings ~/.scion/vscode/user-data/User", ".scion/vscode/user-data/User", []string{"settings.json"}, func(t *testing.T, home string) error {
			return ensureVSCodeSettings(vscodePaths{home: home})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := privateHome(t)
			target, clear := foreignWritableDir(t)
			link := filepath.Join(home, filepath.FromSlash(tc.link))
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
			wintest.MustJunction(t, target, link)

			err := tc.call(t, home)
			require.ErrorIs(t, err, privatefs.ErrNotPrivate, "the rule I failure is returned")
			assert.Contains(t, err.Error(), wintest.SIDUsers)
			for _, l := range tc.leaves {
				assert.NoFileExists(t, filepath.Join(target, l))
			}

			clear()
			require.NoError(t, tc.call(t, home), "control: the same link once no one else can write there")
			for _, l := range tc.leaves {
				assertPrivateSD(t, filepath.Join(target, l))
			}
		})
	}
}

// Addendum (d), foreign-owned target: ~/.ssh/config is a link to a file
// owned by another principal (a group named like Administrators, not
// S-1-5-32-544) in a directory the user controls: refused, untouched.
// Control: the same link to a user-owned file is followed and the file
// replaced by a private one.
func TestSSHWrite_LinkToForeignOwnedTargetRefused(t *testing.T) {
	home := privateHome(t)
	p := sshPaths{home: home}
	require.NoError(t, os.MkdirAll(p.sshDir(), 0o700))
	outside := privateHome(t)
	target := filepath.Join(outside, "ssh_config")
	require.NoError(t, os.WriteFile(target, []byte("Host old\n"), 0o600))
	foreign := fakeAdminsGroupSID(t)
	wintest.SetOwner(t, target, foreign)
	wintest.MustSymlink(t, target, p.userConfig())

	_, err := ensureSSHIncludeLine(p)
	require.ErrorIs(t, err, privatefs.ErrNotPrivate)
	assert.Contains(t, err.Error(), foreign)
	assert.Equal(t, "Host old\n", readTestFile(t, target))

	wintest.SetOwner(t, target, wintest.UserSID(t))
	changed, err := ensureSSHIncludeLine(p)
	require.NoError(t, err, "control: a user-owned link target")
	assert.True(t, changed)
	assertPrivateSD(t, target)
}

// Addendum (b): ValidName is load-bearing on the ensureSSHDir path. '..',
// absolute and drive forms, alternate data streams, trailing dot or space,
// mixed separators, device names and empty components are refused and
// create nothing. Control: the constant relative paths are accepted.
func TestEnsureSSHDir_RefusesInvalidComponents(t *testing.T) {
	root := privateHome(t)
	home := filepath.Join(root, "home")
	require.NoError(t, os.Mkdir(home, 0o700))
	for _, rel := range []string{
		"", "..", "../x", ".ssh/../../x", `.ssh\..\..\x`, `.ssh/..\x`,
		`C:\x`, `C:x`, `\x`, "/x", `\\server\share\x`, `\\?\C:\x`,
		".ssh:evil", ".ssh::$DATA", ".ssh/config.d:stream",
		".ssh.", ".ssh ", ".ssh/config.d.", ".ssh//config.d",
		"CON", "nul.txt", ".ssh/COM1", "LPT1.log",
	} {
		err := ensureSSHDir(home, rel)
		assert.Error(t, err, "%q must be refused", rel)
	}
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1, "nothing was created next to home: %v", entries)
	entries, err = os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was created in home")

	for _, rel := range []string{sshDirRel, sshIncludeDirRel, sshKnownHostsDirRel, vscodeDirRel, vscodeSettingsDirRel} {
		require.NoError(t, ensureSSHDir(home, rel), "control: %s", rel)
		assertPrivateSD(t, filepath.Join(home, filepath.FromSlash(rel)))
	}
}

// Addendum (b), 8.3 short names: ValidName accepts them by design (an 8.3
// name is a legal component; aliasing is pathcanon.Resolve's concern). On
// this path a component is opened relative to its parent's handle, so a
// short name can only name a CHILD of that same directory: it creates
// inside the tree, never outside it.
func TestEnsureSSHDir_ShortNameStaysInside(t *testing.T) {
	root := privateHome(t)
	home := filepath.Join(root, "home")
	long := filepath.Join(home, "longdirectoryname-8dot3")
	require.NoError(t, os.MkdirAll(long, 0o700))
	short := filepath.Base(wintest.ShortPath(t, long))
	require.Contains(t, short, "~", "the test volume must generate 8.3 names (the job enables them)")
	require.NoError(t, ensureSSHDir(home, short+"/sub"))
	assert.DirExists(t, filepath.Join(long, "sub"), "the short name named the same child")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "nothing outside home")
}

// Addendum (c): every ssh-config and vscode write leaf with a second name
// (a hard link) is REFUSED, never repaired, and its other name is left
// untouched. Control: once the second name is gone, the same writer
// succeeds and the leaf is private.
func TestSSHVSCodeWriteLeaves_RefuseHardLinks(t *testing.T) {
	ssh := func(t *testing.T, home string) error {
		p := sshPaths{home: home}
		_, err := regenerateSSHHubFiles(p, regenTestEndpoint, winSSHExe, regenListing(
			sshListedAgent{"proj", "agent", testSSHPublicKey(t)}))
		return err
	}
	vscode := func(t *testing.T, home string) error {
		_, err := updateVSCodeFiles(vscodePaths{home: home}, vscodeUpdateFor(t, "a1"))
		return err
	}
	for _, tc := range []struct {
		leaf  string // relative to home
		write func(t *testing.T, home string) error
	}{
		{".ssh/config", ssh},
		{".ssh/config.d/scion-hub", ssh},
		{".scion/ssh/known_hosts-hub", ssh},
		{".scion/vscode/ssh_config", vscode},
		{".scion/vscode/known_hosts", vscode},
		{".scion/vscode/user-data/User/settings.json", vscode},
	} {
		t.Run(strings.ReplaceAll(tc.leaf, "/", "_"), func(t *testing.T) {
			home := privateHome(t)
			leaf := filepath.Join(home, filepath.FromSlash(tc.leaf))
			require.NoError(t, os.MkdirAll(filepath.Dir(leaf), 0o700))
			require.NoError(t, os.WriteFile(leaf, nil, 0o600))
			second := filepath.Join(home, "second-name")
			require.NoError(t, os.Link(leaf, second))

			err := tc.write(t, home)
			require.ErrorIs(t, err, privatefs.ErrHardLinked)
			assert.Empty(t, readTestFile(t, second), "the other name is untouched")

			require.NoError(t, os.Remove(second))
			require.NoError(t, tc.write(t, home), "control: one name")
			assertPrivateSD(t, leaf)
		})
	}
}
