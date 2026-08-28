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

package config

import "gopkg.in/yaml.v3"

// EmbeddedAgentDefaults returns the default_template and default_harness_config
// values from the embedded default_settings.yaml. These are the canonical
// defaults that the broker reads from its materialized settings.yaml; this
// function makes them available to the hub without re-parsing the full koanf
// merge chain.
//
// Returns zero strings if the embedded file is missing or unparseable (should
// not happen — the file is compiled into the binary).
func EmbeddedAgentDefaults() (defaultTemplate, defaultHarnessConfig string) {
	data, err := EmbedsFS.ReadFile("embeds/default_settings.yaml")
	if err != nil {
		return "", ""
	}
	var raw struct {
		DefaultTemplate      string `yaml:"default_template"`
		DefaultHarnessConfig string `yaml:"default_harness_config"`
	}
	if yaml.Unmarshal(data, &raw) != nil {
		return "", ""
	}
	return raw.DefaultTemplate, raw.DefaultHarnessConfig
}
