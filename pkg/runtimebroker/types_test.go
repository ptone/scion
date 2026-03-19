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
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

func TestAgentInfoToResponse(t *testing.T) {
	tests := []struct {
		name             string
		info             api.AgentInfo
		expectedStatus   string
		expectedPhase    string
		expectedActivity string
		expectedReady    bool
	}{
		{
			name: "phase and activity set uses structured path",
			info: api.AgentInfo{
				Name:     "agent-structured",
				Phase:    "running",
				Activity: "thinking",
			},
			expectedStatus:   "thinking",
			expectedPhase:    "running",
			expectedActivity: "thinking",
			expectedReady:    true,
		},
		{
			name: "phase running with no activity uses phase as status",
			info: api.AgentInfo{
				Name:  "agent-phase-only",
				Phase: "running",
			},
			expectedStatus:   "running",
			expectedPhase:    "running",
			expectedActivity: "",
			expectedReady:    true,
		},
		{
			name: "phase stopped clears activity",
			info: api.AgentInfo{
				Name:  "agent-stopped-phase",
				Phase: "stopped",
			},
			expectedStatus:   "stopped",
			expectedPhase:    "stopped",
			expectedActivity: "",
			expectedReady:    false,
		},
		{
			name: "phase with waiting_for_input activity",
			info: api.AgentInfo{
				Name:     "agent-waiting",
				Phase:    "running",
				Activity: "waiting_for_input",
			},
			expectedStatus:   "waiting_for_input",
			expectedPhase:    "running",
			expectedActivity: "waiting_for_input",
			expectedReady:    true,
		},
		{
			name: "phase already set passes through unchanged",
			info: api.AgentInfo{
				Name:            "agent-1",
				Phase:           "running",
				ContainerStatus: "created", // should be ignored
			},
			expectedStatus:   "running",
			expectedPhase:    "running",
			expectedActivity: "",
			expectedReady:    true,
		},
		{
			name: "phase set to non-running value",
			info: api.AgentInfo{
				Name:            "agent-2",
				Phase:           "stopped",
				ContainerStatus: "Up 5 minutes",
			},
			expectedStatus:   "stopped",
			expectedPhase:    "stopped",
			expectedActivity: "",
			expectedReady:    false,
		},
		{
			name: "empty status with container up maps to running",
			info: api.AgentInfo{
				Name:            "agent-3",
				ContainerStatus: "Up 2 hours",
			},
			expectedStatus:   string(state.PhaseRunning),
			expectedPhase:    string(state.PhaseRunning),
			expectedActivity: "",
			expectedReady:    true,
		},
		{
			name: "empty status with container running maps to running",
			info: api.AgentInfo{
				Name:            "agent-4",
				ContainerStatus: "running",
			},
			expectedStatus:   string(state.PhaseRunning),
			expectedPhase:    string(state.PhaseRunning),
			expectedActivity: "",
			expectedReady:    true,
		},
		{
			name: "empty status with container created maps to provisioning",
			info: api.AgentInfo{
				Name:            "agent-5",
				ContainerStatus: "created",
			},
			expectedStatus:   string(state.PhaseProvisioning),
			expectedPhase:    string(state.PhaseProvisioning),
			expectedActivity: "",
			expectedReady:    false,
		},
		{
			name: "empty status with container exited maps to stopped",
			info: api.AgentInfo{
				Name:            "agent-6",
				ContainerStatus: "Exited (0) 5 minutes ago",
			},
			expectedStatus:   string(state.PhaseStopped),
			expectedPhase:    string(state.PhaseStopped),
			expectedActivity: "",
			expectedReady:    false,
		},
		{
			name: "empty status with container stopped maps to stopped",
			info: api.AgentInfo{
				Name:            "agent-7",
				ContainerStatus: "stopped",
			},
			expectedStatus:   string(state.PhaseStopped),
			expectedPhase:    string(state.PhaseStopped),
			expectedActivity: "",
			expectedReady:    false,
		},
		{
			name: "empty status with empty container status maps to created",
			info: api.AgentInfo{
				Name: "agent-8",
			},
			expectedStatus:   string(state.PhaseCreated),
			expectedPhase:    string(state.PhaseCreated),
			expectedActivity: "",
			expectedReady:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := AgentInfoToResponse(tt.info)
			if resp.Status != tt.expectedStatus {
				t.Errorf("Status = %q, want %q", resp.Status, tt.expectedStatus)
			}
			if resp.Phase != tt.expectedPhase {
				t.Errorf("Phase = %q, want %q", resp.Phase, tt.expectedPhase)
			}
			if resp.Activity != tt.expectedActivity {
				t.Errorf("Activity = %q, want %q", resp.Activity, tt.expectedActivity)
			}
			if resp.Ready != tt.expectedReady {
				t.Errorf("Ready = %v, want %v", resp.Ready, tt.expectedReady)
			}
		})
	}
}

func TestAgentInfoToResponseHarnessConfig(t *testing.T) {
	info := api.AgentInfo{
		Name:          "agent-harness",
		Phase:         "running",
		Template:      "default",
		HarnessConfig: "gemini",
	}

	resp := AgentInfoToResponse(info)
	if resp.HarnessConfig != "gemini" {
		t.Errorf("HarnessConfig = %q, want %q", resp.HarnessConfig, "gemini")
	}
	if resp.Template != "default" {
		t.Errorf("Template = %q, want %q", resp.Template, "default")
	}
}

func TestAgentInfoToResponseProfile(t *testing.T) {
	info := api.AgentInfo{
		Name:    "agent-profile",
		Phase:   "running",
		Profile: "docker-dev",
	}

	resp := AgentInfoToResponse(info)
	if resp.Profile != "docker-dev" {
		t.Errorf("Profile = %q, want %q", resp.Profile, "docker-dev")
	}
}
