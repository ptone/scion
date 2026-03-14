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
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	scionplugin "github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

// PluginManager is an optional interface for looking up harness plugins.
// When set, the harness factory checks plugins after built-in harnesses.
var pluginManager pluginHarnessProvider

type pluginHarnessProvider interface {
	HasPlugin(pluginType, name string) bool
	GetHarness(name string) (api.Harness, error)
}

// SetPluginManager sets the plugin manager used for harness plugin lookup.
// This should be called during server initialization before any harness creation.
func SetPluginManager(mgr *scionplugin.Manager) {
	pluginManager = mgr
}

func New(harnessName string) api.Harness {
	switch harnessName {
	case "claude":
		return &ClaudeCode{}
	case "gemini":
		return &GeminiCLI{}
	case "opencode":
		return &OpenCode{}
	case "codex":
		return &Codex{}
	default:
		// Check plugin registry before falling back to Generic
		if pluginManager != nil && pluginManager.HasPlugin(scionplugin.PluginTypeHarness, harnessName) {
			h, err := pluginManager.GetHarness(harnessName)
			if err == nil {
				return h
			}
		}
		return &Generic{}
	}
}

func All() []api.Harness {
	return []api.Harness{
		&GeminiCLI{},
		&ClaudeCode{},
		&OpenCode{},
		&Codex{},
	}
}
