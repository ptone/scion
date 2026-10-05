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
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

func TestPickImage_OrderAndReplacementLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	img, src := pickImage(logger, "a1", []imageCandidate{
		{source: imageTierHarnessConfigFile, image: "file:1"},
		{source: imageTierSettings, image: ""},
		{source: imageTierTemplate, image: "tpl:2"},
		{source: imageTierInline, image: "tpl:2"}, // same image: not a replacement
		{source: imageTierProfileOverride, image: "profile:3"},
		{source: imageTierRequest, image: ""},
	})
	if img != "profile:3" || src != imageTierProfileOverride {
		t.Fatalf("pickImage = (%q, %q), want (profile:3, %s)", img, src, imageTierProfileOverride)
	}
	out := buf.String()
	if got := strings.Count(out, "lower-tier image replaced"); got != 2 {
		t.Fatalf("expected 2 replacement log lines (file->template, template->profile), got %d:\n%s", got, out)
	}
	for _, want := range []string{"replaced_image=file:1", "replaced_image=tpl:2", "image=profile:3", `"profile harness_overrides outranks inline config"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q:\n%s", want, out)
		}
	}
}

func TestPickImage_NoCandidates(t *testing.T) {
	img, src := pickImage(nil, "a1", []imageCandidate{{source: imageTierTemplate}})
	if img != "" || src != "" {
		t.Fatalf("pickImage = (%q, %q), want empty", img, src)
	}
}

// imagePrecedenceFixture writes a global .scion with a harness-config file
// default, a template with templateImage (omitted when empty), and the given
// settings.yaml, and returns the project .scion dir.
func imagePrecedenceFixture(t *testing.T, templateImage, settingsYAML string) string {
	t.Helper()
	tmpDir := t.TempDir()

	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	t.Setenv("HOME", tmpDir)

	globalScionDir := filepath.Join(tmpDir, ".scion")
	hcDir := filepath.Join(globalScionDir, "harness-configs", "test-harness")
	if err := os.MkdirAll(hcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hcDir, "config.yaml"), []byte("harness: generic\nuser: scion\nimage: file-default:latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tplDir := filepath.Join(globalScionDir, "templates", "default")
	if err := os.MkdirAll(tplDir, 0755); err != nil {
		t.Fatal(err)
	}
	tplJSON := `{"default_harness_config": "test-harness"}`
	if templateImage != "" {
		tplJSON = `{"default_harness_config": "test-harness", "image": "` + templateImage + `"}`
	}
	if err := os.WriteFile(filepath.Join(tplDir, "scion-agent.json"), []byte(tplJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalScionDir, "settings.yaml"), []byte(settingsYAML), 0644); err != nil {
		t.Fatal(err)
	}
	projectScionDir := filepath.Join(tmpDir, "project", ".scion")
	if err := os.MkdirAll(projectScionDir, 0755); err != nil {
		t.Fatal(err)
	}
	return projectScionDir
}

// startCapturingImage runs Start against a mock runtime and returns the
// image handed to the runtime plus everything logged through slog.Default.
func startCapturingImage(t *testing.T, opts api.StartOptions) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

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
	opts.BrokerMode = true
	opts.NoAuth = true
	if _, err := NewManager(mockRT).Start(context.Background(), opts); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	return captured.Image, buf.String()
}

const profileOverrideSettings = `schema_version: "1"
active_profile: staging
profiles:
  staging:
    runtime: docker
    harness_overrides:
      test-harness:
        image: profile-pinned:v4
harness_configs:
  test-harness:
    harness: generic
    image: settings-pinned:v1
`

// TestStart_ExplicitProfileOverrideImageBeatsTemplateImage pins
// ptone/scion#1799: an explicitly set profiles.<p>.harness_overrides.<hc>.image
// outranks a template's image, and the replacement is logged at Info.
func TestStart_ExplicitProfileOverrideImageBeatsTemplateImage(t *testing.T) {
	projectScionDir := imagePrecedenceFixture(t, "template-pinned:v2", profileOverrideSettings)

	img, logs := startCapturingImage(t, api.StartOptions{
		Name:        "test-agent",
		ProjectPath: projectScionDir,
		Profile:     "staging",
	})
	if img != "profile-pinned:v4" {
		t.Fatalf("expected the explicit profile override to beat the template image, got %q", img)
	}
	if !strings.Contains(logs, "lower-tier image replaced") ||
		!strings.Contains(logs, "replaced_image=template-pinned:v2") ||
		!strings.Contains(logs, "image=profile-pinned:v4") ||
		!strings.Contains(logs, "level=INFO") {
		t.Errorf("expected an Info log naming the replaced template image, got:\n%s", logs)
	}
}

// TestStart_PlainSettingsImageDoesNotBeatTemplateImage pins the other half
// of the ptone/scion#1799 decision: the plain harness_configs.<hc>.image
// default does NOT gain priority over a template image.
func TestStart_PlainSettingsImageDoesNotBeatTemplateImage(t *testing.T) {
	projectScionDir := imagePrecedenceFixture(t, "template-pinned:v2", `schema_version: "1"
active_profile: local
profiles:
  local:
    runtime: docker
harness_configs:
  test-harness:
    harness: generic
    image: settings-pinned:v1
`)
	img, logs := startCapturingImage(t, api.StartOptions{
		Name:        "test-agent",
		ProjectPath: projectScionDir,
	})
	if img != "template-pinned:v2" {
		t.Fatalf("expected the template image to beat the plain settings default, got %q", img)
	}
	if !strings.Contains(logs, "replaced_image=settings-pinned:v1") {
		t.Errorf("expected an Info log naming the replaced settings image, got:\n%s", logs)
	}
}

// TestStart_ExplicitProfileOverrideImageBeatsInlineImage: the inline config
// tier also ranks below an explicit profile override.
func TestStart_ExplicitProfileOverrideImageBeatsInlineImage(t *testing.T) {
	projectScionDir := imagePrecedenceFixture(t, "template-pinned:v2", profileOverrideSettings)

	img, _ := startCapturingImage(t, api.StartOptions{
		Name:         "test-agent",
		ProjectPath:  projectScionDir,
		Profile:      "staging",
		InlineConfig: &api.ScionConfig{Image: "inline-pinned:v3"},
	})
	if img != "profile-pinned:v4" {
		t.Fatalf("expected the explicit profile override to beat the inline image, got %q", img)
	}
}

// TestStart_RequestImageBeatsProfileOverrideAndTemplate: an explicit
// --image / request image (opts.Image) still beats everything.
func TestStart_RequestImageBeatsProfileOverrideAndTemplate(t *testing.T) {
	projectScionDir := imagePrecedenceFixture(t, "template-pinned:v2", profileOverrideSettings)

	img, logs := startCapturingImage(t, api.StartOptions{
		Name:         "test-agent",
		ProjectPath:  projectScionDir,
		Profile:      "staging",
		Image:        "flag-pinned:v9",
		InlineConfig: &api.ScionConfig{Image: "inline-pinned:v3"},
	})
	if img != "flag-pinned:v9" {
		t.Fatalf("expected --image to beat every other tier, got %q", img)
	}
	if !strings.Contains(logs, "replaced_image=profile-pinned:v4") {
		t.Errorf("expected an Info log naming the replaced profile image, got:\n%s", logs)
	}
}
