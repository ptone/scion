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
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// A legacy agent (no broker-side provenance record) falls back to the
// template named in agent-info.json in its home, which the agent can write.
// That name is resolved as a remote reference: an absolute directory or a
// relative path leaving the template directories never becomes the template
// whose bundled harness-config Start resolves.
func TestStart_LegacyAgentInfoTemplateIsNeverAHostDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("HOME", tmpDir)

	globalScionDir := filepath.Join(tmpDir, ".scion")
	writeBundled := func(tplDir, user string) string {
		hcDir := filepath.Join(tplDir, "harness-configs", "test-harness")
		if err := os.MkdirAll(hcDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hcDir, "config.yaml"), []byte("harness: generic\nuser: "+user+"\nimage: "+user+"-image:v1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tplDir, "scion-agent.json"), []byte(`{"default_harness_config": "test-harness"}`), 0644); err != nil {
			t.Fatal(err)
		}
		return config.ComputeHarnessConfigRevision(hcDir)
	}
	alpha := writeBundled(filepath.Join(globalScionDir, "templates", "alpha"), "alpha-user")
	outsideDir := filepath.Join(tmpDir, "outside-template")
	outside := writeBundled(outsideDir, "outside-user")
	if err := os.WriteFile(filepath.Join(globalScionDir, "settings.yaml"), []byte("schema_version: \"1\"\nactive_profile: local\nprofiles:\n  local:\n    runtime: docker\n"), 0644); err != nil {
		t.Fatal(err)
	}
	projectScionDir := filepath.Join(tmpDir, "project", ".scion")
	if err := os.MkdirAll(projectScionDir, 0755); err != nil {
		t.Fatal(err)
	}

	_, info := startCapturingRun(t, api.StartOptions{Name: "test-agent", Template: "alpha", ProjectPath: projectScionDir})
	if info == nil || info.HarnessConfigRevision != alpha {
		t.Fatalf("first start: harness-config revision = %v, want alpha's %q", info, alpha)
	}
	// Make it a legacy agent: no provenance record.
	if err := os.Remove(filepath.Join(projectScionDir, "agents", "test-agent", imageProvenanceFile)); err != nil {
		t.Fatal(err)
	}

	rel, err := filepath.Rel(filepath.Join(globalScionDir, "templates"), outsideDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{outsideDir, rel} {
		rewriteAgentInfo(t, projectScionDir, func(raw map[string]any) { raw["template"] = ref })
		// A restart that names the template, as a local start can: the
		// legacy agent's recorded name then chooses the template whose
		// bundled harness-config is resolved.
		// Refusing the name can leave the start without a harness-config
		// image, so a start error is acceptable here; loading the
		// outside directory's bundle is not.
		var captured runtime.RunConfig
		mockRT := &runtime.MockRuntime{
			ListFunc: func(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
				return []api.AgentInfo{}, nil
			},
			RunFunc: func(ctx context.Context, cfg runtime.RunConfig) (string, error) {
				captured = cfg
				return "mock-id", nil
			},
		}
		info, _ := NewManager(mockRT).Start(context.Background(), api.StartOptions{
			Name: "test-agent", Template: "alpha", ProjectPath: projectScionDir, BrokerMode: true, NoAuth: true,
		})
		if info != nil && info.HarnessConfigRevision == outside {
			t.Fatalf("agent-info.json template %q loaded the harness-config bundled in a directory outside the template roots", ref)
		}
		if captured.Image == "outside-user-image:v1" {
			t.Fatalf("agent-info.json template %q selected the image of a directory outside the template roots", ref)
		}
	}
}
