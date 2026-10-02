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
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	harnessFS "github.com/GoogleCloudPlatform/scion/harnesses"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/harness"
)

// TestProvisionAgent_GeminiCLIDefaultModel is a regression test for
// ptone/scion#2674: the gemini-cli harness used to pin model.name in its
// image settings.json and declared no default model in config.yaml, so an
// agent started without --model never resolved through model_aliases. It
// provisions against the real, embedded harnesses/gemini-cli/config.yaml so
// a future edit that drops the default model or the alias fails here.
func TestProvisionAgent_GeminiCLIDefaultModel(t *testing.T) {
	aliases := harness.DefaultModelAliases("gemini-cli")
	if aliases["medium"] == "" || aliases["large"] == "" {
		t.Fatalf("gemini-cli model_aliases missing medium/large: %+v", aliases)
	}

	tests := []struct {
		name   string
		inline *api.ScionConfig
		want   string
	}{
		{name: "no model resolves the medium alias", want: aliases["medium"]},
		{name: "explicit alias wins", inline: &api.ScionConfig{Model: "large"}, want: aliases["large"]},
		{name: "explicit concrete model wins", inline: &api.ScionConfig{Model: "gemini-custom-preview"}, want: "gemini-custom-preview"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			oldWd, _ := os.Getwd()
			_ = os.Chdir(tmpDir)
			defer func() { _ = os.Chdir(oldWd) }()
			t.Setenv("HOME", tmpDir)

			globalScionDir := filepath.Join(tmpDir, ".scion")
			hcDir := filepath.Join(globalScionDir, "harness-configs", "gemini-cli")
			if err := os.MkdirAll(hcDir, 0755); err != nil {
				t.Fatal(err)
			}
			stageEmbeddedHarnessBundle(t, "gemini-cli", hcDir)
			tplDir := filepath.Join(globalScionDir, "templates", "gem-tpl")
			if err := os.MkdirAll(tplDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tplDir, "scion-agent.json"),
				[]byte(`{"default_harness_config": "gemini-cli"}`), 0644); err != nil {
				t.Fatal(err)
			}
			projectScionDir := filepath.Join(tmpDir, "project", ".scion")
			if err := os.MkdirAll(projectScionDir, 0755); err != nil {
				t.Fatal(err)
			}

			var inline []*api.ScionConfig
			if tc.inline != nil {
				inline = append(inline, tc.inline)
			}
			_, _, cfg, err := ProvisionAgent(context.Background(), "gem-agent", "gem-tpl", "", "",
				projectScionDir, "", "", "", "", inline...)
			if err != nil {
				t.Fatalf("ProvisionAgent failed: %v", err)
			}
			if cfg.Model != tc.want {
				t.Errorf("cfg.Model = %q, want %q", cfg.Model, tc.want)
			}
		})
	}
}

// stageEmbeddedHarnessBundle copies the embedded harnesses/<name> bundle
// (config.yaml, provision.py, home/, ...) into dst, plus the canonical
// scion_harness.py when the bundle vendors it via go generate.
func stageEmbeddedHarnessBundle(t *testing.T, name, dst string) {
	t.Helper()
	err := fs.WalkDir(harnessFS.FS, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(name, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := fs.ReadFile(harnessFS.FS, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		t.Fatalf("stage embedded %s bundle: %v", name, err)
	}
	lib := filepath.Join(dst, "scion_harness.py")
	if _, err := os.Stat(lib); os.IsNotExist(err) {
		if err := os.WriteFile(lib, harnessFS.CanonicalHarnessLib, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
