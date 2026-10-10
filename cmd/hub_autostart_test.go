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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutoStartAllowedFor(t *testing.T) {
	off, on := false, true
	settingOff := &config.Settings{Hub: &config.HubClientConfig{AutoStart: &off}}
	settingOn := &config.Settings{Hub: &config.HubClientConfig{AutoStart: &on}}
	tests := []struct {
		name      string
		settings  *config.Settings
		env       map[string]string
		want      bool
		wantInWhy string
	}{
		{name: "default is on", settings: &config.Settings{}, want: true},
		{name: "nil settings is on", settings: nil, want: true},
		{name: "setting true", settings: settingOn, want: true},
		{name: "setting false", settings: settingOff, want: false, wantInWhy: "hub.auto_start"},
		{name: "env 0", settings: settingOn, env: map[string]string{"SCION_HUB_AUTO_START": "0"}, want: false, wantInWhy: "SCION_HUB_AUTO_START=0"},
		{name: "env false", settings: &config.Settings{}, env: map[string]string{"SCION_HUB_AUTO_START": "false"}, want: false},
		{name: "env off", settings: &config.Settings{}, env: map[string]string{"SCION_HUB_AUTO_START": "off"}, want: false},
		{name: "env 1 beats setting false", settings: settingOff, env: map[string]string{"SCION_HUB_AUTO_START": "1"}, want: true},
		{name: "env yes", settings: settingOff, env: map[string]string{"SCION_HUB_AUTO_START": "yes"}, want: true},
		{name: "unrecognised env value is off", settings: settingOn, env: map[string]string{"SCION_HUB_AUTO_START": "maybe"}, want: false, wantInWhy: "not a boolean"},
		{name: "inside agent container (host uid)", settings: settingOn, env: map[string]string{"SCION_HOST_UID": "1000"}, want: false, wantInWhy: "agent container"},
		{name: "inside agent container (agent id)", settings: settingOn, env: map[string]string{"SCION_AGENT_ID": "a1"}, want: false, wantInWhy: "agent container"},
		{name: "container beats env 1", settings: settingOn, env: map[string]string{"SCION_HOST_UID": "1000", "SCION_HUB_AUTO_START": "1"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			got, why := autoStartAllowedFor(tc.settings, getenv)
			assert.Equal(t, tc.want, got)
			if tc.want {
				assert.Empty(t, why)
			} else {
				assert.Contains(t, why, tc.wantInWhy)
			}
		})
	}
}

func TestIsLocalWorkstationEndpoint_CLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SCION_DEV_TOKEN", "scion_dev_abc")

	assert.True(t, isLocalWorkstationEndpoint("http://127.0.0.1:8080"), "loopback plus dev token")
	assert.False(t, isLocalWorkstationEndpoint("https://hub.example.com"), "remote plus dev token")

	t.Setenv("SCION_HUB_TOKEN", "scion_pat_xyz")
	assert.False(t, isLocalWorkstationEndpoint("http://127.0.0.1:8080"), "loopback with a non-dev token")
}

func TestEndpointHostPort(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{in: "http://127.0.0.1:8080", wantHost: "127.0.0.1", wantPort: 8080},
		{in: "http://localhost", wantHost: "localhost", wantPort: 80},
		{in: "https://hub.example.com", wantHost: "hub.example.com", wantPort: 443},
		{in: "http://[::1]:9090", wantHost: "[::1]", wantPort: 9090},
		{in: "not a url", wantErr: true},
	}
	for _, tc := range tests {
		host, port, err := endpointHostPort(tc.in)
		if tc.wantErr {
			assert.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.wantHost, host, tc.in)
		assert.Equal(t, tc.wantPort, port, tc.in)
	}
}

// setupNoEndpointLink prepares a temp HOME whose global project has no hub
// endpoint, and targets it with hub link. It returns the global dir.
func setupNoEndpointLink(t *testing.T) string {
	t.Helper()
	origProjectPath, origGlobal, origYes := projectPath, globalMode, autoConfirm
	origEnsure, origOffer := ensureLocalServerFn, offerTemplateSyncOnLinkFn
	t.Cleanup(func() {
		projectPath, globalMode, autoConfirm = origProjectPath, origGlobal, origYes
		ensureLocalServerFn, offerTemplateSyncOnLinkFn = origEnsure, origOffer
	})
	offerTemplateSyncOnLinkFn = func(_, _, _ string, _ bool) {}

	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".scion")
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "settings.yaml"),
		[]byte("schema_version: \"1\"\n"), 0o644))
	t.Chdir(home)
	projectPath, globalMode, autoConfirm = "", true, true
	return globalDir
}

func TestRunHubLink_NoEndpointAutoStartOff(t *testing.T) {
	setupNoEndpointLink(t)
	t.Setenv("SCION_HUB_AUTO_START", "0")
	calls := 0
	ensureLocalServerFn = func() (string, error) {
		calls++
		return "", errors.New("must not be called")
	}

	err := runHubLink(hubLinkCmd, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errHubNotConfigured), "error = %v, want errHubNotConfigured", err)
	assert.Contains(t, err.Error(), "SCION_HUB_AUTO_START=0")
	assert.Equal(t, 0, calls, "auto-start must not run")
}

func TestRunHubLink_NoEndpointAutoStartFailureIsReturned(t *testing.T) {
	setupNoEndpointLink(t)
	ensureLocalServerFn = func() (string, error) {
		return "", errors.New("port conflict: ports [8080] are occupied")
	}

	err := runHubLink(hubLinkCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port conflict")
}

// TestRunHubLink_NoEndpointAutoStarts covers the vertical slice: with no
// endpoint, hub link starts the local server (stubbed: it writes the
// endpoint and dev token the way 'scion server start' does) and links.
func TestRunHubLink_NoEndpointAutoStarts(t *testing.T) {
	globalDir := setupNoEndpointLink(t)

	var registered bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
		case r.URL.Path == "/api/v1/projects/register" && r.Method == http.MethodPost:
			registered = true
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"project": map[string]interface{}{"id": body["id"], "name": body["name"], "slug": body["name"]},
				"created": true,
			})
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"projects": []interface{}{}})
		case r.URL.Path == "/api/v1/runtime-brokers":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"brokers": []interface{}{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{"code": "not_found", "message": "not found"},
			})
		}
	}))
	t.Cleanup(server.Close)

	calls := 0
	ensureLocalServerFn = func() (string, error) {
		calls++
		if err := config.UpdateVersionedSetting(globalDir, "hub.endpoint", server.URL); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(globalDir, "dev-token"), []byte("scion_dev_test\n"), 0o600); err != nil {
			return "", err
		}
		return server.URL, nil
	}

	require.NoError(t, runHubLink(hubLinkCmd, nil))
	assert.Equal(t, 1, calls, "auto-start runs once")
	assert.True(t, registered, "project registered on the started hub")

	s, err := config.LoadSettings(globalDir)
	require.NoError(t, err)
	assert.Equal(t, server.URL, s.GetHubEndpoint())
	assert.True(t, s.IsHubLinked(), "hub.linked written")
	assert.True(t, s.Hub.Enabled != nil && *s.Hub.Enabled, "hub.enabled still written in this phase")
	assert.True(t, strings.HasPrefix(server.URL, "http://127.0.0.1:"))
	assert.True(t, isLocalWorkstationEndpoint(server.URL), "the started hub is a local workstation endpoint")
}
