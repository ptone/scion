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

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// TestProvisionAgent_RecordsHarnessConfigSource pins the ptone/scion#620
// provenance: agent info records whether the harness-config came from the
// hub-hydrated copy or a broker-local directory.
func TestProvisionAgent_RecordsHarnessConfigSource(t *testing.T) {
	mockRuntimeForTest(t)
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	t.Setenv("HOME", tmpDir)

	if err := config.InitMachine(getTestHarnesses()); err != nil {
		t.Fatalf("InitMachine failed: %v", err)
	}
	projectDir := filepath.Join(tmpDir, "project")
	projectScionDir := filepath.Join(projectDir, ".scion")
	if err := config.InitProject(projectScionDir, getTestHarnesses()); err != nil {
		t.Fatalf("InitProject failed: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}

	// Unstamped: resolves from the broker's own disk.
	_, _, cfg, err := ProvisionAgent(context.Background(), "local-src", "default", "", "claude", projectScionDir, "", "", "", "")
	if err != nil {
		t.Fatalf("ProvisionAgent (local) failed: %v", err)
	}
	if cfg.Info == nil || cfg.Info.HarnessConfigSource != string(config.HarnessConfigSourceBrokerLocal) {
		t.Errorf("local provision: HarnessConfigSource = %q, want %q", infoSource(cfg), config.HarnessConfigSourceBrokerLocal)
	}

	// Hub-hydrated: the dispatch context carries the hydrated path.
	hcDir, err := config.FindHarnessConfigDir("claude", projectScionDir)
	if err != nil {
		t.Fatalf("FindHarnessConfigDir: %v", err)
	}
	hydrated := filepath.Join(t.TempDir(), "claude")
	if err := os.CopyFS(hydrated, os.DirFS(hcDir.Path)); err != nil {
		t.Fatalf("copy harness-config: %v", err)
	}
	ctx := api.ContextWithHarnessConfigPath(context.Background(), hydrated)
	_, _, cfg, err = ProvisionAgent(ctx, "hub-src", "default", "", "claude", projectScionDir, "", "", "", "")
	if err != nil {
		t.Fatalf("ProvisionAgent (hydrated) failed: %v", err)
	}
	if cfg.Info == nil || cfg.Info.HarnessConfigSource != string(config.HarnessConfigSourceHubHydrated) {
		t.Errorf("hydrated provision: HarnessConfigSource = %q, want %q", infoSource(cfg), config.HarnessConfigSourceHubHydrated)
	}
}

func infoSource(cfg *api.ScionConfig) string {
	if cfg == nil || cfg.Info == nil {
		return "<nil info>"
	}
	return cfg.Info.HarnessConfigSource
}
