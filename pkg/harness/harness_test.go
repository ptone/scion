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

package harness

import (
	"context"
	"embed"
	"fmt"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	scionplugin "github.com/GoogleCloudPlatform/scion/pkg/plugin"
	"github.com/stretchr/testify/assert"
)

func TestNew_BuiltinHarnesses(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"claude", "claude"},
		{"gemini", "gemini"},
		{"opencode", "opencode"},
		{"codex", "codex"},
	}

	for _, tt := range tests {
		h := New(tt.name)
		assert.Equal(t, tt.expected, h.Name())
	}
}

func TestNew_UnknownFallsToGeneric(t *testing.T) {
	pluginManager = nil
	h := New("unknown-harness")
	assert.Equal(t, "generic", h.Name())
}

// mockPluginProvider implements pluginHarnessProvider for testing.
type mockPluginProvider struct {
	plugins map[string]api.Harness
}

func (m *mockPluginProvider) HasPlugin(pluginType, name string) bool {
	if pluginType != scionplugin.PluginTypeHarness {
		return false
	}
	_, ok := m.plugins[name]
	return ok
}

func (m *mockPluginProvider) GetHarness(name string) (api.Harness, error) {
	h, ok := m.plugins[name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return h, nil
}

// stubHarness is a minimal api.Harness for testing plugin lookup.
type stubHarness struct{ name string }

func (s *stubHarness) Name() string                                              { return s.name }
func (s *stubHarness) AdvancedCapabilities() api.HarnessAdvancedCapabilities     { return api.HarnessAdvancedCapabilities{} }
func (s *stubHarness) GetEnv(string, string, string) map[string]string           { return nil }
func (s *stubHarness) GetCommand(string, bool, []string) []string                { return nil }
func (s *stubHarness) DefaultConfigDir() string                                  { return "" }
func (s *stubHarness) SkillsDir() string                                         { return "" }
func (s *stubHarness) HasSystemPrompt(string) bool                               { return false }
func (s *stubHarness) Provision(_ context.Context, _, _, _, _ string) error       { return nil }
func (s *stubHarness) GetEmbedDir() string                                       { return "" }
func (s *stubHarness) GetInterruptKey() string                                   { return "" }
func (s *stubHarness) GetHarnessEmbedsFS() (embed.FS, string)                    { return embed.FS{}, "" }
func (s *stubHarness) InjectAgentInstructions(string, []byte) error              { return nil }
func (s *stubHarness) InjectSystemPrompt(string, []byte) error                   { return nil }
func (s *stubHarness) GetTelemetryEnv() map[string]string                        { return nil }
func (s *stubHarness) ResolveAuth(api.AuthConfig) (*api.ResolvedAuth, error)     { return nil, nil }

func TestNew_PluginHarness(t *testing.T) {
	provider := &mockPluginProvider{
		plugins: map[string]api.Harness{
			"cursor": &stubHarness{name: "cursor"},
		},
	}
	pluginManager = provider
	defer func() { pluginManager = nil }()

	// Built-in harnesses should still work
	h := New("claude")
	assert.Equal(t, "claude", h.Name())

	// Plugin harness should be found
	h = New("cursor")
	assert.Equal(t, "cursor", h.Name())

	// Unknown plugin falls back to Generic
	h = New("nonexistent")
	assert.Equal(t, "generic", h.Name())
}

func TestAll_ReturnsBuiltins(t *testing.T) {
	all := All()
	assert.Len(t, all, 4)
	names := make([]string, len(all))
	for i, h := range all {
		names[i] = h.Name()
	}
	assert.Contains(t, names, "gemini")
	assert.Contains(t, names, "claude")
	assert.Contains(t, names, "opencode")
	assert.Contains(t, names, "codex")
}
