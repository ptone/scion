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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// server.hub.trusted_proxies is read once at startup. Every settings write
// path refuses it (422) in file, DB and workstation mode, and the GET view
// omits it, the same as server.hub.test_identities.

var trustedProxiesBodies = map[string]string{
	"nested":      `{"server":{"hub":{"trusted_proxies":["10.0.0.0/8"]}}}`,
	"flat dotted": `{"server.hub.trusted_proxies":["10.0.0.0/8"]}`,
	"case folded": `{"Server":{"HUB":{"Trusted_Proxies":["10.0.0.0/8"]}}}`,
	"empty list":  `{"server":{"hub":{"trusted_proxies":[]}}}`,
	"null value":  `{"server":{"hub":{"trusted_proxies":null}}}`,
	"with others": `{"default_model":"m1","server":{"hub":{"trusted_proxies":["10.0.0.0/8"],"auto_suspend_stalled":true}}}`,
}

func TestNeverAPIWritable_TrustedProxiesRefusedInFileMode(t *testing.T) {
	for name, body := range trustedProxiesBodies {
		t.Run(name, func(t *testing.T) {
			srv := &Server{}
			rr, settingsPath := fileModePutServerConfig(t, srv, body)
			assert.Equal(t, []string{config.TrustedProxiesKey}, tiaErrorKeys(t, rr))
			_, err := os.Stat(settingsPath)
			assert.True(t, os.IsNotExist(err), "a refused PUT writes no settings file")
		})
	}
}

func TestNeverAPIWritable_TrustedProxiesRefusedInDBAndWorkstationMode(t *testing.T) {
	for _, workstation := range []bool{false, true} {
		for name, body := range trustedProxiesBodies {
			t.Run(map[bool]string{false: "db", true: "workstation"}[workstation]+"/"+name, func(t *testing.T) {
				settingsPath := workstationHome(t)
				srv, st, _ := newSQLiteHubInMode(t, workstation, nil)
				before := hubSettingRevisions(t, st)
				rr := putServerConfig(t, srv, body)
				assert.Equal(t, []string{config.TrustedProxiesKey}, tiaErrorKeys(t, rr))
				data, err := os.ReadFile(settingsPath)
				require.NoError(t, err)
				assert.NotContains(t, string(data), "trusted_proxies")
				assert.Equal(t, before, hubSettingRevisions(t, st), "a refused PUT writes no DB row")
			})
		}
	}
}

func TestNeverAPIWritable_TrustedProxiesHaveNoDBSection(t *testing.T) {
	layer1, layer0, _ := opsettings.ClassifyKeys([]string{config.TrustedProxiesKey})
	assert.Empty(t, layer1, "no Layer-1 section can store the trusted proxy list")
	assert.Equal(t, []string{config.TrustedProxiesKey}, layer0)
}

func TestNeverAPIWritable_GetDoesNotEchoTrustedProxies(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	settingsPath := filepath.Join(tmpHome, ".scion", "settings.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o700))
	yaml := "schema_version: \"1\"\nserver:\n  hub:\n    trusted_proxies:\n      - 198.51.100.0/24\n"
	require.NoError(t, os.WriteFile(settingsPath, []byte(yaml), 0o644))
	srv := &Server{}
	getRR := httptest.NewRecorder()
	srv.handleGetServerConfig(getRR)
	require.Equal(t, http.StatusOK, getRR.Code, getRR.Body.String())
	assert.NotContains(t, getRR.Body.String(), "trusted_proxies")
	assert.NotContains(t, getRR.Body.String(), "198.51.100.0/24")
	// The GET body echoes back cleanly, and the file value is preserved.
	echo := httptest.NewRecorder()
	srv.handleAdminServerConfig(echo, adminRequest(http.MethodPut, "/api/v1/admin/server-config", getRR.Body.String()))
	assert.Equal(t, http.StatusOK, echo.Code, echo.Body.String())
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "198.51.100.0/24")
}
