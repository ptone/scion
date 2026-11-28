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

package commands

import (
	"testing"
)

func TestInternalNormalizeExec_IsHookInvocation(t *testing.T) {
	if !isHookInvocation([]string{"internal", "normalize-exec", "--", "/bin/true"}) {
		t.Error("expected internal normalize-exec to be treated as quiet hook invocation")
	}
}

func TestInternalNormalizeExec_Validation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing command", []string{}},
		{"relative command", []string{"--", "python3"}},
		{"dot-relative command", []string{"--", "./normalize.py"}},
		{"zero as-mib", []string{"--as-mib", "0", "--", "/bin/true"}},
		{"negative as-mib", []string{"--as-mib", "-1", "--", "/bin/true"}},
		{"zero cpu-seconds", []string{"--cpu-seconds", "0", "--", "/bin/true"}},
		{"negative cpu-seconds", []string{"--cpu-seconds", "-5", "--", "/bin/true"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newInternalNormalizeExecCmd()
			cmd.SetArgs(tt.args)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			if err := cmd.Execute(); err == nil {
				t.Errorf("expected error for args %v, got nil", tt.args)
			}
		})
	}
}
