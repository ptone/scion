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

// Windows file-level tests of the ssh-config, ssh and vscode write paths
// (design §3.5 I1-I4, §4.2; security lead ruling I; checklist addendum
// A1). They drive the real command code (writeSSHHubPlan,
// regenerateSSHHubFiles, updateVSCodeFiles, the ssh known-hosts writer)
// into a temporary home. Every refusal has its positive control in the
// same test (security checklist §5).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/GoogleCloudPlatform/scion/internal/testutil/wintest"
	"github.com/GoogleCloudPlatform/scion/pkg/tunnelclient"
	"github.com/GoogleCloudPlatform/scion/pkg/util/privatefs"
)

// winSSHExe is a Windows scion path with a space, so the generated
// ProxyCommand exercises sshCmdQuote.
const winSSHExe = `C:\Program Files\Scion\scion.exe`

// privateHome returns a temporary home whose DACL is what a profile
// directory has by default: the user, SYSTEM and Administrators only
// (no foreign writer), inherited by everything created inside.
func privateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	setUserOnlyDirDACL(t, home)
	return home
}

func setUserOnlyDirDACL(t *testing.T, dir string) {
	t.Helper()
	u := wintest.UserSID(t)
	wintest.SetDACL(t, dir, fmt.Sprintf("D:P(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)", u, wintest.SIDSystem, wintest.SIDAdministrators))
}

// assertPrivateSD: owner the user, protected DACL granting FILE_ALL_ACCESS
// to exactly the user and SYSTEM (privatefs's private descriptor). A file
// that merely inherited privateHome's DACL fails (it has Administrators).
func assertPrivateSD(t *testing.T, path string) {
	t.Helper()
	user := wintest.UserSID(t)
	sd := wintest.ReadSD(t, path)
	require.Equal(t, user, sd.Owner, "%s: owner", path)
	require.True(t, sd.Protected, "%s: DACL not protected", path)
	require.Len(t, sd.ACEs, 2, "%s: ACEs %+v, want exactly {user, SYSTEM}", path, sd.ACEs)
	seen := map[string]bool{}
	for _, a := range sd.ACEs {
		require.Equal(t, uint8(windows.ACCESS_ALLOWED_ACE_TYPE), a.Type, "%s: ACE %+v", path, a)
		require.Equal(t, uint32(wintest.FileAllAccess), a.Mask, "%s: ACE %+v", path, a)
		seen[a.SID] = true
	}
	require.True(t, seen[user] && seen[wintest.SIDSystem], "%s: ACEs %+v", path, sd.ACEs)
}

// sshPlan is a planned hub write with fixed content, for writeSSHHubPlan.
func sshPlan(p sshPaths) *sshRegenResult {
	return &sshRegenResult{
		label:          "hub",
		includeFile:    p.includeFile("hub"),
		knownHostsFile: p.knownHostsFile("hub"),
		includeContent: "# include\n",
		knownContent:   "# known hosts\n",
	}
}

// I1-I3: a fresh ssh-config run creates ~/.ssh, ~/.ssh/config.d and
// ~/.scion/ssh private, and writes ~/.ssh/config, the include file and the
// known-hosts file with the private descriptor. The generated ProxyCommand
// uses the Windows quoting for a scion path with a space (sshCmdQuote).
// Control: a file created plainly in the same home does not have the
// private descriptor, so the assertion is not vacuous.
func TestSSHWrite_NewFilePrivate(t *testing.T) {
	home := privateHome(t)
	p := sshPaths{home: home}
	res, err := regenerateSSHHubFiles(p, regenTestEndpoint, winSSHExe, regenListing(
		sshListedAgent{"proj", "agent", testSSHPublicKey(t)}))
	require.NoError(t, err)
	for _, d := range []string{p.sshDir(), p.includeDir(), p.knownHostsDir()} {
		assertPrivateSD(t, d)
	}
	for _, f := range []string{p.userConfig(), res.includeFile, res.knownHostsFile} {
		assertPrivateSD(t, f)
	}
	inc := readTestFile(t, res.includeFile)
	assert.Contains(t, inc, `ProxyCommand "C:/Program Files/Scion/scion.exe" tunnel --stdio --ssh`)
	assert.Contains(t, readTestFile(t, p.userConfig()), sshIncludeLine)

	plain := filepath.Join(home, "plain")
	require.NoError(t, os.WriteFile(plain, []byte("x"), 0o600))
	assert.True(t, wintest.ReadSD(t, plain).HasAllowACE(wintest.SIDAdministrators), "control: an inheriting file differs from the private descriptor")
}

// I1/I2, rule I: ~/.ssh is a dotfile-manager link (junction, absolute and
// relative symlink) to a directory elsewhere, and ~/.ssh/config is itself
// a symlink to a file the user owns but left readable and writable by
// Users. The links are followed and kept; config.d is created private in
// the link's target; the config target is replaced by a private file (the
// old descriptor is not copied) holding the Include line.
func TestSSHWrite_FollowsUserSymlinkAndRepairsOpenTarget(t *testing.T) {
	for _, kind := range []string{"junction", "symlink", "relative-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := privateHome(t)
			dot := filepath.Join(home, "dotfiles", "ssh")
			require.NoError(t, os.MkdirAll(dot, 0o700))
			switch kind {
			case "junction":
				wintest.MustJunction(t, dot, filepath.Join(home, ".ssh"))
			case "symlink":
				wintest.MustSymlink(t, dot, filepath.Join(home, ".ssh"))
			default:
				wintest.MustSymlink(t, filepath.Join("dotfiles", "ssh"), filepath.Join(home, ".ssh"))
			}
			target := filepath.Join(home, "dotfiles", "ssh_config")
			require.NoError(t, os.WriteFile(target, []byte("Host old\n"), 0o600))
			u := wintest.UserSID(t)
			wintest.SetDACL(t, target, fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;%s)", u, wintest.SIDUsers))
			wintest.MustSymlink(t, target, filepath.Join(dot, "config"))

			p := sshPaths{home: home}
			require.NoError(t, writeSSHHubPlan(p, sshPlan(p)))

			fi, err := os.Lstat(filepath.Join(dot, "config"))
			require.NoError(t, err)
			assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the config link is kept")
			content := readTestFile(t, target)
			assert.True(t, strings.HasPrefix(content, sshIncludeLine+"\n"), content)
			assert.Contains(t, content, "Host old")
			assertPrivateSD(t, target)
			assertPrivateSD(t, filepath.Join(dot, "config.d"))
			assertPrivateSD(t, filepath.Join(dot, "config.d", "scion-hub"))
		})
	}
}

// Rule I refuse + control: when ~/.ssh grants Users modify, writing the
// include file and ~/.ssh/config is refused (another user could swap them
// after us) and nothing is written; once the foreign ACE is gone the same
// write succeeds.
func TestSSHWrite_RefusesForeignWritableParent(t *testing.T) {
	home := privateHome(t)
	p := sshPaths{home: home}
	require.NoError(t, os.MkdirAll(p.includeDir(), 0o700))
	wintest.Icacls(t, p.sshDir(), "/grant", "*"+wintest.SIDUsers+":(OI)(CI)(M)")

	_, err := ensureSSHIncludeLine(p)
	require.ErrorIs(t, err, privatefs.ErrNotPrivate)
	assert.Contains(t, err.Error(), wintest.SIDUsers)
	assert.NoFileExists(t, p.userConfig())

	wintest.Icacls(t, p.sshDir(), "/remove:g", "*"+wintest.SIDUsers)
	setUserOnlyDirDACL(t, p.includeDir())
	require.NoError(t, writeSSHHubPlan(p, sshPlan(p)), "control: no foreign writer")
	assertPrivateSD(t, p.userConfig())
}

// Rule I refuse + control: a ~/.ssh/config owned by another principal
// (SYSTEM) is refused and left untouched, naming the owner; the same DACL
// on a config the user owns is replaced by a private file.
func TestSSHWrite_RefusesForeignOwnedTarget(t *testing.T) {
	home := privateHome(t)
	p := sshPaths{home: home}
	require.NoError(t, os.MkdirAll(p.sshDir(), 0o700))
	require.NoError(t, os.WriteFile(p.userConfig(), []byte("Host old\n"), 0o600))
	u := wintest.UserSID(t)
	open := fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FR;;;%s)", u, wintest.SIDUsers)
	wintest.SetDACL(t, p.userConfig(), open)
	wintest.SetOwner(t, p.userConfig(), wintest.SIDSystem)

	_, err := ensureSSHIncludeLine(p)
	require.ErrorIs(t, err, privatefs.ErrNotPrivate)
	assert.Contains(t, err.Error(), wintest.SIDSystem)
	assert.Equal(t, "Host old\n", readTestFile(t, p.userConfig()))
	assert.Equal(t, wintest.SIDSystem, wintest.ReadSD(t, p.userConfig()).Owner)

	// Control: owned by the user, same DACL.
	wintest.SetOwner(t, p.userConfig(), u)
	changed, err := ensureSSHIncludeLine(p)
	require.NoError(t, err)
	assert.True(t, changed)
	assertPrivateSD(t, p.userConfig())
}

// Addendum A1 on the relaxed ssh path: a ~/.ssh/config with a second name
// (a hard link) is refused and left untouched; an ordinary config with the
// same DACL is rewritten.
func TestSSHWrite_RefusesHardLinkedTarget(t *testing.T) {
	home := privateHome(t)
	p := sshPaths{home: home}
	require.NoError(t, os.MkdirAll(p.sshDir(), 0o700))
	require.NoError(t, os.WriteFile(p.userConfig(), []byte("Host old\n"), 0o600))
	second := filepath.Join(home, "second-name")
	require.NoError(t, os.Link(p.userConfig(), second))

	_, err := ensureSSHIncludeLine(p)
	require.ErrorIs(t, err, privatefs.ErrHardLinked)
	assert.Equal(t, "Host old\n", readTestFile(t, second))

	require.NoError(t, os.Remove(second))
	changed, err := ensureSSHIncludeLine(p)
	require.NoError(t, err, "control: the same file with one name")
	assert.True(t, changed)
}

// I3 (cmd/ssh.go): scion ssh's per-run known-hosts file goes through the
// shared writer and is private. Control: the refusal side of the same
// writer is covered by the TestSSHWrite_Refuses* tests.
func TestSSHKnownHostsTempFilePrivate(t *testing.T) {
	dir := privateHome(t)
	kh := filepath.Join(dir, "known_hosts")
	require.NoError(t, writeFileAtomic(kh, []byte("scion.h.p.a ssh-ed25519 AAAA\n"), sshFileMode))
	assertPrivateSD(t, kh)
}

// I3: scion vscode's ssh_config, known_hosts and settings.json are written
// private through the shared writer, under the directory lock (I4), with
// the Windows ProxyCommand quoting. A second run rewrites them in place.
func TestVSCodeFiles_PrivateOnWindows(t *testing.T) {
	home := privateHome(t)
	p := vscodePaths{home: home}
	for _, agent := range []string{"a1", "a2"} {
		block, err := renderSSHHostBlockKnown(winSSHExe, "https://h.example", "h", "p", agent, vscodeKnownHostsRef)
		require.NoError(t, err)
		_, err = updateVSCodeFiles(p, vscodeUpdate{host: "scion.h.p." + agent, block: block,
			key: mustNormKey(t, testSSHPublicKey(t)), endpoint: tunnelclient.NormalizeEndpoint("https://h.example")})
		require.NoError(t, err)
	}
	for _, f := range []string{p.sshConfig(), p.knownHosts(), p.settings()} {
		assertPrivateSD(t, f)
	}
	assertPrivateSD(t, p.dir())
	assertPrivateSD(t, p.lockFile())
	blocks := parseSSHHostBlocks(readTestFile(t, p.sshConfig()))
	require.Contains(t, blocks, "scion.h.p.a2")
	assert.Contains(t, blocks["scion.h.p.a2"], `"C:/Program Files/Scion/scion.exe"`)
	assert.Equal(t, "https://h.example", vscodeBlockHub(blocks["scion.h.p.a2"]), "the quoted --hub reads back")
}
