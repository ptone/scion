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

package runtimebroker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A template ref the hub passed through unresolved is resolved on the broker.
// Refused sources resolve to nothing and copy nothing; allowlisted storage
// sources are opened anonymously.
func TestBrokerTemplateResolution_RemoteSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var calls []config.RemoteFetchCredentials
	t.Cleanup(config.SetRcloneFetchForTest(func(_ context.Context, _ config.RcloneRemote, creds config.RemoteFetchCredentials, destPath string) error {
		calls = append(calls, creds)
		return os.WriteFile(filepath.Join(destPath, "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644)
	}))

	dir := filepath.Join(t.TempDir(), "host-dir")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("host file"), 0o644))
	projectPath := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(filepath.Join(projectPath, "templates"), 0o755))

	refused := map[string]string{
		"local":                 ":local:" + dir,
		"alias":                 ":alias,remote=" + dir + ":",
		"crypt on local":        ":crypt,remote=" + dir + ",password=x:",
		"gcs metadata endpoint": ":gcs,endpoint=http://169.254.169.254:bucket/x",
		"archive metadata":      "http://169.254.169.254/latest/x.tar.gz",
		"archive loopback":      "https://127.0.0.1/x.tgz",
	}
	for name, ref := range refused {
		t.Run(name, func(t *testing.T) {
			req := CreateAgentRequest{Config: &CreateAgentConfig{Template: ref}, ProjectPath: projectPath}
			assert.Nil(t, envGatherTemplate(req, ""))
			assert.Nil(t, templateChainPaths(ref, projectPath))
			// Both helpers drop the error; the resolver they call refuses
			// the source itself.
			_, err := config.GetTemplateChainInProject(ref, projectPath)
			assert.ErrorIs(t, err, config.ErrRemoteSourceNotAccepted)
		})
	}
	assert.Empty(t, calls, "a refused source must never reach rclone")
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "marker.txt" {
			t.Errorf("host file copied into %s", p)
		}
		return nil
	})

	const src = ":gcs:my-bucket/t"
	req := CreateAgentRequest{Config: &CreateAgentConfig{Template: src}, ProjectPath: projectPath}
	require.NotNil(t, envGatherTemplate(req, ""))
	require.NotEmpty(t, templateChainPaths(src, projectPath))
	assert.Equal(t, []config.RemoteFetchCredentials{config.RemoteFetchAnonymous, config.RemoteFetchAnonymous}, calls)
}
