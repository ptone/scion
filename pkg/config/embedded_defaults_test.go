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

import "testing"

func TestEmbeddedAgentDefaults(t *testing.T) {
	tmpl, hc := EmbeddedAgentDefaults()

	if tmpl != "default" {
		t.Errorf("expected default_template='default', got %q", tmpl)
	}
	if hc != "antigravity" {
		t.Errorf("expected default_harness_config='antigravity', got %q", hc)
	}
}
