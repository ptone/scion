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
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliRefusal is a source and the refusal text it must produce.
type cliRefusal struct{ uri, want string }

// cliRefusedSources returns host-path, wrapping, endpoint-parameter and
// loopback-archive sources, the first three naming a host directory that
// holds a template.
func cliRefusedSources(t *testing.T) (home string, sources map[string]cliRefusal) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(t.TempDir(), "host-dir")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tpl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tpl", "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tpl", "config.yaml"), []byte("harness: claude\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("host file"), 0o644))
	const noParams = "must not carry connection-string parameters"
	return home, map[string]cliRefusal{
		"local":          {":local:" + dir, `rclone backend "local" is not accepted`},
		"alias":          {":alias,remote=" + dir + ":", noParams},
		"crypt on local": {":crypt,remote=" + dir + ",password=x:", noParams},
		"gcs metadata":   {":gcs,endpoint=http://169.254.169.254:bucket/x", noParams},
		// At most a local connection attempt; refused before it.
		"archive loopback": {"https://127.0.0.1/x.tgz", "source host must be a public address"},
		"archive metadata": {"https://169.254.169.254/latest/x.tgz", "source host must be a public address"},
	}
}

func assertNoCLIHostCopy(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "marker.txt" {
			t.Errorf("host file copied into %s", p)
		}
		return nil
	})
}

// 'scion templates import <source>' refuses the sources and copies nothing.
func TestRunTemplateImport_RefusesRemoteSources(t *testing.T) {
	home, sources := cliRefusedSources(t)
	projectDir := t.TempDir()
	t.Chdir(projectDir)
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, ".scion", "templates"), 0o755))
	oldGlobal := globalMode
	t.Cleanup(func() { globalMode = oldGlobal })
	globalMode = false

	for name, tc := range sources {
		t.Run(name, func(t *testing.T) {
			command := &cobra.Command{}
			command.Flags().Bool("all", true, "")
			command.Flags().Bool("force", false, "")
			command.Flags().Bool("dry-run", true, "")
			command.Flags().String("harness", "", "")
			command.Flags().String("name", "", "")
			var runErr error
			_ = captureStdout(t, func() { runErr = runTemplateImport(command, []string{tc.uri}) })
			assert.ErrorContains(t, runErr, tc.want)
		})
	}
	assertNoCLIHostCopy(t, home)
	assertNoCLIHostCopy(t, projectDir)
}

// 'scion harness-config install <source>' refuses the sources and copies
// nothing.
func TestResolveInstallSource_RefusesRemoteSources(t *testing.T) {
	home, sources := cliRefusedSources(t)
	for name, tc := range sources {
		t.Run(name, func(t *testing.T) {
			var err error
			_ = captureStdout(t, func() {
				var cleanup func()
				_, cleanup, err = resolveInstallSource(tc.uri)
				if cleanup != nil {
					cleanup()
				}
			})
			assert.ErrorContains(t, err, tc.want)
		})
	}
	assertNoCLIHostCopy(t, home)
}
