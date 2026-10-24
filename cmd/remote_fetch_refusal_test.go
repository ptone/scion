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

// cliRefusedSources returns host-path, wrapping and endpoint-parameter
// sources naming a host directory that holds a template.
func cliRefusedSources(t *testing.T) (home string, sources map[string]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(t.TempDir(), "host-dir")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tpl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tpl", "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tpl", "config.yaml"), []byte("harness: claude\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("host file"), 0o644))
	return home, map[string]string{
		"local":          ":local:" + dir,
		"alias":          ":alias,remote=" + dir + ":",
		"crypt on local": ":crypt,remote=" + dir + ",password=x:",
		// Refused by the pre-check before any I/O. Archive URLs with an
		// internal address are covered in pkg/config, without I/O.
		"gcs metadata": ":gcs,endpoint=http://169.254.169.254:bucket/x",
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

	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			command := &cobra.Command{}
			command.Flags().Bool("all", true, "")
			command.Flags().Bool("force", false, "")
			command.Flags().Bool("dry-run", true, "")
			command.Flags().String("harness", "", "")
			command.Flags().String("name", "", "")
			var runErr error
			_ = captureStdout(t, func() { runErr = runTemplateImport(command, []string{src}) })
			assert.Error(t, runErr)
		})
	}
	assertNoCLIHostCopy(t, home)
	assertNoCLIHostCopy(t, projectDir)
}

// 'scion harness-config install <source>' refuses the sources and copies
// nothing.
func TestResolveInstallSource_RefusesRemoteSources(t *testing.T) {
	home, sources := cliRefusedSources(t)
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			var err error
			_ = captureStdout(t, func() {
				var cleanup func()
				_, cleanup, err = resolveInstallSource(src)
				if cleanup != nil {
					cleanup()
				}
			})
			assert.Error(t, err)
		})
	}
	assertNoCLIHostCopy(t, home)
}
