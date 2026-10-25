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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// A broker-mode start of an agent whose directory already exists (what a
// hub create-with-gather becomes for an agent that was provisioned before,
// ptone/scion#6096) reuses the directory: the persisted scion-agent.json and
// the agent's files are left as they were, and the env the request carries
// (for example as-needed values the hub resolved after the broker's needs
// report) reaches the container. The request env is not persisted, so it
// must arrive with the start that needs it.
func TestBrokerModeStart_ExistingAgentDirIsReusedAndRequestEnvReachesContainer(t *testing.T) {
	projectScionDir := rankProbeFixture(t)
	const name = "existing-dir-agent"

	var captured runtime.RunConfig
	mockRT := &runtime.MockRuntime{
		ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{}, nil
		},
		RunFunc: func(_ context.Context, cfg runtime.RunConfig) (string, error) {
			captured = cfg
			return "mock-id", nil
		},
	}
	mgr := NewManager(mockRT)
	start := func(env map[string]string) {
		t.Helper()
		if _, err := mgr.Start(context.Background(), api.StartOptions{
			Name:        name,
			Template:    "default",
			ProjectPath: projectScionDir,
			NoAuth:      true,
			BrokerMode:  true,
			Env:         env,
		}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
	}

	// First start provisions the agent directory.
	start(nil)
	matches, err := filepath.Glob(filepath.Join(projectScionDir, "agents", name, "scion-agent.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("agent config not found after the first start: %v %v", matches, err)
	}
	cfgPath := matches[0]
	agentDir := filepath.Dir(cfgPath)
	cfgBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(agentDir, "fake-existing-state.txt")
	if err := os.WriteFile(marker, []byte("fake persisted state"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second start on the existing directory, carrying a request env value.
	start(map[string]string{"FAKE_AS_NEEDED_KEY": "fake-as-needed-value"})

	cfgAfter, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("agent config missing after the second start: %v", err)
	}
	if !bytes.Equal(cfgBefore, cfgAfter) {
		t.Errorf("scion-agent.json changed on a start of an existing agent dir:\nbefore: %s\nafter:  %s", cfgBefore, cfgAfter)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "fake persisted state" {
		t.Errorf("the agent's existing files were not kept: %q, %v", got, err)
	}
	if strings.Contains(string(cfgAfter), "FAKE_AS_NEEDED_KEY") {
		t.Errorf("the request env was persisted into scion-agent.json; it must only reach the container")
	}

	env := map[string]string{}
	for _, e := range captured.Env {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	if got := env["FAKE_AS_NEEDED_KEY"]; got != "fake-as-needed-value" {
		t.Errorf("container env FAKE_AS_NEEDED_KEY = %q, want the request's value", got)
	}
}
