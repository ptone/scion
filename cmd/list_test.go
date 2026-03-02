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

package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ptone/scion-agent/pkg/api"
	"github.com/ptone/scion-agent/pkg/hubclient"
)

func TestFormatLastSeen(t *testing.T) {
	tests := []struct {
		name     string
		offset   time.Duration
		expected string
	}{
		{"zero time", 0, "-"},
		{"just now", 0 * time.Second, "just now"},
		{"1 second ago", 1 * time.Second, "just now"},
		{"30 seconds ago", 30 * time.Second, "30 seconds ago"},
		{"59 seconds ago", 59 * time.Second, "59 seconds ago"},
		{"1 minute ago", 1 * time.Minute, "1 minute ago"},
		{"5 minutes ago", 5 * time.Minute, "5 minutes ago"},
		{"59 minutes ago", 59 * time.Minute, "59 minutes ago"},
		{"1 hour ago", 1 * time.Hour, "1 hour ago"},
		{"3 hours ago", 3 * time.Hour, "3 hours ago"},
		{"23 hours ago", 23 * time.Hour, "23 hours ago"},
		{"1 day ago", 24 * time.Hour, "1 day ago"},
		{"7 days ago", 7 * 24 * time.Hour, "7 days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input time.Time
			if tt.name == "zero time" {
				input = time.Time{}
			} else {
				input = time.Now().Add(-tt.offset)
			}

			result := formatLastSeen(input)
			if result != tt.expected {
				t.Errorf("formatLastSeen() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestFormatLastSeenFutureTime(t *testing.T) {
	future := time.Now().Add(10 * time.Second)
	result := formatLastSeen(future)
	if result != "just now" {
		t.Errorf("formatLastSeen(future) = %q, want %q", result, "just now")
	}
}

func TestFormatLastActivity(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		status   string
		t        time.Time
		expected string
	}{
		{"activity with time", "thinking", now.Add(-30 * time.Second), "thinking, 30 seconds ago"},
		{"phase with time", "stopped", now.Add(-2 * time.Hour), "stopped, 2 hours ago"},
		{"empty status with time", "", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"IDLE status with time", "IDLE", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"activity with zero time", "running", time.Time{}, "running"},
		{"empty status with zero time", "", time.Time{}, "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatLastActivity(tt.status, tt.t)
			if result != tt.expected {
				t.Errorf("formatLastActivity(%q, ...) = %q, want %q", tt.status, result, tt.expected)
			}
		})
	}
}

func TestDisplayAgentsLocalMode(t *testing.T) {
	agents := []api.AgentInfo{
		{
			Name:            "agent-1",
			Template:        "default",
			HarnessConfig:   "claude",
			Runtime:         "docker",
			Grove:           "my-project",
			Phase:           "running",
			Activity:        "thinking",
			ContainerStatus: "Up 2 hours",
			LastSeen:        time.Now().Add(-30 * time.Second),
		},
		{
			Name:            "agent-2",
			Template:        "research",
			Runtime:         "docker",
			Grove:           "my-project",
			Phase:           "stopped",
			ContainerStatus: "created",
			// No HarnessConfig, no LastSeen
		},
	}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := displayAgents(agents, false, false)
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("displayAgents returned error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Verify header contains all expected columns
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (header + 2 agents), got %d: %s", len(lines), output)
	}

	header := lines[0]
	for _, col := range []string{"NAME", "TEMPLATE", "HARNESS-CFG", "RUNTIME", "GROVE", "STATUS", "CONTAINER", "LAST ACTIVITY"} {
		if !strings.Contains(header, col) {
			t.Errorf("header missing column %q: %s", col, header)
		}
	}

	// Verify first agent row has harness config value
	if !strings.Contains(lines[1], "claude") {
		t.Errorf("agent-1 row should contain harness config 'claude': %s", lines[1])
	}
	if !strings.Contains(lines[1], "thinking, 30 seconds ago") {
		t.Errorf("agent-1 row should contain 'thinking, 30 seconds ago': %s", lines[1])
	}

	// Verify second agent row shows "-" for missing harness config
	if !strings.Contains(lines[2], "-") {
		t.Errorf("agent-2 row should contain '-' for missing values: %s", lines[2])
	}
}

func TestDisplayAgentsHubMode(t *testing.T) {
	agents := []api.AgentInfo{
		{
			Name:              "hub-agent",
			Template:          "default",
			HarnessConfig:     "gemini",
			Runtime:           "docker",
			Grove:             "hub-project",
			RuntimeBrokerName: "local-broker",
			Phase:             "running",
			ContainerStatus:   "Up 5 minutes",
			LastSeen:          time.Now().Add(-2 * time.Minute),
		},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := displayAgents(agents, false, true)
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("displayAgents returned error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}

	header := lines[0]
	// Hub mode should have BROKER column
	for _, col := range []string{"NAME", "TEMPLATE", "HARNESS-CFG", "RUNTIME", "GROVE", "BROKER", "STATUS", "CONTAINER", "LAST ACTIVITY"} {
		if !strings.Contains(header, col) {
			t.Errorf("hub mode header missing column %q: %s", col, header)
		}
	}

	// Verify agent row
	if !strings.Contains(lines[1], "gemini") {
		t.Errorf("hub agent row should contain harness config 'gemini': %s", lines[1])
	}
	if !strings.Contains(lines[1], "local-broker") {
		t.Errorf("hub agent row should contain broker name: %s", lines[1])
	}
	if !strings.Contains(lines[1], "running, 2 minutes ago") {
		t.Errorf("hub agent row should contain 'running, 2 minutes ago': %s", lines[1])
	}
}

func TestDisplayAgentsSortByTime(t *testing.T) {
	now := time.Now()
	agents := []api.AgentInfo{
		{
			Name:     "old-agent",
			Template: "default",
			Runtime:  "docker",
			Grove:    "my-project",
			LastSeen: now.Add(-10 * time.Minute),
		},
		{
			Name:     "new-agent",
			Template: "default",
			Runtime:  "docker",
			Grove:    "my-project",
			LastSeen: now.Add(-1 * time.Minute),
		},
		{
			Name:     "mid-agent",
			Template: "default",
			Runtime:  "docker",
			Grove:    "my-project",
			LastSeen: now.Add(-5 * time.Minute),
		},
	}

	// Enable sort-by-time flag
	sortByTime = true
	defer func() { sortByTime = false }()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := displayAgents(agents, false, false)
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("displayAgents returned error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected 4 lines (header + 3 agents), got %d: %s", len(lines), output)
	}

	// Most recent first: new-agent, mid-agent, old-agent
	if !strings.Contains(lines[1], "new-agent") {
		t.Errorf("first agent should be 'new-agent' (most recent), got: %s", lines[1])
	}
	if !strings.Contains(lines[2], "mid-agent") {
		t.Errorf("second agent should be 'mid-agent', got: %s", lines[2])
	}
	if !strings.Contains(lines[3], "old-agent") {
		t.Errorf("third agent should be 'old-agent' (oldest), got: %s", lines[3])
	}
}

func TestDisplayAgentsEmpty(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := displayAgents(nil, false, false)
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("displayAgents returned error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "No active agents found in the current grove.") {
		t.Errorf("expected empty grove message, got: %s", output)
	}
}

func TestDisplayAgentsEmptyAll(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := displayAgents(nil, true, false)
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("displayAgents returned error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "No active agents found across any groves.") {
		t.Errorf("expected all-groves empty message, got: %s", output)
	}
}

func TestHubAgentToAgentInfo_HarnessConfigFromTopLevel(t *testing.T) {
	// When the Hub returns harnessConfig at the top level, use it directly
	a := hubclient.Agent{
		ID:            "agent-1",
		Name:          "test-agent",
		HarnessConfig: "gemini",
	}
	info := hubAgentToAgentInfo(a)
	if info.HarnessConfig != "gemini" {
		t.Errorf("HarnessConfig = %q, want %q", info.HarnessConfig, "gemini")
	}
}

func TestHubAgentToAgentInfo_HarnessConfigFallbackToAppliedConfig(t *testing.T) {
	// When the Hub does NOT return harnessConfig at the top level (older Hub),
	// fall back to AppliedConfig.HarnessConfig
	a := hubclient.Agent{
		ID:   "agent-2",
		Name: "test-agent-2",
		AppliedConfig: &hubclient.AgentConfig{
			HarnessConfig: "claude",
		},
	}
	info := hubAgentToAgentInfo(a)
	if info.HarnessConfig != "claude" {
		t.Errorf("HarnessConfig = %q, want %q (should fall back to AppliedConfig.HarnessConfig)", info.HarnessConfig, "claude")
	}
}

func TestHubAgentToAgentInfo_HarnessConfigTopLevelTakesPrecedence(t *testing.T) {
	// When both are set, top-level harnessConfig takes precedence
	a := hubclient.Agent{
		ID:            "agent-3",
		Name:          "test-agent-3",
		HarnessConfig: "gemini",
		AppliedConfig: &hubclient.AgentConfig{
			HarnessConfig: "claude",
		},
	}
	info := hubAgentToAgentInfo(a)
	if info.HarnessConfig != "gemini" {
		t.Errorf("HarnessConfig = %q, want %q (top-level should take precedence)", info.HarnessConfig, "gemini")
	}
}
