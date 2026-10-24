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

//go:build !no_sqlite

package hub

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hubRemoteSourceMarker = "remote-source-marker-content"

// hubHostSourceForms returns host-path and wrapping rclone sources naming a
// host directory that holds both a template and a harness-config.
func hubHostSourceForms(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "host-dir")
	tpl := filepath.Join(dir, "tpl")
	require.NoError(t, os.MkdirAll(tpl, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tpl, "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tpl, "config.yaml"), []byte("name: claude\nharness: claude\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tpl, "marker.txt"), []byte(hubRemoteSourceMarker), 0o644))
	return map[string]string{
		"local":          ":local:" + dir,
		"alias":          ":alias,remote=" + dir + ":",
		"crypt on local": ":crypt,remote=" + dir + ",password=x:",
	}
}

// storedMarker reports whether any stored object holds the host marker.
func storedMarker(stor *mockStorage) bool {
	for _, b := range storageSnapshot(stor) {
		if bytes.Contains(b, []byte(hubRemoteSourceMarker)) {
			return true
		}
	}
	return false
}

// hubRemoteSourceProjectRoutes are the project-scoped import and discover
// routes that take a sourceUrl.
var hubRemoteSourceProjectRoutes = []string{
	"import-templates", "import-harness-configs", "discover-templates", "discover-harness-configs",
}

// createRemoteSourceProject stores a project for the project-scoped routes.
func createRemoteSourceProject(t *testing.T, s store.Store) *store.Project {
	t.Helper()
	project := &store.Project{
		ID:        tid("project-remote-source"),
		Name:      "remote-source",
		Slug:      "remote-source",
		GitRemote: "https://github.com/test/remote-source",
	}
	require.NoError(t, s.CreateProject(context.Background(), project))
	return project
}

// assertHubSourceRefused posts sourceURL to the global and project-scoped
// import and discover endpoints and to reimport, and expects a 400 each
// time, with nothing stored.
func assertHubSourceRefused(t *testing.T, sourceURL string) {
	t.Helper()
	srv, s := testInstallSourceServer(t)
	hc := installClaudeViaHub(t, srv, s, installPinnedClaudeURL)
	project := createRemoteSourceProject(t, s)
	stor := srv.GetStorage().(*mockStorage)
	before := storageSnapshot(stor)

	for _, kind := range []string{"template", "harness-config"} {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/resources/import", ImportResourcesRequest{
			Kind: kind, Scope: "global", SourceURL: sourceURL,
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "import %s: %s", kind, rec.Body.String())

		rec = doRequest(t, srv, http.MethodPost, "/api/v1/resources/discover", map[string]interface{}{
			"kind": kind, "scope": "global", "sourceUrl": sourceURL,
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "discover %s: %s", kind, rec.Body.String())
	}

	for _, route := range hubRemoteSourceProjectRoutes {
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/projects/"+project.ID+"/"+route,
			map[string]interface{}{"sourceUrl": sourceURL})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "project %s: %s", route, rec.Body.String())
	}

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/harness-configs/"+hc.ID+"/reimport",
		map[string]interface{}{"sourceUrl": sourceURL})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "reimport: %s", rec.Body.String())

	assert.False(t, storedMarker(stor), "no host file may be stored")
	assert.True(t, maps.EqualFunc(before, storageSnapshot(stor), bytes.Equal), "a refused source must not write to storage")
	after := globalClaude(t, s)
	require.NotNil(t, after)
	assert.Equal(t, hc.ContentHash, after.ContentHash, "a refused reimport must not change the files")
	assert.Equal(t, installPinnedClaudeURL, after.SourceURL, "a refused reimport must not change the source URL")
}

// The hub import, discover and reimport endpoints refuse host-path and
// wrapping rclone sources.
func TestHubRemoteSourceRefusal_HostForms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, uri := range hubHostSourceForms(t) {
		t.Run(name, func(t *testing.T) { assertHubSourceRefused(t, uri) })
	}
}
