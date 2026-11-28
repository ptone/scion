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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

func TestMCPDefaultDisabled(t *testing.T) {
	gc := config.DefaultGlobalConfig()
	if gc.MCP.Enabled {
		t.Fatal("expected DefaultGlobalConfig().MCP.Enabled to be false")
	}
}

func TestValidateMCPStartup(t *testing.T) {
	if err := validateMCPStartup(&config.GlobalConfig{}, false); err != nil {
		t.Fatalf("disabled MCP without hub should succeed, got: %v", err)
	}
	if err := validateMCPStartup(&config.GlobalConfig{MCP: config.MCPServerConfig{Enabled: true}}, true); err != nil {
		t.Fatalf("enabled MCP with hub should succeed, got: %v", err)
	}
	if err := validateMCPStartup(&config.GlobalConfig{MCP: config.MCPServerConfig{Enabled: true}}, false); err == nil {
		t.Fatal("expected error when MCP is enabled without hub in P1")
	}
}

func TestV1ServerMCPRoundTrip(t *testing.T) {
	v1 := &config.V1ServerConfig{
		MCP: &config.V1ServerMCPConfig{
			Enabled:                true,
			Host:                   "127.0.0.1",
			Port:                   9821,
			Path:                   "/custom-mcp",
			PublicURL:              "https://mcp.example.com",
			HubEndpoint:            "https://hub.example.com",
			AllowedOrigins:         []string{"https://app.example.com"},
			MaxStreamsPerPrincipal: 12,
			StreamWait:             "45s",
		},
	}

	gc := config.ConvertV1ServerToGlobalConfig(v1)
	if !gc.MCP.Enabled || gc.MCP.Host != "127.0.0.1" || gc.MCP.Port != 9821 || gc.MCP.Path != "/custom-mcp" {
		t.Fatalf("unexpected GlobalConfig.MCP: %+v", gc.MCP)
	}
	if gc.MCP.PublicURL != "https://mcp.example.com" || gc.MCP.HubEndpoint != "https://hub.example.com" {
		t.Fatalf("unexpected URLs: %+v", gc.MCP)
	}
	if gc.MCP.MaxStreamsPerPrincipal != 12 || gc.MCP.StreamWait != 45*time.Second {
		t.Fatalf("unexpected stream settings: %+v", gc.MCP)
	}
	if len(gc.MCP.AllowedOrigins) != 1 || gc.MCP.AllowedOrigins[0] != "https://app.example.com" {
		t.Fatalf("unexpected AllowedOrigins: %+v", gc.MCP.AllowedOrigins)
	}

	back := config.ConvertGlobalToV1ServerConfig(gc)
	if back.MCP == nil {
		t.Fatal("expected back.MCP to be non-nil")
	}
	if !back.MCP.Enabled || back.MCP.Port != 9821 || back.MCP.StreamWait != "45s" {
		t.Fatalf("unexpected round-tripped V1ServerMCPConfig: %+v", back.MCP)
	}
}

func TestLoadGlobalConfig_MCPStreamWaitValidation(t *testing.T) {
	t.Run("rejects_unparseable_stream_wait", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		scionDir := filepath.Join(dir, ".scion")
		if err := os.MkdirAll(scionDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		yaml := "schema_version: \"1\"\nserver:\n  mcp:\n    enabled: true\n    stream_wait: \"not-a-duration\"\n"
		if err := os.WriteFile(filepath.Join(scionDir, "settings.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatalf("write settings.yaml: %v", err)
		}
		_, err := config.LoadGlobalConfig(scionDir)
		if err == nil {
			t.Fatal("expected error for unparseable server.mcp.stream_wait")
		}
		if !strings.Contains(err.Error(), "stream_wait") {
			t.Fatalf("expected error mentioning stream_wait, got: %v", err)
		}
	})

	t.Run("rejects_non_positive_above_55s_stream_wait_and_negative_max_streams", func(t *testing.T) {
		if err := config.ValidateV1MCPConfig(&config.V1ServerMCPConfig{StreamWait: "0s"}); err == nil {
			t.Fatal("expected error for stream_wait=0s")
		}
		if err := config.ValidateV1MCPConfig(&config.V1ServerMCPConfig{StreamWait: "-5s"}); err == nil {
			t.Fatal("expected error for stream_wait=-5s")
		}
		if err := config.ValidateV1MCPConfig(&config.V1ServerMCPConfig{StreamWait: "56s"}); err == nil {
			t.Fatal("expected error for stream_wait=56s (> 55s Hub maximum)")
		}
		if err := config.ValidateV1MCPConfig(&config.V1ServerMCPConfig{MaxStreamsPerPrincipal: -1}); err == nil {
			t.Fatal("expected error for negative max_streams_per_principal")
		}
		if err := config.ValidateV1MCPConfig(&config.V1ServerMCPConfig{StreamWait: "55s", MaxStreamsPerPrincipal: 8}); err != nil {
			t.Fatalf("valid MCP config (55s) rejected: %v", err)
		}
	})

	t.Run("legacy_server_yaml_runs_mcp_validation", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		scionDir := filepath.Join(dir, ".scion")
		if err := os.MkdirAll(scionDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Legacy server.yaml (no settings.yaml) with stream_wait > 55s.
		legacyYAML := "mcp:\n  enabled: true\n  streamWait: 60s\n"
		if err := os.WriteFile(filepath.Join(scionDir, "server.yaml"), []byte(legacyYAML), 0o600); err != nil {
			t.Fatalf("write server.yaml: %v", err)
		}
		_, err := config.LoadGlobalConfig(scionDir)
		if err == nil || !strings.Contains(err.Error(), "stream_wait") {
			t.Fatalf("legacy server.yaml with streamWait=60s: err = %v, want stream_wait validation error", err)
		}
	})
}

// TestEmbeddedMCPStop_BoundedWithoutDeadline covers R3:
// when neither teardown ctx nor httpDrainContext has a deadline (default config
// with no shutdown_grace), waitBeforeHubTeardown must still return within its
// fallback bound rather than blocking Hub teardown forever.
func TestEmbeddedMCPStop_BoundedWithoutDeadline(t *testing.T) {
	mcpStop := &embeddedMCPStop{
		stopped:  make(chan struct{}),
		fallback: 120 * time.Millisecond,
	}
	mcpStop.markStarted()

	unboundedDrain := func(parent context.Context) (context.Context, context.CancelFunc) {
		return parent, func() {}
	}
	start := time.Now()
	mcpStop.waitBeforeHubTeardown(context.Background(), unboundedDrain)
	elapsed := time.Since(start)
	if elapsed < 80*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("waitBeforeHubTeardown elapsed = %s, want ~120ms fallback bound", elapsed)
	}
}

// TestComposeBeforeHTTPDrain_OrdersBrokerThenMCPBeforeHubTeardown tests the
// real BeforeHTTPDrain composition used by runServerStart: broker drain runs
// first, then MCP drain is awaited before BeforeHTTPDrain returns to let Hub
// teardown proceed.
func TestComposeBeforeHTTPDrain_OrdersBrokerThenMCPBeforeHubTeardown(t *testing.T) {
	var events []string
	mcpStop := newEmbeddedMCPStop()
	mcpStop.markStarted()

	brokerDrain := func(_ context.Context) {
		events = append(events, "broker_drained")
		go func() {
			time.Sleep(40 * time.Millisecond)
			events = append(events, "mcp_drained")
			mcpStop.markStopped()
		}()
	}

	hook := composeBeforeHTTPDrain(brokerDrain, mcpStop, func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(parent, 2*time.Second)
	})
	hook(context.Background())
	events = append(events, "hub_teardown")

	want := "broker_drained,mcp_drained,hub_teardown"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("drain order = %q, want %q", got, want)
	}
}
