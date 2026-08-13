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
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// newOverlayTestServer creates a minimal broker Server with a temp home and
// settings.yaml so that resolveEffectiveSettings can load file-based values.
func newOverlayTestServer(t *testing.T, settingsYAML string) *Server {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Isolate from repo .scion
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	dotScion := filepath.Join(tmpDir, ".scion")
	if err := os.Mkdir(dotScion, 0755); err != nil {
		t.Fatal(err)
	}
	if settingsYAML != "" {
		if err := os.WriteFile(filepath.Join(dotScion, "settings.yaml"), []byte(settingsYAML), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return &Server{}
}

func TestResolveEffectiveSettings_NoOverlay(t *testing.T) {
	settings := `schema_version: "1"
runtimes:
    docker:
        type: docker
profiles:
    default:
        runtime: docker
image_registry: "from-file"
`
	srv := newOverlayTestServer(t, settings)

	vs, _, err := srv.resolveEffectiveSettings("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vs == nil {
		t.Fatal("expected non-nil VersionedSettings")
	}

	// Without overlay, file values should be returned (merged with defaults)
	if _, ok := vs.Runtimes["docker"]; !ok {
		t.Error("Runtimes should contain 'docker' from file")
	}
	if vs.Runtimes["docker"].Type != "docker" {
		t.Errorf("Runtimes[docker].Type: want docker, got %s", vs.Runtimes["docker"].Type)
	}
	if _, ok := vs.Profiles["default"]; !ok {
		t.Error("Profiles should contain 'default' from file")
	}
	if vs.ImageRegistry != "from-file" {
		t.Errorf("ImageRegistry: want from-file, got %s", vs.ImageRegistry)
	}
}

func TestResolveEffectiveSettings_WithOverlay(t *testing.T) {
	settings := `schema_version: "1"
runtimes:
    docker:
        type: docker
profiles:
    default:
        runtime: docker
image_registry: "from-file"
`
	srv := newOverlayTestServer(t, settings)

	// Set an overlay that overrides all sections
	srv.SetSettingsOverlay(SettingsOverlay{
		Runtimes: map[string]config.V1RuntimeConfig{
			"cloudrun": {Type: "cloudrun_instances", CloudRun: &config.V1CloudRunConfig{Project: "db-project", Region: "us-east1"}},
		},
		Profiles: map[string]config.V1ProfileConfig{
			"production": {Runtime: "cloudrun"},
		},
		HarnessConfigs: map[string]config.HarnessConfigEntry{
			"claude": {Harness: "claude", Image: "gcr.io/test/claude:v1"},
		},
		ImageRegistry: "gcr.io/db-registry",
	})

	vs, _, err := srv.resolveEffectiveSettings("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Overlay should replace file values
	if len(vs.Runtimes) != 1 {
		t.Fatalf("Runtimes: want 1, got %d", len(vs.Runtimes))
	}
	if _, ok := vs.Runtimes["cloudrun"]; !ok {
		t.Fatal("Runtimes should contain 'cloudrun', not 'docker'")
	}
	if vs.Runtimes["cloudrun"].CloudRun == nil || vs.Runtimes["cloudrun"].CloudRun.Project != "db-project" {
		t.Errorf("Runtimes[cloudrun].CloudRun.Project: want db-project")
	}

	if len(vs.Profiles) != 1 {
		t.Fatalf("Profiles: want 1, got %d", len(vs.Profiles))
	}
	if _, ok := vs.Profiles["production"]; !ok {
		t.Fatal("Profiles should contain 'production', not 'default'")
	}

	if len(vs.HarnessConfigs) != 1 {
		t.Fatalf("HarnessConfigs: want 1 (from overlay), got %d", len(vs.HarnessConfigs))
	}

	if vs.ImageRegistry != "gcr.io/db-registry" {
		t.Errorf("ImageRegistry: want gcr.io/db-registry, got %s", vs.ImageRegistry)
	}
}

func TestResolveEffectiveSettings_NilOverlayMapsPreserveFileValues(t *testing.T) {
	settings := `schema_version: "1"
runtimes:
    docker:
        type: docker
profiles:
    default:
        runtime: docker
`
	srv := newOverlayTestServer(t, settings)

	// Overlay with nil maps — should keep file values
	srv.SetSettingsOverlay(SettingsOverlay{
		Runtimes:       nil,
		Profiles:       nil,
		HarnessConfigs: nil,
		ImageRegistry:  "", // empty = no DB override
	})

	vs, _, err := srv.resolveEffectiveSettings("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File values should be preserved (merged with defaults)
	if _, ok := vs.Runtimes["docker"]; !ok {
		t.Error("Runtimes should still contain 'docker' from file")
	}
	if _, ok := vs.Profiles["default"]; !ok {
		t.Error("Profiles should still contain 'default' from file")
	}
}

func TestResolveEffectiveSettings_ImageRegistryEnvVarPrecedence(t *testing.T) {
	settings := `schema_version: "1"
`
	srv := newOverlayTestServer(t, settings)

	// Set env var — should win over DB overlay
	t.Setenv("SCION_IMAGE_REGISTRY", "from-env")

	srv.SetSettingsOverlay(SettingsOverlay{
		ImageRegistry: "from-db",
	})

	vs, _, err := srv.resolveEffectiveSettings("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The env var was already baked into vs.ImageRegistry by
	// LoadEffectiveSettings. The overlay should NOT overwrite it.
	if vs.ImageRegistry != "from-env" {
		t.Errorf("ImageRegistry: want from-env (env var wins), got %s", vs.ImageRegistry)
	}
}

func TestResolveEffectiveSettings_ImageRegistryDBWinsWithoutEnv(t *testing.T) {
	settings := `schema_version: "1"
image_registry: "from-file"
`
	srv := newOverlayTestServer(t, settings)

	// Ensure env var is NOT set
	t.Setenv("SCION_IMAGE_REGISTRY", "")
	os.Unsetenv("SCION_IMAGE_REGISTRY")

	srv.SetSettingsOverlay(SettingsOverlay{
		ImageRegistry: "from-db",
	})

	vs, _, err := srv.resolveEffectiveSettings("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if vs.ImageRegistry != "from-db" {
		t.Errorf("ImageRegistry: want from-db (DB wins over file), got %s", vs.ImageRegistry)
	}
}

func TestSetSettingsOverlay_AtomicUpdate(t *testing.T) {
	srv := &Server{}

	// Initially nil
	if overlay := srv.settingsOverlay.Load(); overlay != nil {
		t.Fatal("initial overlay should be nil")
	}

	// Set overlay
	srv.SetSettingsOverlay(SettingsOverlay{
		Runtimes: map[string]config.V1RuntimeConfig{
			"docker": {Type: "docker"},
		},
	})

	overlay := srv.settingsOverlay.Load()
	if overlay == nil {
		t.Fatal("overlay should not be nil after SetSettingsOverlay")
	}
	if len(overlay.Runtimes) != 1 {
		t.Errorf("overlay.Runtimes: want 1, got %d", len(overlay.Runtimes))
	}

	// Update overlay
	srv.SetSettingsOverlay(SettingsOverlay{
		Runtimes: map[string]config.V1RuntimeConfig{
			"cloudrun": {Type: "cloudrun_instances"},
			"docker":   {Type: "docker"},
		},
	})

	overlay = srv.settingsOverlay.Load()
	if len(overlay.Runtimes) != 2 {
		t.Errorf("overlay.Runtimes after update: want 2, got %d", len(overlay.Runtimes))
	}
}
