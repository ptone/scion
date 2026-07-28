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

package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteProjectPreStartHook_CreatesFile(t *testing.T) {
	agentHome := t.TempDir()
	script := "#!/bin/sh\necho hello\n"

	err := WriteProjectPreStartHook(agentHome, script)
	require.NoError(t, err)

	target := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d", ProjectPreStartHookFilename)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, script, string(data))

	// Owner-executable only: the script may carry project secrets and only the
	// owner ever runs it.
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.True(t, info.Mode()&0100 != 0, "file should be owner-executable")
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm(),
		"hook script must not be group- or world-accessible")
}

func TestWriteProjectPreStartHook_Idempotent(t *testing.T) {
	agentHome := t.TempDir()

	require.NoError(t, WriteProjectPreStartHook(agentHome, "#!/bin/sh\necho v1\n"))
	require.NoError(t, WriteProjectPreStartHook(agentHome, "#!/bin/sh\necho v2\n"))

	target := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d", ProjectPreStartHookFilename)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho v2\n", string(data), "second write should overwrite first")
}

func TestWriteProjectPreStartHook_EmptyScript_NoOp(t *testing.T) {
	agentHome := t.TempDir()

	err := WriteProjectPreStartHook(agentHome, "")
	require.NoError(t, err)

	target := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d", ProjectPreStartHookFilename)
	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr), "file should not be created for empty script")
}

// An empty script means "no hook applies" — a previously staged script must be
// removed so it cannot survive on a reused agent home after the hook is deleted.
func TestWriteProjectPreStartHook_EmptyScript_RemovesStaleFile(t *testing.T) {
	agentHome := t.TempDir()

	require.NoError(t, WriteProjectPreStartHook(agentHome, "#!/bin/sh\necho stale\n"))
	target := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d", ProjectPreStartHookFilename)
	require.FileExists(t, target)

	require.NoError(t, WriteProjectPreStartHook(agentHome, ""))

	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr), "stale hook script should be removed")
}

// A file staged by an older build with mode 0755 must be tightened on rewrite;
// os.WriteFile alone leaves the mode of an existing file untouched.
func TestWriteProjectPreStartHook_TightensLegacyPermissions(t *testing.T) {
	agentHome := t.TempDir()
	dir := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d")
	require.NoError(t, os.MkdirAll(dir, 0755))
	target := filepath.Join(dir, ProjectPreStartHookFilename)
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\necho legacy\n"), 0755))

	require.NoError(t, WriteProjectPreStartHook(agentHome, "#!/bin/sh\necho current\n"))

	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestWriteProjectPreStartHook_CreatesDirectory(t *testing.T) {
	agentHome := t.TempDir()

	// pre-start.d does not exist yet — WriteProjectPreStartHook must create it.
	err := WriteProjectPreStartHook(agentHome, "#!/bin/sh\n")
	require.NoError(t, err)

	dir := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

// Regression test for issue #612 bug #7. os.Remove reports ENOTDIR, not
// ENOENT, when a parent path component exists but is not a directory. The
// guard used to check only os.IsNotExist, so WriteProjectPreStartHook returned
// an error and failed the whole ProvisionAgent call — for every agent with no
// project hook configured, which is the common case.
func TestWriteProjectPreStartHook_EmptyScript_ParentNotADirectory(t *testing.T) {
	for _, blocker := range []string{".scion", filepath.Join(".scion", "hooks"),
		filepath.Join(".scion", "hooks", "pre-start.d")} {
		t.Run(blocker, func(t *testing.T) {
			agentHome := t.TempDir()
			path := filepath.Join(agentHome, blocker)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
			// Occupy the path component with a regular file.
			require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0644))

			// Sanity check: the underlying os.Remove really does fail here, so
			// this test would catch a regression rather than pass vacuously.
			target := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d",
				ProjectPreStartHookFilename)
			removeErr := os.Remove(target)
			require.Error(t, removeErr)
			require.False(t, os.IsNotExist(removeErr),
				"expected ENOTDIR (not ENOENT) from os.Remove, got %v", removeErr)

			assert.NoError(t, WriteProjectPreStartHook(agentHome, ""),
				"no configured hook must not fail provisioning")
		})
	}
}

// A genuine removal failure must still surface. Making the parent directory
// read-only makes the unlink fail with EACCES, which is neither ENOENT nor
// ENOTDIR.
func TestWriteProjectPreStartHook_EmptyScript_RealRemoveErrorSurfaces(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}

	agentHome := t.TempDir()
	require.NoError(t, WriteProjectPreStartHook(agentHome, "#!/bin/sh\necho stale\n"))

	dir := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d")
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	err := WriteProjectPreStartHook(agentHome, "")
	assert.Error(t, err, "a real removal failure must not be swallowed")
}

func TestWriteProjectPreStartHook_Filename(t *testing.T) {
	assert.Equal(t, "30-project-custom", ProjectPreStartHookFilename,
		"filename must be exactly '30-project-custom' to run after 20-harness-provision")
}
