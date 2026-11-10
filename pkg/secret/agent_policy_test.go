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

package secret

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAgentEnvTargetAllowed_Table pins the agent-route env name policy.
//
// MUTANT M3a (the LD_ deny prefix removed) fails the LD_PRELOAD,
// LD_AUDIT and LD_LIBRARY_PATH_TOKEN rows. MUTANT M3i (deny rules matched on
// the name as written instead of upper-cased) fails the http_proxy,
// https_proxy, no_proxy and ld_preload rows.
func TestAgentEnvTargetAllowed_Table(t *testing.T) {
	refused := []string{
		// brief-named denies
		"BASH_ENV", "ENV", "LD_PRELOAD", "LD_LIBRARY_PATH", "PATH", "PROMPT_COMMAND",
		"GIT_SSH_COMMAND", "GIT_ASKPASS", "GIT_CONFIG_GLOBAL", "GIT_EXEC_PATH",
		"PYTHONSTARTUP", "PYTHONPATH", "NODE_OPTIONS", "PERL5OPT", "RUBYOPT",
		// interpreter, loader and runtime option names
		"PYTHONHOME", "PERL5LIB", "RUBYLIB", "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "GCONV_PATH",
		"GLIBC_TUNABLES", "IFS", "HOME", "SHELL", "NPM_CONFIG_REGISTRY", "npm_config_registry",
		// build, package-index and pager names
		"GOPROXY", "CC", "CGO_LDFLAGS", "PIP_INDEX_URL", "UV_INDEX_URL", "PYTHONWARNINGS",
		"LESSOPEN", "MANPAGER", "SYSTEMD_PAGER", "SSLKEYLOGFILE",
		// deny wins over a credential-looking suffix
		"GIT_TOKEN", "LD_AUDIT", "LD_LIBRARY_PATH_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN",
		// A2: the scion-env coupling
		"GITHUB_TOKEN", "GITHUB_TOKEN_X",
		// A3: ADC redirection and gcloud overrides
		"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_CONFIG", "CLOUDSDK_CORE_ACCOUNT",
		// A6: proxy, CA bundle and base-URL redirection
		"HTTPS_PROXY", "ALL_PROXY", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "ANTHROPIC_BASE_URL",
		// A7: executable ADC credential sources
		"GOOGLE_EXTERNAL_ACCOUNT_ALLOW_EXECUTABLES",
		// reserved control-plane prefixes
		"SCION_HUB_TOKEN", "GCE_METADATA_HOST",
		// lower-case forms of denied names
		"http_proxy", "https_proxy", "no_proxy", "ld_preload",
		// malformed
		"1_TOKEN", "", "A-B_TOKEN", "A=B", "A B",
		// a CLAUDE_CODE_ name other than the OAuth token
		"CLAUDE_CODE_USE_VERTEX",
	}
	allowed := []string{
		"FOO_TOKEN", "MY_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GH_TOKEN",
		"COPILOT_GITHUB_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "DB_PASSWORD", "SERVICE_SECRET",
		"SSH_KEY", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_REGION", "CLOUD_ML_REGION",
		// ordinary names: any portable name outside the deny rules
		"FOO", "MY_SETTING", "lower_token", "_UNDERSCORE_FIRST", "PYTHON_VERSION",
		"POLICY_KEY_setting_nil_scope__force_false", "TOGGLE_KEY_1",
	}
	for _, name := range refused {
		err := AgentEnvTargetAllowed(name)
		if err == nil {
			t.Errorf("%q: allowed, want refused", name)
		} else if !errors.Is(err, ErrAgentEnvTarget) {
			t.Errorf("%q: error %v does not wrap ErrAgentEnvTarget", name, err)
		}
	}
	for _, name := range allowed {
		if err := AgentEnvTargetAllowed(name); err != nil {
			t.Errorf("%q: refused (%v), want allowed", name, err)
		}
	}
}

// TestAgentFileTargetAllowed_Table pins the agent-route file target policy.
//
// MUTANT M4b (the single-leaf rule replaced by a non-empty leaf check)
// fails the nested and "../" rows. MUTANT M4a (the ~/.copilot/config.json
// exclusion check disabled) is equivalent: that path is still refused
// because it is not on the credential-file allow-list; only the error
// text differs.
func TestAgentFileTargetAllowed_Table(t *testing.T) {
	allowed := append([]string{
		"~/.scion/agent-secrets/cert.pem",
		"~/.scion/agent-secrets/sa-key_1.json",
	}, agentCredentialFileTargets...)
	refused := []string{
		"~/.bashrc", "~/.profile", "~/.bash_profile", "~/.zshrc", "~/.config/fish/config.fish",
		"/etc/profile.d/x.sh", "/etc/ld.so.preload", "/home/scion/.codex/auth.json",
		"~/.scion/agent-secrets/../.bashrc", "~/.scion/agent-secrets/a/b",
		"~/.scion/agent-secrets/.hidden", "~/.scion/agent-secrets/", "~/.scion/agent-secrets/..",
		"~//.bashrc", "~/x/.codex/auth.json", "~/.codex/auth.json/", "~/.ssh/authorized_keys",
		"relative/path", "", "~/.scion/agent-secrets/a\\b",
		// A7: Copilot's settings file stays excluded
		"~/.copilot/config.json",
	}
	for _, target := range allowed {
		if err := AgentFileTargetAllowed(target); err != nil {
			t.Errorf("%q: refused (%v), want allowed", target, err)
		}
	}
	for _, target := range refused {
		err := AgentFileTargetAllowed(target)
		if err == nil {
			t.Errorf("%q: allowed, want refused", target)
		} else if !errors.Is(err, ErrAgentFileTarget) {
			t.Errorf("%q: error %v does not wrap ErrAgentFileTarget", target, err)
		}
	}
}

func TestAgentWrittenTargetRefused(t *testing.T) {
	cases := []struct {
		name string
		meta SecretMeta
		want bool
	}{
		{"user-written env with refused name", SecretMeta{Name: "X", SecretType: TypeEnvironment, Target: "LD_PRELOAD", CreatedBy: "u1", UpdatedBy: "u1"}, false},
		{"agent-written env with refused name", SecretMeta{Name: "X", SecretType: TypeEnvironment, Target: "LD_PRELOAD", CreatedBy: "agent:a1"}, true},
		{"agent-updated env with refused name", SecretMeta{Name: "X", SecretType: TypeEnvironment, Target: "PATH", CreatedBy: "u1", UpdatedBy: "agent:a1"}, true},
		{"agent-written env, empty type, name used", SecretMeta{Name: "BASH_ENV", CreatedBy: "agent:a1"}, true},
		{"agent-written env allowed", SecretMeta{Name: "X_TOKEN", SecretType: TypeEnvironment, Target: "X_TOKEN", CreatedBy: "agent:a1"}, false},
		{"agent-written file outside dir", SecretMeta{Name: "F", SecretType: TypeFile, Target: "~/.bashrc", CreatedBy: "agent:a1"}, true},
		{"agent-written file with empty target (A4)", SecretMeta{Name: "F", SecretType: TypeFile, CreatedBy: "agent:a1"}, true},
		{"agent-written file in dir", SecretMeta{Name: "F", SecretType: TypeFile, Target: "~/.scion/agent-secrets/f", CreatedBy: "agent:a1"}, false},
		{"agent-written variable", SecretMeta{Name: "V", SecretType: TypeVariable, Target: "anything", CreatedBy: "agent:a1"}, false},
	}
	for _, c := range cases {
		if got := AgentWrittenTargetRefused(c.meta); got != c.want {
			t.Errorf("%s: AgentWrittenTargetRefused = %v, want %v", c.name, got, c.want)
		}
	}
}

// harnessAuthConfig is the subset of harnesses/*/config.yaml the drift
// guards read.
type harnessAuthConfig struct {
	Auth struct {
		Types map[string]struct {
			RequiredEnv []struct {
				AnyOf []string `yaml:"any_of"`
			} `yaml:"required_env"`
			RequiredFiles []struct {
				TargetSuffix string `yaml:"target_suffix"`
			} `yaml:"required_files"`
		} `yaml:"types"`
	} `yaml:"auth"`
}

func loadHarnessAuthConfigs(t *testing.T) map[string]harnessAuthConfig {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "harnesses", "*", "config.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no harness configs found (err=%v)", err)
	}
	out := make(map[string]harnessAuthConfig, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var cfg harnessAuthConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		out[p] = cfg
	}
	return out
}

// agentEnvDriftExceptions are harness required_env keys deliberately NOT
// agent-writable. Each must have at least one allowed alternative in the
// same any_of group, so the harness still has an agent-writable credential.
//   - GITHUB_TOKEN: sciontool init copies GITHUB_TOKEN* values into the
//     scion-env file the shell rc files source.
//   - SCION_METADATA_PROJECT_ID: a reserved control-plane name on every route.
var agentEnvDriftExceptions = []string{"GITHUB_TOKEN", "SCION_METADATA_PROJECT_ID"}

// TestAgentEnvAllowList_CoversHarnessRequiredEnv is the env drift guard:
// every harness required_env key is agent-writable, except the pinned
// exceptions, and each exception keeps an allowed alternative in its group.
func TestAgentEnvAllowList_CoversHarnessRequiredEnv(t *testing.T) {
	for path, cfg := range loadHarnessAuthConfigs(t) {
		for typeName, typ := range cfg.Auth.Types {
			for _, group := range typ.RequiredEnv {
				hasAllowed := false
				for _, key := range group.AnyOf {
					if slices.Contains(agentEnvDriftExceptions, key) {
						continue
					}
					if err := AgentEnvTargetAllowed(key); err != nil {
						t.Errorf("%s auth type %s: required_env %q is not agent-writable: %v", path, typeName, key, err)
						continue
					}
					hasAllowed = true
				}
				if !hasAllowed {
					t.Errorf("%s auth type %s: any_of %v has no agent-writable alternative", path, typeName, group.AnyOf)
				}
			}
		}
	}
	for _, key := range agentEnvDriftExceptions {
		if AgentEnvTargetAllowed(key) == nil {
			t.Errorf("drift exception %q is agent-writable; remove it from the exception list", key)
		}
	}
}

// TestDriftGuard_CredentialFileAllowListMatchesHarnessConfigs is the file
// drift guard: the credential-file allow-list is exactly the harness
// required_files target suffixes plus the ADC path, minus the pinned
// exclusions, and the exclusions are refused.
func TestDriftGuard_CredentialFileAllowListMatchesHarnessConfigs(t *testing.T) {
	want := map[string]bool{"~/.config/gcloud/application_default_credentials.json": true}
	for _, cfg := range loadHarnessAuthConfigs(t) {
		for _, typ := range cfg.Auth.Types {
			for _, rf := range typ.RequiredFiles {
				if rf.TargetSuffix == "" {
					continue
				}
				suffix := rf.TargetSuffix
				if !strings.HasPrefix(suffix, "/") {
					suffix = "/" + suffix
				}
				want["~"+suffix] = true
			}
		}
	}
	for _, excluded := range agentCredentialFileExclusions {
		if !want[excluded] {
			t.Errorf("exclusion %q is no longer a harness credential file; remove it", excluded)
		}
		delete(want, excluded)
		if AgentFileTargetAllowed(excluded) == nil {
			t.Errorf("exclusion %q is agent-writable", excluded)
		}
	}
	var wantList []string
	for k := range want {
		wantList = append(wantList, k)
	}
	sort.Strings(wantList)
	got := slices.Clone(agentCredentialFileTargets)
	sort.Strings(got)
	if !slices.Equal(got, wantList) {
		t.Errorf("agentCredentialFileTargets = %v, want %v", got, wantList)
	}
}
