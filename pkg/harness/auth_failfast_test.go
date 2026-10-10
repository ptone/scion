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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

func failfastAuthMeta() *config.HarnessAuthMetadata {
	return &config.HarnessAuthMetadata{
		Types: map[string]api.HarnessAuthTypeMetadata{
			"api-key": {RequiredEnv: []api.HarnessAuthEnvRequirement{{AnyOf: []string{"EXAMPLE_API_KEY"}}}},
			"auth-file": {RequiredFiles: []api.HarnessAuthFileRequirement{{
				Name: "EXAMPLE_AUTH", TargetSuffix: "/.example/auth.json", Field: "ExampleAuthFile",
			}}},
			"vertex-ai": {
				RequiredEnv: []api.HarnessAuthEnvRequirement{
					{AnyOf: []string{"GOOGLE_CLOUD_PROJECT"}},
					{AnyOf: []string{"GOOGLE_CLOUD_REGION"}},
				},
				RequiredFiles: []api.HarnessAuthFileRequirement{{
					Name: "gcloud-adc", Field: "GoogleAppCredentials", AlternativeEnvKeys: []string{"GOOGLE_APPLICATION_CREDENTIALS"},
				}},
			},
		},
	}
}

func stageCandidates(t *testing.T, payload map[string]interface{}) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".scion", "harness", "inputs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	full := map[string]interface{}{
		"schema_version": 1, "explicit_type": "", "env_vars": []string{},
		"env_secret_files": map[string]string{}, "file_secret_files": map[string]string{}, "files": []interface{}{},
	}
	for k, v := range payload {
		full[k] = v
	}
	data, _ := json.Marshal(full)
	if err := os.WriteFile(filepath.Join(dir, "auth-candidates.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestCheckStagedAuth(t *testing.T) {
	dropToShell := &config.HarnessNoAuthConfig{Behavior: "drop-to-shell"}
	cases := []struct {
		name    string
		staged  map[string]interface{}
		noAuth  *config.HarnessNoAuthConfig
		env     map[string]string
		preFile string // home-relative file created before the check
		mounts  []string
		wantErr string // substring; "" means no error
	}{
		{name: "explicit unsatisfied", staged: map[string]interface{}{"explicit_type": "api-key"},
			noAuth: dropToShell, wantErr: `auth type "api-key" is selected`},
		{name: "explicit unsatisfied names accepted types", staged: map[string]interface{}{"explicit_type": "auth-file"},
			wantErr: "accepted auth types: api-key, auth-file, vertex-ai"},
		{name: "explicit satisfied by env_vars", staged: map[string]interface{}{"explicit_type": "api-key", "env_vars": []string{"EXAMPLE_API_KEY"}}},
		{name: "explicit satisfied by env secret file", staged: map[string]interface{}{"explicit_type": "api-key",
			"env_secret_files": map[string]string{"EXAMPLE_API_KEY": "$HOME/.scion/harness/secrets/EXAMPLE_API_KEY"}}},
		{name: "explicit satisfied by container env", staged: map[string]interface{}{"explicit_type": "api-key"},
			env: map[string]string{"EXAMPLE_API_KEY": "placeholder"}},
		{name: "explicit satisfied by file secret", staged: map[string]interface{}{"explicit_type": "auth-file",
			"file_secret_files": map[string]string{"EXAMPLE_AUTH": "$HOME/.scion/harness/secrets/EXAMPLE_AUTH"}}},
		{name: "explicit satisfied by env secret named like the file", staged: map[string]interface{}{"explicit_type": "auth-file",
			"env_secret_files": map[string]string{"EXAMPLE_AUTH": "$HOME/.scion/harness/secrets/EXAMPLE_AUTH"}}},
		{name: "explicit satisfied by mounted file", staged: map[string]interface{}{"explicit_type": "auth-file",
			"files": []map[string]string{{"container_path": "~/.example/auth.json"}}}},
		{name: "explicit satisfied by file already in agent home", staged: map[string]interface{}{"explicit_type": "auth-file"},
			preFile: ".example/auth.json"},
		{name: "explicit satisfied by mapped ADC", staged: map[string]interface{}{"explicit_type": "vertex-ai",
			"files": []map[string]string{{"container_path": "~/.config/gcloud/application_default_credentials.json"}}}},
		{name: "explicit vertex-ai satisfied by project alone (permissive)", staged: map[string]interface{}{"explicit_type": "vertex-ai",
			"env_vars": []string{"GOOGLE_CLOUD_PROJECT"}}},
		{name: "explicit satisfied by volume at target", staged: map[string]interface{}{"explicit_type": "auth-file"},
			mounts: []string{"~/.example/auth.json"}},
		{name: "explicit satisfied by volume at ancestor dir", staged: map[string]interface{}{"explicit_type": "auth-file"},
			mounts: []string{"~/.example"}},
		{name: "explicit satisfied by absolute volume under container home", staged: map[string]interface{}{"explicit_type": "auth-file"},
			mounts: []string{"/home/scion/.example/"}},
		{name: "explicit satisfied by volume at ADC path", staged: map[string]interface{}{"explicit_type": "vertex-ai"},
			mounts: []string{"~/.config/gcloud"}},
		{name: "explicit satisfied by unresolvable volume target (fail open)", staged: map[string]interface{}{"explicit_type": "auth-file"},
			mounts: []string{"$CREDS_DIR/auth.json"}},
		{name: "volume elsewhere does not satisfy", staged: map[string]interface{}{"explicit_type": "auth-file"},
			mounts:  []string{"~/.example-other", "~/.example/auth.json.bak", "/workspace/.example", "/home/scion2/.example", "/scion-volumes/scratchpad"},
			wantErr: `auth type "auth-file" is selected`},
		{name: "volume does not satisfy an env-only type", staged: map[string]interface{}{"explicit_type": "api-key"},
			mounts: []string{"~/.example/auth.json"}, wantErr: `auth type "api-key" is selected`},
		{name: "explicit type unknown to Go is allowed", staged: map[string]interface{}{"explicit_type": "custom"}},
		{name: "no explicit, no_auth allows", noAuth: dropToShell},
		{name: "no explicit, no_auth forbids, nothing staged",
			wantErr: "does not allow starting without credentials"},
		{name: "no explicit, blank no_auth behaviour forbids", noAuth: &config.HarnessNoAuthConfig{Behavior: "  "},
			wantErr: "does not allow starting without credentials"},
		{name: "no explicit, no_auth forbids, one type satisfied", staged: map[string]interface{}{"env_vars": []string{"EXAMPLE_API_KEY"}}},
		{name: "no explicit, ambient env does not satisfy", staged: map[string]interface{}{"env_vars": []string{"SCION_UNRELATED"}},
			wantErr: "does not allow starting without credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := stageCandidates(t, tc.staged)
			if tc.preFile != "" {
				p := filepath.Join(home, tc.preFile)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckStagedAuth("example", failfastAuthMeta(), tc.noAuth,
				AuthCheckInputs{AgentHome: home, Env: tc.env, ContainerHome: "/home/scion", MountTargets: tc.mounts})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckStagedAuth = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrNoAuthSatisfied) {
				t.Fatalf("CheckStagedAuth = %v, want ErrNoAuthSatisfied", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "example harness") {
				t.Errorf("error %q, want it to name the harness and contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckStagedAuthNoTypesNeverFails(t *testing.T) {
	home := stageCandidates(t, map[string]interface{}{"explicit_type": "api-key"})
	if err := CheckStagedAuth("example", nil, nil, AuthCheckInputs{AgentHome: home}); err != nil {
		t.Errorf("nil auth metadata: %v", err)
	}
	if err := CheckStagedAuth("example", &config.HarnessAuthMetadata{}, nil, AuthCheckInputs{AgentHome: home}); err != nil {
		t.Errorf("no auth types: %v", err)
	}
}

func TestCheckStagedAuthErrorHasNoValues(t *testing.T) {
	home := stageCandidates(t, map[string]interface{}{"explicit_type": "auth-file",
		"env_vars": []string{"EXAMPLE_API_KEY"}})
	err := CheckStagedAuth("example", failfastAuthMeta(), nil, AuthCheckInputs{AgentHome: home, Env: map[string]string{"OTHER": "placeholder-secret-value"}})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "placeholder-secret-value") {
		t.Errorf("error leaks an env value: %q", err)
	}
}

func TestMountCoversTarget(t *testing.T) {
	const target = "/.example/auth.json"
	cases := []struct {
		mount, home string
		want        bool
	}{
		{"~/.example/auth.json", "/home/scion", true},
		{"~/.example", "/home/scion", true},
		{"~/.example/", "/home/scion", true},
		{"~", "/home/scion", true},
		{"/home/scion", "/home/scion", true},
		{"/home/scion/.example", "/home/scion", true},
		{"/root/.example", "/root", true},
		{"~/.example-other", "/home/scion", false},
		{"~/.example/auth.json.bak", "/home/scion", false},
		{"~/.example/auth.json/sub", "/home/scion", false},
		{"/home/scion2/.example", "/home/scion", false},
		{"/workspace", "/home/scion", false},
		{"/home/scion/.example", "", true}, // home unknown: fail open
		{"${HOME}/.example", "/home/scion", true},
		{"relative/dir", "/home/scion", true},
		{"", "/home/scion", true},
	}
	for _, tc := range cases {
		if got := mountCoversTarget(tc.mount, target, tc.home); got != tc.want {
			t.Errorf("mountCoversTarget(%q, %q, %q) = %t, want %t", tc.mount, target, tc.home, got, tc.want)
		}
	}
}
