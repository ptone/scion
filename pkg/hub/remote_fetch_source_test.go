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
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hubNetworkSourceForms are endpoint-parameter and internal-address
// sources, with the refusal text the hub must answer with.
var hubNetworkSourceForms = map[string]hubRefusal{
	"gcs metadata endpoint":   {":gcs,endpoint=http://169.254.169.254:bucket/x", "must not carry connection-string parameters"},
	"archive metadata http":   {"http://169.254.169.254/latest/x.tar.gz", "archive source must use https"},
	"archive metadata https":  {"https://169.254.169.254/x.tar.gz", "source host must be a public address"},
	"archive mapped metadata": {"https://[::ffff:169.254.169.254]/x.tar.gz", "source host must be a public address"},
	"archive loopback":        {"https://127.0.0.1/x.tar.gz", "source host must be a public address"},
}

// The same endpoints refuse endpoint-parameter and internal-address
// sources with a validation error, before any fetch.
func TestHubRemoteSourceRefusal_NetworkForms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fetched := 0
	t.Cleanup(config.SetRcloneFetchForTest(func(context.Context, config.RcloneRemote, config.RemoteFetchCredentials, string) error {
		fetched++
		return nil
	}))
	for name, tc := range hubNetworkSourceForms {
		t.Run(name, func(t *testing.T) {
			// The refusal reason follows the sentinel text, possibly after a
			// qualifier ("rclone source ..."), so the two are checked apart.
			assertHubSourceRefused(t, tc.uri, config.ErrRemoteSourceNotAccepted.Error())
			assertHubSourceRefused(t, tc.uri, tc.want)
		})
	}
	assert.Zero(t, fetched)
}

// Every hub fetch path opens storage sources anonymously.
func TestHubRemoteFetch_UsesAnonymousCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var calls []config.RemoteFetchCredentials
	t.Cleanup(config.SetRcloneFetchForTest(func(_ context.Context, _ config.RcloneRemote, creds config.RemoteFetchCredentials, destPath string) error {
		calls = append(calls, creds)
		dir := filepath.Join(destPath, "claude")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "scion-agent.yaml"), []byte("schema_version: \"1\"\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("name: claude\nharness: claude\n"), 0o644)
	}))
	const src = ":gcs:my-bucket/configs"

	srv, s := testInstallSourceServer(t)
	hc := installClaudeViaHub(t, srv, s, installPinnedClaudeURL)
	project := createRemoteSourceProject(t, s)
	for _, route := range hubRemoteSourceProjectRoutes {
		_ = doRequest(t, srv, http.MethodPost, "/api/v1/projects/"+project.ID+"/"+route,
			map[string]interface{}{"sourceUrl": src})
	}
	for _, kind := range []string{"template", "harness-config"} {
		_ = doRequest(t, srv, http.MethodPost, "/api/v1/resources/import", ImportResourcesRequest{
			Kind: kind, Scope: "global", SourceURL: src,
		})
		_ = doRequest(t, srv, http.MethodPost, "/api/v1/resources/discover", map[string]interface{}{
			"kind": kind, "scope": "global", "sourceUrl": src,
		})
	}
	_ = doRequest(t, srv, http.MethodPost, "/api/v1/harness-configs/"+hc.ID+"/reimport",
		map[string]interface{}{"sourceUrl": src})

	require.Len(t, calls, 9, "project import and discover (4 routes), global import and discover for both kinds, plus reimport")
	for i, c := range calls {
		assert.Equal(t, config.RemoteFetchAnonymous, c, "call %d", i)
	}
}
