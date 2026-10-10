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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Tests for ptone/scion#4125: template finalize (the CLI push path) applies
// the scion-agent.yaml telemetry rules of ptone/scion#2093, and a template
// update through the API owns the telemetry it sets.

// pushViaFinalize mimics a CLI push: files are written straight to storage
// (as with signed upload URLs), then finalize is called with a manifest that
// lists exactly the given paths. Files from earlier pushes stay in storage.
func pushViaFinalize(t *testing.T, srv *Server, stor *contentMockStorage, tmpl *store.Template, manifest map[string]string) {
	t.Helper()
	files := make([]store.TemplateFile, 0, len(manifest))
	for path, content := range manifest {
		objectPath := tmpl.StoragePath + "/" + path
		stor.content[objectPath] = []byte(content)
		stor.objects[objectPath] = &storage.Object{Name: objectPath, Size: int64(len(content))}
		files = append(files, store.TemplateFile{Path: path, Size: int64(len(content)), Hash: "sha256:placeholder"})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	body, err := json.Marshal(FinalizeRequest{Manifest: &TemplateManifest{Version: "1", Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/templates/"+tmpl.ID+"/finalize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testDevToken)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("finalize: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// putTemplateTelemetry replaces the template through PUT
// /api/v1/templates/{id}, changing only Config.Telemetry. The body also
// claims the file-sourced marker, which the server must ignore.
func putTemplateTelemetry(t *testing.T, srv *Server, s store.Store, id string, telemetry *api.TelemetryConfig) {
	t.Helper()
	tmpl, err := s.GetTemplate(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Config == nil {
		tmpl.Config = &store.TemplateConfig{}
	}
	tmpl.Config.Telemetry = telemetry
	tmpl.TelemetrySource = store.TemplateTelemetrySourceAgentConfig
	body, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/templates/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testDevToken)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("put: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func telemetryEnabled(tc *api.TelemetryConfig) *bool {
	if tc == nil {
		return nil
	}
	return tc.Enabled
}

func requireTelemetry(t *testing.T, s store.Store, id string, wantEnabled bool, wantSource string) {
	t.Helper()
	got := storedTemplateTelemetry(t, s, id)
	if e := telemetryEnabled(got); e == nil || *e != wantEnabled {
		t.Fatalf("expected telemetry enabled=%v, got %+v", wantEnabled, got)
	}
	if src := storedTelemetrySource(t, s, id); src != wantSource {
		t.Errorf("expected telemetry source %q, got %q", wantSource, src)
	}
}

func requireNoTelemetry(t *testing.T, s store.Store, id string) {
	t.Helper()
	if got := storedTemplateTelemetry(t, s, id); got != nil {
		t.Fatalf("expected telemetry to be cleared, got %+v", got)
	}
	if src := storedTelemetrySource(t, s, id); src != "" {
		t.Errorf("expected no telemetry source, got %q", src)
	}
}

const fileSourced = store.TemplateTelemetrySourceAgentConfig

func TestTemplateFinalize_YAMLFillsUnsetTelemetry(t *testing.T) {
	srv, s, stor := testTemplateFileServer(t)
	tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})

	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff})

	requireTelemetry(t, s, tmpl.ID, false, fileSourced)
	if got := storedTemplateTelemetry(t, s, tmpl.ID); got.Cloud == nil || got.Cloud.Endpoint != "otel.example.com:4317" {
		t.Errorf("expected cloud endpoint from the YAML, got %+v", got.Cloud)
	}
}

func TestTemplateFinalize_JSONTelemetryWins(t *testing.T) {
	srv, s, stor := testTemplateFileServer(t)
	tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
	setJSONTelemetry(t, s, tmpl, true)

	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff})

	requireTelemetry(t, s, tmpl.ID, true, "")
}

func TestTemplateFinalize_LaterPushFollowsYAML(t *testing.T) {
	srv, s, stor := testTemplateFileServer(t)
	tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})

	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff})
	requireTelemetry(t, s, tmpl.ID, false, fileSourced)

	// A changed block replaces file-sourced telemetry.
	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOn})
	requireTelemetry(t, s, tmpl.ID, true, fileSourced)
	if got := storedTemplateTelemetry(t, s, tmpl.ID); got.Cloud != nil {
		t.Errorf("expected the old cloud block to be gone, got %+v", got.Cloud)
	}

	// A push without a block clears it.
	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
	requireNoTelemetry(t, s, tmpl.ID)
}

func TestTemplateFinalize_LaterPushKeepsJSONTelemetry(t *testing.T) {
	srv, s, stor := testTemplateFileServer(t)
	tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlTelemetryOn})
	// Equal to the stored file's block, but set through the API.
	setJSONTelemetry(t, s, tmpl, true)

	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlNoTelemetry})

	requireTelemetry(t, s, tmpl.ID, true, "")
	full, err := s.GetTemplate(context.Background(), tmpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Config.Model != "opus" {
		t.Errorf("other config fields changed: %+v", full.Config)
	}
}

// The CLI drops a removed file from the manifest without deleting it from
// storage; a manifest without scion-agent.yaml counts as no telemetry block.
func TestTemplateFinalize_ManifestWithoutAgentConfigClearsYAMLTelemetry(t *testing.T) {
	srv, s, stor := testTemplateFileServer(t)
	tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})

	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff, "home/.bashrc": "x"})
	requireTelemetry(t, s, tmpl.ID, false, fileSourced)

	// The old scion-agent.yaml (with telemetry) stays in storage.
	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"home/.bashrc": "y"})
	requireNoTelemetry(t, s, tmpl.ID)
}

func TestTemplateFinalize_UnparseableKeepsTelemetry(t *testing.T) {
	srv, s, stor := testTemplateFileServer(t)
	tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})

	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff})
	pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": "telemetry: [unclosed"})

	requireTelemetry(t, s, tmpl.ID, false, fileSourced)
}

// File uploads and finalize share one marker, so either path can clear what
// the other filled.
func TestTemplateFinalize_MixedPaths(t *testing.T) {
	t.Run("upload fills, finalize clears", func(t *testing.T) {
		srv, s, stor := testTemplateFileServer(t)
		tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
		templateUploadPaths["raw write"](t, srv, tmpl.ID, yamlTelemetryOff)
		requireTelemetry(t, s, tmpl.ID, false, fileSourced)

		pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
		requireNoTelemetry(t, s, tmpl.ID)
	})
	t.Run("finalize fills, upload clears", func(t *testing.T) {
		srv, s, stor := testTemplateFileServer(t)
		tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
		pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff})
		requireTelemetry(t, s, tmpl.ID, false, fileSourced)

		templateUploadPaths["raw write"](t, srv, tmpl.ID, yamlNoTelemetry)
		requireNoTelemetry(t, s, tmpl.ID)
	})
}

func TestTemplatePut_TelemetrySource(t *testing.T) {
	t.Run("changed telemetry becomes API-set", func(t *testing.T) {
		srv, s, stor := testTemplateFileServer(t)
		tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
		pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff})

		on := true
		putTemplateTelemetry(t, srv, s, tmpl.ID, &api.TelemetryConfig{Enabled: &on})
		requireTelemetry(t, s, tmpl.ID, true, "")

		// A later push without a block keeps the API-set value.
		pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
		requireTelemetry(t, s, tmpl.ID, true, "")
	})
	t.Run("echoed telemetry stays file-sourced", func(t *testing.T) {
		srv, s, stor := testTemplateFileServer(t)
		tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
		pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlTelemetryOff})

		putTemplateTelemetry(t, srv, s, tmpl.ID, storedTemplateTelemetry(t, s, tmpl.ID))
		requireTelemetry(t, s, tmpl.ID, false, fileSourced)

		pushViaFinalize(t, srv, stor, tmpl, map[string]string{"scion-agent.yaml": yamlNoTelemetry})
		requireNoTelemetry(t, s, tmpl.ID)
	})
	t.Run("body cannot claim the marker", func(t *testing.T) {
		srv, s, stor := testTemplateFileServer(t)
		tmpl := createTestTemplate(t, s, stor, map[string]string{"scion-agent.yaml": yamlNoTelemetry})

		on := true
		putTemplateTelemetry(t, srv, s, tmpl.ID, &api.TelemetryConfig{Enabled: &on})
		requireTelemetry(t, s, tmpl.ID, true, "")
	})
}
