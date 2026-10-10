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
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// Tests for ptone/scion#4241 (TS-2c): broker settings.yaml telemetry is a
// per-start tier and is never persisted into an agent's scion-agent.json,
// neither by ProvisionAgent nor by Start's scion-agent.json rewrites.
//
// These drive the real create path (ProvisionAgent) and then Start, so a
// settings value baked in at provision time would show up as agent config.

// provisionSettingsTelemetryAgent provisions name from templateName into the
// project returned by hubTelemetryFixture and returns the agent dir.
func provisionSettingsTelemetryAgent(t *testing.T, projectScionDir, name, templateName string) string {
	t.Helper()
	mockRuntimeForTest(t)
	if _, _, _, err := ProvisionAgent(context.Background(), name, templateName, "", "", projectScionDir, "", "", "", ""); err != nil {
		t.Fatalf("ProvisionAgent failed: %v", err)
	}
	return filepath.Join(projectScionDir, "agents", name)
}

// writeTelemetryTemplate adds a global template whose own telemetry block is
// telemetryJSON, on top of the hubTelemetryFixture HOME.
func writeTelemetryTemplate(t *testing.T, name, telemetryJSON string) {
	t.Helper()
	home := os.Getenv("HOME")
	tplDir := filepath.Join(home, ".scion", "templates", name)
	if err := os.MkdirAll(tplDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"default_harness_config": "test-harness", "telemetry": ` + telemetryJSON + `}`
	if err := os.WriteFile(filepath.Join(tplDir, "scion-agent.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
}

func readAgentConfig(t *testing.T, agentDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(agentDir, "scion-agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// assertNoSettingsTelemetry fails if any value from hubTierSettingsTelemetry
// appears in the persisted agent config.
func assertNoSettingsTelemetry(t *testing.T, cfg string, settingsValues ...string) {
	t.Helper()
	for _, v := range settingsValues {
		if strings.Contains(cfg, v) {
			t.Errorf("settings telemetry value %q persisted to scion-agent.json:\n%s", v, cfg)
		}
	}
}

// harnessAuthVertex makes Start rewrite scion-agent.json.
func harnessAuthVertex(o *api.StartOptions) { o.HarnessAuth = "vertex-ai" }

func setSettingsTelemetry(t *testing.T, settingsTelemetry string) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".scion", "settings.yaml")
	if err := os.WriteFile(path, []byte(`schema_version: "1"
active_profile: local
profiles:
  local:
    runtime: docker
`+settingsTelemetry), 0644); err != nil {
		t.Fatal(err)
	}
}

// Acceptance 1: with telemetry configured in settings.yaml, a newly
// provisioned agent's scion-agent.json contains no settings telemetry, and a
// template's own telemetry block is still persisted.
func TestProvision_SettingsTelemetryNotPersisted(t *testing.T) {
	t.Run("no template telemetry", func(t *testing.T) {
		project := hubTelemetryFixture(t, hubTierSettingsTelemetry)
		agentDir := provisionSettingsTelemetryAgent(t, project, "fresh", "default")

		cfg := readAgentConfig(t, agentDir)
		assertNoSettingsTelemetry(t, cfg, "settings:4317", `"http"`, "9s")
		if strings.Contains(cfg, `"telemetry"`) {
			t.Errorf("scion-agent.json has a telemetry block with no template telemetry:\n%s", cfg)
		}
	})
	t.Run("template telemetry kept", func(t *testing.T) {
		project := hubTelemetryFixture(t, hubTierSettingsTelemetry)
		writeTelemetryTemplate(t, "tel-tpl", `{"cloud": {"endpoint": "template:4317"}}`)
		agentDir := provisionSettingsTelemetryAgent(t, project, "fresh-tpl", "tel-tpl")

		cfg := readAgentConfig(t, agentDir)
		if !strings.Contains(cfg, "template:4317") {
			t.Errorf("template telemetry missing from scion-agent.json:\n%s", cfg)
		}
		assertNoSettingsTelemetry(t, cfg, "settings:4317", `"http"`, "9s")
	})
}

// Acceptance 2: on a provisioned agent, Start applies per-field
// settings < hub default < template. Protocol is the regression detector:
// settings sets it and the template does not, so it must come from the hub
// default; a provision-time bake would make the settings value win.
func TestProvisionThenStart_SettingsBelowHubBelowTemplate(t *testing.T) {
	project := hubTelemetryFixture(t, hubTierSettingsTelemetry)
	writeTelemetryTemplate(t, "tel-tpl", `{"cloud": {"endpoint": "template:4317"}}`)
	provisionSettingsTelemetryAgent(t, project, "order", "tel-tpl")

	env, _ := startWithHubTelemetry(t, project, "order", hubTelemetryDefault("hub:4317"), nil)

	assertEnv(t, env, "SCION_OTEL_ENDPOINT", "template:4317")       // template beats hub and settings
	assertEnv(t, env, "SCION_OTEL_PROTOCOL", "grpc")                // hub beats settings
	assertEnv(t, env, "SCION_TELEMETRY_CLOUD_PROVIDER", "gcp")      // hub fills
	assertEnv(t, env, "SCION_TELEMETRY_HUB_REPORT_INTERVAL", "45s") // hub fills
	assertEnv(t, env, "SCION_TELEMETRY_CLOUD_BATCH_TIMEOUT", "9s")  // settings fills below hub
}

// Acceptance 3: a restart after a settings telemetry change sees the new
// value, including when Start rewrites scion-agent.json (HarnessAuth), with
// and without a hub default.
func TestProvisionThenStart_RestartSeesSettingsChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		hub  bool
	}{{"local mode", false}, {"with hub default", true}} {
		t.Run(tc.name, func(t *testing.T) {
			project := hubTelemetryFixture(t, hubTierSettingsTelemetry)
			agentDir := provisionSettingsTelemetryAgent(t, project, "restart", "default")
			start := func() map[string]string {
				hd := hubTelemetryDefault("hub:4317")
				if !tc.hub {
					hd = nil
				}
				env, _ := startWithHubTelemetry(t, project, "restart", hd, harnessAuthVertex)
				return env
			}

			env := start()
			assertEnv(t, env, "SCION_TELEMETRY_CLOUD_BATCH_TIMEOUT", "9s")
			if !tc.hub {
				assertEnv(t, env, "SCION_OTEL_ENDPOINT", "settings:4317")
			}

			cfg := readAgentConfig(t, agentDir)
			if !strings.Contains(cfg, "vertex-ai") {
				t.Fatalf("test precondition: Start did not rewrite scion-agent.json:\n%s", cfg)
			}
			assertNoSettingsTelemetry(t, cfg, "settings:4317", `"http"`, "9s")

			setSettingsTelemetry(t, `telemetry:
  cloud:
    endpoint: settings-b:4317
    protocol: http
    batch:
      timeout: 7s
`)
			env = start()
			assertEnv(t, env, "SCION_TELEMETRY_CLOUD_BATCH_TIMEOUT", "7s")
			if !tc.hub {
				assertEnv(t, env, "SCION_OTEL_ENDPOINT", "settings-b:4317")
			}
			assertNoSettingsTelemetry(t, readAgentConfig(t, agentDir), "settings-b:4317", "7s")
		})
	}
}

// Acceptance 4: local mode (no hub default on the context) still gets the
// full settings telemetry applied at Start on a provisioned agent.
func TestProvisionThenStart_LocalModeGetsSettingsTelemetry(t *testing.T) {
	project := hubTelemetryFixture(t, hubTierSettingsTelemetry)
	provisionSettingsTelemetryAgent(t, project, "local", "default")

	env, _ := startWithHubTelemetry(t, project, "local", nil, nil)

	assertEnv(t, env, "SCION_OTEL_ENDPOINT", "settings:4317")
	assertEnv(t, env, "SCION_OTEL_PROTOCOL", "http")
	assertEnv(t, env, "SCION_TELEMETRY_CLOUD_BATCH_TIMEOUT", "9s")
}
