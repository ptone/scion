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
	"slices"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
)

// TestAgentFileTargetMirrorsHubPolicy keeps sciontool's copies of the hub's
// agent file-target policy equal to pkg/secret's: the secrets directory, the
// harness credential files and the file-name rule inside the directory.
func TestAgentFileTargetMirrorsHubPolicy(t *testing.T) {
	if agentSecretsDir != secret.AgentSecretsDir {
		t.Errorf("agentSecretsDir = %q, want secret.AgentSecretsDir %q", agentSecretsDir, secret.AgentSecretsDir)
	}
	got := slices.Sorted(slices.Values(knownCredentialFileTargets))
	if want := secret.AgentCredentialFileTargets(); !slices.Equal(got, want) {
		t.Errorf("knownCredentialFileTargets = %v, want %v", got, want)
	}
	for _, leaf := range []string{"cert.pem", "MY_TOKEN", "a-b.c", "_leading", ".hidden", "a/b", "", "x y"} {
		local := agentSecretLeaf.MatchString(leaf)
		hub := secret.AgentFileTargetAllowed(secret.AgentSecretsDir+leaf) == nil
		if local != hub {
			t.Errorf("leaf %q: sciontool accepts=%v, hub accepts=%v", leaf, local, hub)
		}
	}
}

// TestDefaultFileTarget pins the target used when --target is not given:
// a harness credential file under home keeps its ~/ path, any other file
// goes to the agent secrets directory, and a key that is not a valid file
// name there is refused locally instead of failing at the hub.
func TestDefaultFileTarget(t *testing.T) {
	const home = "/home/u"
	cases := []struct {
		abs, key, want string
		wantErr        bool
	}{
		{"/home/u/.codex/auth.json", "CODEX_AUTH", "~/.codex/auth.json", false},
		{"/home/u/.local/share/opencode/auth.json", "OPENCODE_AUTH", "~/.local/share/opencode/auth.json", false},
		{"/home/u/certs/x.pem", "MY_CERT", "~/.scion/agent-secrets/MY_CERT", false},
		{"/home/u/.copilot/config.json", "COPILOT_CONFIG", "~/.scion/agent-secrets/COPILOT_CONFIG", false},
		{"/tmp/x.pem", "MY_CERT", "~/.scion/agent-secrets/MY_CERT", false},
		{"/tmp/x.pem", "_LEADING", "", true},
	}
	for _, c := range cases {
		got, err := defaultFileTarget(c.abs, home, c.key)
		if c.wantErr {
			if err == nil {
				t.Errorf("defaultFileTarget(%q, %q) = %q, want an error", c.abs, c.key, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("defaultFileTarget(%q, %q) = %q, %v; want %q", c.abs, c.key, got, err, c.want)
			continue
		}
		if err := secret.AgentFileTargetAllowed(got); err != nil {
			t.Errorf("default target %q is refused by the hub policy: %v", got, err)
		}
	}
}
