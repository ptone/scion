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
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/wsprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// templateRefCreateFixture is a broker with a project holding the template
// "claude", and a template directory outside the template roots.
type templateRefCreateFixture struct {
	srv        *Server
	mgr        *envCapturingManager
	projectDir string
	outsideDir string
}

func newTemplateRefCreateFixture(t *testing.T) templateRefCreateFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.StateDir = t.TempDir()
	cfg.ForceRuntime = "mock"
	projectDir := t.TempDir()
	settingsYAML := `schema_version: "1"
active_profile: local
profiles:
    local:
        runtime: mock
runtimes:
    mock:
        type: mock
`
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "settings.yaml"), []byte(settingsYAML), 0o644))
	tplDir := filepath.Join(projectDir, "templates", "claude")
	require.NoError(t, os.MkdirAll(tplDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tplDir, "scion-agent.yaml"), []byte("harness_config: claude\n"), 0o644))
	hcDir := filepath.Join(projectDir, "harness-configs", "claude")
	require.NoError(t, os.MkdirAll(hcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hcDir, "config.yaml"), []byte("harness: claude\nimage: test-image:claude\n"), 0o644))

	outside := filepath.Join(t.TempDir(), "host-dir")
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "home"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "scion-agent.yaml"), []byte("harness_config: claude\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "home", "secret.txt"), []byte("host file"), 0o644))

	mgr := &envCapturingManager{}
	srv := New(cfg, mgr, &runtime.MockRuntime{NameFunc: func() string { return "mock" }}, config.RuntimeBuildRecord{})
	return templateRefCreateFixture{srv: srv, mgr: mgr, projectDir: projectDir, outsideDir: outside}
}

func (fx templateRefCreateFixture) createBody(t *testing.T, name, template string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"name":        name,
		"id":          "agent-uuid-" + name,
		"projectPath": fx.projectDir,
		"config":      map[string]any{"template": template},
	})
	require.NoError(t, err)
	return body
}

// refusedTemplateRefs are template references a remote create must refuse:
// absolute host directories, relative paths leaving the template
// directories, and remote sources the shared validator does not accept.
func (fx templateRefCreateFixture) refusedTemplateRefs(t *testing.T) map[string]string {
	t.Helper()
	rel, err := filepath.Rel(filepath.Join(fx.projectDir, "templates"), fx.outsideDir)
	require.NoError(t, err)
	return map[string]string{
		"absolute host directory": fx.outsideDir,
		"absolute home directory": filepath.Dir(fx.outsideDir),
		"escaping relative path":  rel,
		"rclone local backend":    ":local:" + fx.outsideDir,
		"archive on metadata IP":  "http://169.254.169.254/latest/x.tar.gz",
		"archive on loopback":     "https://127.0.0.1/x.tgz",
	}
}

func TestCreateAgent_RefusesTemplateRefsOutsideTheTemplateRoots(t *testing.T) {
	fx := newTemplateRefCreateFixture(t)
	for name, ref := range fx.refusedTemplateRefs(t) {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(string(fx.createBody(t, "refused", ref))))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			fx.srv.Handler().ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "template reference not allowed")
			assert.NotContains(t, w.Body.String(), "host file")
		})
	}
	assert.Equal(t, 0, fx.mgr.StartCalls(), "a refused template reference must not start or provision anything")
	_, err := os.Stat(filepath.Join(fx.projectDir, "agents", "refused"))
	assert.True(t, os.IsNotExist(err), "no agent directory is created for a refused reference")

	// A template name inside the template directories is still accepted.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(string(fx.createBody(t, "accepted", "claude"))))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	fx.srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, 1, fx.mgr.StartCalls())
}

// The combined hub-broker server reaches the broker's handlers through the
// control-channel tunnel (serveTunneledRequest), the same path a remote hub
// uses: the refusal applies there too.
func TestCreateAgent_TunneledRequestRefusesTemplateRefsOutsideTheTemplateRoots(t *testing.T) {
	fx := newTemplateRefCreateFixture(t)
	for name, ref := range fx.refusedTemplateRefs(t) {
		t.Run(name, func(t *testing.T) {
			var resp *wsprotocol.ResponseEnvelope
			serveTunneledRequest(context.Background(), fx.srv.Handler(), "", "test.tunnel", slog.Default(), wsprotocol.RequestEnvelope{
				Type:      "request",
				RequestID: "req-" + name,
				Method:    http.MethodPost,
				Path:      "/api/v1/agents",
				Headers:   map[string]string{"Content-Type": "application/json"},
				Body:      fx.createBody(t, "tunneled", ref),
			}, func(r *wsprotocol.ResponseEnvelope) error {
				resp = r
				return nil
			})
			require.NotNil(t, resp)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(resp.Body))
			assert.Contains(t, string(resp.Body), "template reference not allowed")
		})
	}
	assert.Equal(t, 0, fx.mgr.StartCalls())
}
