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

package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localSourceFixture creates a directory holding a template and returns the
// rclone source forms that would name it through a host-path or wrapping
// backend (hostForms), plus endpoint and internal-address forms
// (networkForms). Network forms are only passed to checks that do no I/O,
// so these tests never open a connection.
func localSourceFixture(t *testing.T) (hostForms, networkForms map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "host-dir")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tpl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tpl", "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("host file"), 0o644))
	hostForms = map[string]string{
		"local":          ":local:" + dir,
		"alias":          ":alias,remote=" + dir + ":",
		"crypt on local": ":crypt,remote=" + dir + ",password=x:",
	}
	networkForms = map[string]string{
		"gcs metadata": ":gcs,endpoint=http://169.254.169.254:bucket/x",
		"archive meta": "http://169.254.169.254/latest/x.tar.gz",
		"archive loop": "https://127.0.0.1/x.tgz",
	}
	return hostForms, networkForms
}

// assertNoHostCopy fails if any file under the remote-template cache holds
// the fixture's marker file.
func assertNoHostCopy(t *testing.T, home string) {
	t.Helper()
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "marker.txt" {
			t.Errorf("host file copied into %s", p)
		}
		return nil
	})
}

// The CLI pre-check, the shared resolver and the broker resolver refuse
// host-path, wrapping, endpoint-parameter and internal-address sources, and
// copy nothing.
func TestRemoteSourceRefusal_ValidateAndResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hostForms, networkForms := localSourceFixture(t)
	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "templates"), 0o755))

	// The resolvers call ValidateRemoteURI before fetching, so this check
	// covers network forms on the resolver paths too.
	for name, uri := range networkForms {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, ValidateRemoteURI(uri), "CLI and resolver pre-check")
		})
	}
	for name, uri := range hostForms {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, ValidateRemoteURI(uri), "CLI pre-check")
			_, err := FindTemplateWithContext(context.Background(), uri)
			assert.Error(t, err, "shared resolver")
			_, err = FindTemplateInProjectPath(uri, projectDir)
			assert.Error(t, err, "broker resolver")
			_, err = GetTemplateChainInProject(uri, projectDir)
			assert.Error(t, err, "broker template chain")
		})
	}
	assertNoHostCopy(t, home)
}

// An allowlisted remote is still accepted at parse level (no fetch).
func TestRemoteSourceRefusal_AllowlistedRemoteAccepted(t *testing.T) {
	assert.NoError(t, ValidateRemoteURI(":gcs:my-bucket/templates/x"))
	assert.NoError(t, ValidateRemoteURI("https://github.com/o/r/tree/main/.scion/templates/x"))
	assert.NoError(t, ValidateRemoteURI("https://example.com/templates/x.tgz"))
}
