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

// uninterceptedMetadataOptOutYAML is the runtime.allow_unintercepted_metadata
// opt-out with the cloud acknowledgement, as a settings file fragment.
const uninterceptedMetadataOptOutYAML = "runtime:\n  allow_unintercepted_metadata:\n    enabled: true\n    acknowledge_cloud_host: true\n"

// startUninterceptedMetadataFixture starts a block-mode agent on bridge
// networking through the real Manager.Start on a mock runtime named
// runtimeName. globalExtra is appended to the global settings file and
// projectSettings, when set, is written as the project's settings file.
// extraEnv is added to the start's env. It returns the RunConfig the runtime
// received.
func startUninterceptedMetadataFixture(t *testing.T, runtimeName, globalExtra, projectSettings string, extraEnv map[string]string) (string, *runtime.RunConfig) {
	t.Helper()
	t.Setenv(runtime.ForceHostNetworkEnvVar, "")
	projectScionDir, _ := startTZFixture(t, "", `""`)

	if globalExtra != "" {
		globalSettings := filepath.Join(os.Getenv("HOME"), ".scion", "settings.yaml")
		f, err := os.OpenFile(globalSettings, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(globalExtra); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if projectSettings != "" {
		if err := os.WriteFile(filepath.Join(projectScionDir, "settings.yaml"), []byte(projectSettings), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := map[string]string{
		"SCION_AGENT_ID":      "agent-uuid-unintercepted",
		"SCION_HUB_ENDPOINT":  "https://hub.example.com",
		"SCION_METADATA_MODE": "block",
	}
	for k, v := range extraEnv {
		env[k] = v
	}

	var got *runtime.RunConfig
	rt := &runtime.MockRuntime{
		NameFunc: func() string { return runtimeName },
		ListFunc: func(ctx context.Context, labelFilter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{}, nil
		},
		RunFunc: func(ctx context.Context, cfg runtime.RunConfig) (string, error) {
			c := cfg
			got = &c
			return "mock-id", nil
		},
	}
	_, err := NewManager(rt).Start(context.Background(), api.StartOptions{
		Name:           "tz-agent",
		ProjectPath:    projectScionDir,
		BrokerMode:     true,
		NoAuth:         true,
		HubIssuedToken: "fake.unintercepted.hub.token",
		Env:            env,
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if got == nil {
		t.Fatal("the runtime's Run was not called")
	}
	if !got.MetadataInterception {
		t.Fatalf("MetadataInterception = false, want true for block on bridge (fixture precondition)")
	}
	return projectScionDir, got
}

// TestStart_UninterceptedMetadataOptOutFromGlobalSettings checks that the
// operator's opt-out in the broker's global settings reaches RunConfig.
func TestStart_UninterceptedMetadataOptOutFromGlobalSettings(t *testing.T) {
	_, cfg := startUninterceptedMetadataFixture(t, "podman", uninterceptedMetadataOptOutYAML, "", nil)
	if !cfg.AllowUninterceptedMetadata || !cfg.UninterceptedMetadataCloudAck {
		t.Errorf("opt-out = (%v, %v), want (true, true) from global settings",
			cfg.AllowUninterceptedMetadata, cfg.UninterceptedMetadataCloudAck)
	}
}

// TestStart_UninterceptedMetadataOptOutNotFromProjectOrCreator checks that a
// project settings file, or agent env from the start's creator, cannot set
// the opt-out: it is operator-only.
func TestStart_UninterceptedMetadataOptOutNotFromProjectOrCreator(t *testing.T) {
	projectScionDir, cfg := startUninterceptedMetadataFixture(t, "podman", "",
		"schema_version: \"1\"\n"+uninterceptedMetadataOptOutYAML,
		map[string]string{
			"SCION_RUNTIME_ALLOW_UNINTERCEPTED_METADATA_ENABLED":                "true",
			"SCION_RUNTIME_ALLOW_UNINTERCEPTED_METADATA_ACKNOWLEDGE_CLOUD_HOST": "true",
		})
	if cfg.AllowUninterceptedMetadata || cfg.UninterceptedMetadataCloudAck {
		t.Errorf("opt-out = (%v, %v), want (false, false): project settings and agent env must not enable it",
			cfg.AllowUninterceptedMetadata, cfg.UninterceptedMetadataCloudAck)
	}

	// The project's value does reach the project-merged settings, so the
	// check above exercises a real project-level opt-out.
	merged, _, err := config.LoadEffectiveSettings(projectScionDir)
	if err != nil {
		t.Fatalf("LoadEffectiveSettings: %v", err)
	}
	if enabled, ack := config.UninterceptedMetadataOptOut(merged); !enabled || !ack {
		t.Fatalf("project-merged opt-out = (%v, %v), want (true, true) (fixture precondition)", enabled, ack)
	}
}

// TestStart_UninterceptedMetadataOptOutOnlyForPodman checks that other
// runtimes, which do not read the opt-out, get it unset.
func TestStart_UninterceptedMetadataOptOutOnlyForPodman(t *testing.T) {
	_, cfg := startUninterceptedMetadataFixture(t, "docker", uninterceptedMetadataOptOutYAML, "", nil)
	if cfg.AllowUninterceptedMetadata || cfg.UninterceptedMetadataCloudAck {
		t.Errorf("opt-out = (%v, %v) on docker, want (false, false)",
			cfg.AllowUninterceptedMetadata, cfg.UninterceptedMetadataCloudAck)
	}
}

func TestUninterceptedMetadataOptOut_GlobalLoadErrorFailsClosed(t *testing.T) {
	orig := loadOperatorSettings
	loadOperatorSettings = func() (*config.VersionedSettings, []string, error) {
		return nil, nil, os.ErrPermission
	}
	t.Cleanup(func() { loadOperatorSettings = orig })
	if enabled, ack := uninterceptedMetadataOptOut("a1", true); enabled || ack {
		t.Errorf("opt-out = (%v, %v) after a load error, want (false, false)", enabled, ack)
	}
}
