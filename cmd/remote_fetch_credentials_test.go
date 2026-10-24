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
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The CLI fetches allowlisted storage sources with the user's own
// credentials, on every CLI fetch path.
func TestCLIRemoteFetch_UsesCallerCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var calls []config.RemoteFetchCredentials
	restore := config.SetRcloneFetchForTest(func(_ context.Context, _ config.RcloneRemote, creds config.RemoteFetchCredentials, destPath string) error {
		calls = append(calls, creds)
		dir := filepath.Join(destPath, "tpl")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644))
		return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("harness: claude\n"), 0o644)
	})
	t.Cleanup(restore)
	const src = ":gcs:my-bucket/tpl"

	projectDir := t.TempDir()
	t.Chdir(projectDir)
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, ".scion", "templates"), 0o755))
	oldGlobal := globalMode
	t.Cleanup(func() { globalMode = oldGlobal })
	globalMode = false
	command := &cobra.Command{}
	command.Flags().Bool("all", true, "")
	command.Flags().Bool("force", false, "")
	command.Flags().Bool("dry-run", true, "")
	command.Flags().String("harness", "", "")
	command.Flags().String("name", "", "")
	_ = captureStdout(t, func() { _ = runTemplateImport(command, []string{src}) })

	_ = captureStdout(t, func() {
		_, cleanup, _ := resolveInstallSource(src)
		if cleanup != nil {
			cleanup()
		}
	})

	_, _ = findLocalTemplate(src)

	assert.Equal(t, []config.RemoteFetchCredentials{
		config.RemoteFetchCallerCredentials,
		config.RemoteFetchCallerCredentials,
		config.RemoteFetchCallerCredentials,
	}, calls)
}
