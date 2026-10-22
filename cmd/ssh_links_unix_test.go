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

//go:build unix

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/internal/testutil/wintest"
	"github.com/GoogleCloudPlatform/scion/pkg/tunnelclient"
)

// writeLinkedLayout runs the ssh-config and vscode writers in home and
// returns nothing; failures fail the test.
func writeLinkedLayout(t *testing.T, home string) {
	t.Helper()
	p := sshPaths{home: home}
	_, err := regenerateSSHHubFiles(p, regenTestEndpoint, "/bin/scion", regenListing(
		sshListedAgent{"proj", "agent", testSSHPublicKey(t)}))
	require.NoError(t, err)
	block, err := renderSSHHostBlockKnown(sshTestExe, "https://h.example", "h", "p", "a1", vscodeKnownHostsRef)
	require.NoError(t, err)
	_, err = updateVSCodeFiles(vscodePaths{home: home}, vscodeUpdate{host: "scion.h.p.a1", block: block,
		key: mustNormKey(t, testSSHPublicKey(t)), endpoint: tunnelclient.NormalizeEndpoint("https://h.example")})
	require.NoError(t, err)
}

// Review r1 finding 1: ~/.ssh and ~/.scion linked to directories OUTSIDE
// home (a dotfile manager, a ~/.scion moved to another disk) are followed:
// ssh-config and vscode write into the link targets, and the links are
// kept. Control: the same writers in a home without links.
func TestSSHAndVSCodeFollowLinksOutsideHome(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	for _, d := range []string{"dotfiles-ssh", "big-disk-scion"} {
		require.NoError(t, os.Mkdir(filepath.Join(outside, d), 0o700))
	}
	wintest.MustSymlink(t, filepath.Join(outside, "dotfiles-ssh"), filepath.Join(home, ".ssh"))
	wintest.MustSymlink(t, filepath.Join(outside, "big-disk-scion"), filepath.Join(home, ".scion"))

	writeLinkedLayout(t, home)
	for _, f := range []string{
		filepath.Join(outside, "dotfiles-ssh", "config"),
		filepath.Join(outside, "dotfiles-ssh", "config.d", "scion-hub"),
		filepath.Join(outside, "big-disk-scion", "ssh", "known_hosts-hub"),
		filepath.Join(outside, "big-disk-scion", "vscode", "ssh_config"),
		filepath.Join(outside, "big-disk-scion", "vscode", "user-data", "User", "settings.json"),
	} {
		assert.FileExists(t, f)
	}
	for _, l := range []string{".ssh", ".scion"} {
		fi, err := os.Lstat(filepath.Join(home, l))
		require.NoError(t, err)
		assert.NotZero(t, fi.Mode()&os.ModeSymlink, "%s is still a link", l)
	}

	plain := t.TempDir()
	writeLinkedLayout(t, plain)
	assert.FileExists(t, filepath.Join(plain, ".ssh", "config.d", "scion-hub"), "control: a home without links")
}
