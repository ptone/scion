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

package cmd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sshSpecialSuffixes are endpoint path suffixes with characters that the
// ProxyCommand shell or the C runtime's command-line parser treats
// specially (and '%').
var sshSpecialSuffixes = []string{"/$(id)", "/`id`", `/x"y`, "/$HOME", "/a!b", "/a&b", "/100%25", "/a'b", "/a;b", `/a\b`}

// A hub endpoint is held to ASCII letters, digits and -._:/ (plus the
// brackets of an IPv6 host) on every OS. Controls: a path, a port and an
// IPv6 host are accepted.
func TestSSHEndpointURL_RefusesShellSpecialChars(t *testing.T) {
	for _, s := range sshSpecialSuffixes {
		ep := "https://hub.example.com" + s
		_, err := sshEndpointURL(ep)
		require.Error(t, err, ep)
		assert.Contains(t, err.Error(), "may contain only", ep)
	}
	for _, bad := range []string{"https://hub.example.com/a[b]", "https://hub.example.com/~me", "https://hub.example.com/a,b", "https://hub.example.com/a+b", "https://hüb.example.com"} {
		_, err := sshEndpointURL(bad)
		require.Error(t, err, bad)
	}
	for _, ok := range []string{"https://hub.example.com", "https://hub.example.com:8443/team-a/hub_1.v2/", "http://[::1]:9810", "http://127.0.0.1:8080"} {
		_, err := sshEndpointURL(ok)
		require.NoError(t, err, ok)
	}
}

// With the Windows ProxyCommand form, scion ssh-config (all agents and one
// agent), scion vscode and scion ssh refuse an endpoint with a character
// that form cannot carry, and nothing in the home directory changes.
// Control: the same commands with the hub's plain endpoint succeed.
func TestSSHWindowsForm_RefusesEndpointShellSpecialChars(t *testing.T) {
	setSSHQuoteForWindows(t, true)
	hub := twoProjectHub(t, "tok")
	for _, s := range sshSpecialSuffixes {
		env, _, _ := newSSHTestEnv(t, t.TempDir(), hub)
		fake := stubVSCode(t)
		env.endpoint = hub.srv.URL + s
		before := sshFilesSnapshot(t, env.home)

		assert.Error(t, sshConfigMain(context.Background(), env, nil, true), s)
		assert.Error(t, sshConfigMain(context.Background(), env, []string{"a/b-c"}, true), s)
		assert.Error(t, vscodeMain(context.Background(), env, "a/b-c", vscodeDefaultFolder, false), s)
		_, cleanup, err := prepareSSH(context.Background(), env, "a/b-c", nil)
		if cleanup != nil {
			cleanup()
		}
		assert.Error(t, err, s)
		assert.Empty(t, fake.runs, s)
		assert.Equal(t, before, sshFilesSnapshot(t, env.home), "%s: nothing is written", s)
	}

	env, _, _ := newSSHTestEnv(t, t.TempDir(), hub)
	stubVSCode(t)
	require.NoError(t, sshConfigMain(context.Background(), env, nil, true))
	require.NoError(t, vscodeMain(context.Background(), env, "a/b-c", vscodeDefaultFolder, false))
	_, cleanup, err := prepareSSH(context.Background(), env, "a/b-c", nil)
	require.NoError(t, err)
	cleanup()
}

// With the Windows form, a scion executable path with a character outside
// the allowlist is refused by ssh-config, vscode and ssh, and nothing in
// the home directory changes. Controls: paths with a space, with
// "(x86)" and with an 8.3 "~" are written double-quoted.
func TestSSHWindowsForm_RefusesExePathShellSpecialChars(t *testing.T) {
	setSSHQuoteForWindows(t, true)
	hub := twoProjectHub(t, "tok")
	base := t.TempDir()
	for _, dir := range []string{"a$(b)", "a`b`", "a!b", "a%b", "a&b", "a;b", "a'b", "a^b"} {
		exe := filepath.Join(base, dir, "scion")
		env, _, _ := newSSHTestEnv(t, t.TempDir(), hub)
		fake := stubVSCode(t)
		env.exe = exe
		before := sshFilesSnapshot(t, env.home)

		err := sshConfigMain(context.Background(), env, nil, true)
		require.Error(t, err, exe)
		assert.Contains(t, err.Error(), "scion executable path", exe)
		assert.Error(t, sshConfigMain(context.Background(), env, []string{"a/b-c"}, true), exe)
		assert.Error(t, vscodeMain(context.Background(), env, "a/b-c", vscodeDefaultFolder, false), exe)
		_, cleanup, err := prepareSSH(context.Background(), env, "a/b-c", nil)
		if cleanup != nil {
			cleanup()
		}
		assert.Error(t, err, exe)
		assert.Empty(t, fake.runs, exe)
		assert.Equal(t, before, sshFilesSnapshot(t, env.home), "%s: nothing is written", exe)
	}

	for _, dir := range []string{"My Tools", "Program Files (x86)", "PROGRA~1"} {
		env, _, _ := newSSHTestEnv(t, t.TempDir(), hub)
		stubVSCode(t)
		env.exe = filepath.Join(base, dir, "scion")
		require.NoError(t, sshConfigMain(context.Background(), env, nil, true), dir)
		require.NoError(t, vscodeMain(context.Background(), env, "a/b-c", vscodeDefaultFolder, false), dir)
		cfg := readTestFile(t, vscodePaths{home: env.home}.sshConfig())
		assert.Contains(t, cfg, `ProxyCommand "`+filepath.ToSlash(env.exe)+`" tunnel --stdio --ssh`, dir)
	}
}

// The rendered Host block with the Windows form and an endpoint with a
// path (control): every word is unquoted except the executable path with a
// space.
func TestRenderSSHHostBlock_WindowsFormEndpointWithPath(t *testing.T) {
	setSSHQuoteForWindows(t, true)
	exe := filepath.Join(t.TempDir(), "Program Files", "scion.exe")
	pin := filepath.Join(t.TempDir(), "known_hosts")
	block, err := renderSSHHostBlockPinned(exe, "https://hub.example.com/team-a", "hub", "p", "a", vscodeKnownHostsRef, pin)
	require.NoError(t, err)
	assert.Contains(t, block, `  ProxyCommand "`+filepath.ToSlash(exe)+`" tunnel --stdio --ssh --hub https://hub.example.com/team-a --project p --known-hosts `+filepath.ToSlash(pin)+` --host-key-alias scion.hub.p.a a`+"\n")
}

// With the Windows form, an IPv6-literal endpoint is accepted and written
// double-quoted, and the vscode block parser reads it back.
func TestRenderSSHHostBlock_WindowsFormIPv6Endpoint(t *testing.T) {
	setSSHQuoteForWindows(t, true)
	exe := filepath.Join(t.TempDir(), "scion")
	block, err := renderSSHHostBlock(exe, "http://[::1]:9810", "hub", "p", "a")
	require.NoError(t, err)
	assert.Contains(t, block, ` --hub "http://[::1]:9810" --project p a`+"\n")
	assert.Equal(t, "http://[::1]:9810", vscodeBlockHub(block))
}
