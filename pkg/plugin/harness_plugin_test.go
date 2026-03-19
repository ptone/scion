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

package plugin

import (
	"context"
	"embed"
	"net"
	"net/rpc"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHarness implements api.Harness for testing.
type mockHarness struct {
	name        string
	provisioned bool
	injected    []byte
}

func (m *mockHarness) Name() string { return m.name }
func (m *mockHarness) AdvancedCapabilities() api.HarnessAdvancedCapabilities {
	return api.HarnessAdvancedCapabilities{Harness: m.name}
}
func (m *mockHarness) GetEnv(agentName, agentHome, unixUsername string) map[string]string {
	return map[string]string{
		"AGENT_NAME": agentName,
		"AGENT_HOME": agentHome,
	}
}
func (m *mockHarness) GetCommand(task string, resume bool, baseArgs []string) []string {
	cmd := []string{"my-harness"}
	if task != "" {
		cmd = append(cmd, "--task", task)
	}
	if resume {
		cmd = append(cmd, "--resume")
	}
	return append(cmd, baseArgs...)
}
func (m *mockHarness) DefaultConfigDir() string { return ".my-harness" }
func (m *mockHarness) SkillsDir() string        { return "skills" }
func (m *mockHarness) HasSystemPrompt(agentHome string) bool {
	return true
}
func (m *mockHarness) Provision(ctx context.Context, agentName, agentDir, agentHome, agentWorkspace string) error {
	m.provisioned = true
	return nil
}
func (m *mockHarness) GetEmbedDir() string                    { return "my-harness" }
func (m *mockHarness) GetInterruptKey() string                { return "C-c" }
func (m *mockHarness) GetHarnessEmbedsFS() (embed.FS, string) { return embed.FS{}, "" }
func (m *mockHarness) InjectAgentInstructions(agentHome string, content []byte) error {
	m.injected = content
	return nil
}
func (m *mockHarness) InjectSystemPrompt(agentHome string, content []byte) error {
	return nil
}
func (m *mockHarness) GetTelemetryEnv() map[string]string {
	return map[string]string{"OTEL_ENABLED": "true"}
}
func (m *mockHarness) ResolveAuth(auth api.AuthConfig) (*api.ResolvedAuth, error) {
	return &api.ResolvedAuth{
		Method:  "passthrough",
		EnvVars: map[string]string{"API_KEY": "test"},
	}, nil
}

func startTestHarnessRPCServer(t *testing.T, impl api.Harness) *HarnessRPCClient {
	t.Helper()

	server := rpc.NewServer()
	rpcServer := &HarnessRPCServer{Impl: impl}
	require.NoError(t, server.RegisterName("Plugin", rpcServer))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })

	go server.Accept(listener)

	client, err := rpc.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	return &HarnessRPCClient{client: client}
}

func TestHarnessRPC_Name(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	assert.Equal(t, "test-harness", client.Name())
}

func TestHarnessRPC_AdvancedCapabilities(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	caps := client.AdvancedCapabilities()
	assert.Equal(t, "test-harness", caps.Harness)
}

func TestHarnessRPC_GetEnv(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	env := client.GetEnv("agent1", "/home/agent1", "agent")
	assert.Equal(t, "agent1", env["AGENT_NAME"])
	assert.Equal(t, "/home/agent1", env["AGENT_HOME"])
}

func TestHarnessRPC_GetCommand(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	cmd := client.GetCommand("implement feature", false, []string{"--verbose"})
	assert.Equal(t, []string{"my-harness", "--task", "implement feature", "--verbose"}, cmd)
}

func TestHarnessRPC_DefaultConfigDir(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	assert.Equal(t, ".my-harness", client.DefaultConfigDir())
}

func TestHarnessRPC_SkillsDir(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	assert.Equal(t, "skills", client.SkillsDir())
}

func TestHarnessRPC_HasSystemPrompt(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	assert.True(t, client.HasSystemPrompt("/home/agent1"))
}

func TestHarnessRPC_Provision(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	err := client.Provision(context.Background(), "agent1", "/dir", "/home/agent1", "/workspace")
	require.NoError(t, err)
	assert.True(t, mock.provisioned)
}

func TestHarnessRPC_GetEmbedDir(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	assert.Equal(t, "my-harness", client.GetEmbedDir())
}

func TestHarnessRPC_GetInterruptKey(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	assert.Equal(t, "C-c", client.GetInterruptKey())
}

func TestHarnessRPC_GetHarnessEmbedsFS_ReturnsNil(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	fs, base := client.GetHarnessEmbedsFS()
	assert.Equal(t, embed.FS{}, fs)
	assert.Empty(t, base)
}

func TestHarnessRPC_InjectAgentInstructions(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	err := client.InjectAgentInstructions("/home/agent1", []byte("# Instructions\nDo the thing."))
	require.NoError(t, err)
	assert.Equal(t, []byte("# Instructions\nDo the thing."), mock.injected)
}

func TestHarnessRPC_GetTelemetryEnv(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	env := client.GetTelemetryEnv()
	assert.Equal(t, "true", env["OTEL_ENABLED"])
}

func TestHarnessRPC_ResolveAuth(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	resolved, err := client.ResolveAuth(api.AuthConfig{
		AnthropicAPIKey: "test-key",
	})
	require.NoError(t, err)
	assert.Equal(t, "passthrough", resolved.Method)
	assert.Equal(t, "test", resolved.EnvVars["API_KEY"])
}

func TestHarnessRPC_GetInfo(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	info, err := client.GetInfo()
	require.NoError(t, err)
	assert.Equal(t, "test-harness", info.Name)
}

func TestHarnessRPC_MetadataCaching(t *testing.T) {
	mock := &mockHarness{name: "test-harness"}
	client := startTestHarnessRPCServer(t, mock)

	// First call fetches metadata
	name1 := client.Name()
	// Second call should use cached value
	name2 := client.Name()

	assert.Equal(t, name1, name2)
	assert.Equal(t, "test-harness", name1)
	assert.NotNil(t, client.metadata, "metadata should be cached after first call")
}

// mockHarnessWithCapabilities extends mockHarness with optional interfaces.
type mockHarnessWithCapabilities struct {
	mockHarness
	appliedAuthHome      string
	appliedTelemetryHome string
}

func (m *mockHarnessWithCapabilities) ApplyAuthSettings(agentHome string, resolved *api.ResolvedAuth) error {
	m.appliedAuthHome = agentHome
	return nil
}

func (m *mockHarnessWithCapabilities) ApplyTelemetrySettings(agentHome string, telemetry *api.TelemetryConfig, env map[string]string) error {
	m.appliedTelemetryHome = agentHome
	return nil
}

func TestHarnessRPC_ApplyAuthSettings_WithCapability(t *testing.T) {
	mock := &mockHarnessWithCapabilities{mockHarness: mockHarness{name: "capable-harness"}}
	client := startTestHarnessRPCServer(t, mock)

	err := client.ApplyAuthSettings("/home/agent1", &api.ResolvedAuth{
		Method:  "passthrough",
		EnvVars: map[string]string{"KEY": "val"},
	})
	require.NoError(t, err)
	assert.Equal(t, "/home/agent1", mock.appliedAuthHome)
}

func TestHarnessRPC_ApplyTelemetrySettings_WithCapability(t *testing.T) {
	mock := &mockHarnessWithCapabilities{mockHarness: mockHarness{name: "capable-harness"}}
	client := startTestHarnessRPCServer(t, mock)

	enabled := true
	err := client.ApplyTelemetrySettings("/home/agent1", &api.TelemetryConfig{Enabled: &enabled}, map[string]string{"FOO": "bar"})
	require.NoError(t, err)
	assert.Equal(t, "/home/agent1", mock.appliedTelemetryHome)
}

func TestHarnessRPC_ApplyAuthSettings_WithoutCapability(t *testing.T) {
	// mockHarness does NOT implement AuthSettingsApplier
	mock := &mockHarness{name: "basic-harness"}
	client := startTestHarnessRPCServer(t, mock)

	// Should silently succeed (no-op) when capability is not supported
	err := client.ApplyAuthSettings("/home/agent1", &api.ResolvedAuth{Method: "passthrough"})
	require.NoError(t, err)
}

func TestHarnessRPC_ApplyTelemetrySettings_WithoutCapability(t *testing.T) {
	// mockHarness does NOT implement TelemetrySettingsApplier
	mock := &mockHarness{name: "basic-harness"}
	client := startTestHarnessRPCServer(t, mock)

	enabled := true
	err := client.ApplyTelemetrySettings("/home/agent1", &api.TelemetryConfig{Enabled: &enabled}, nil)
	require.NoError(t, err)
}

func TestHarnessRPC_Capabilities_Advertised(t *testing.T) {
	mock := &mockHarnessWithCapabilities{mockHarness: mockHarness{name: "capable-harness"}}
	client := startTestHarnessRPCServer(t, mock)

	meta, err := client.getMetadata()
	require.NoError(t, err)
	assert.Contains(t, meta.Capabilities, "auth_settings")
	assert.Contains(t, meta.Capabilities, "telemetry_settings")
}

func TestHarnessRPC_Capabilities_NotAdvertised(t *testing.T) {
	mock := &mockHarness{name: "basic-harness"}
	client := startTestHarnessRPCServer(t, mock)

	meta, err := client.getMetadata()
	require.NoError(t, err)
	assert.Empty(t, meta.Capabilities)
}
