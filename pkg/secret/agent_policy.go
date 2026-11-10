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
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// This file holds the target policy for secrets written through the
// agent-only route (PUT /api/v1/agents/{id}/secrets/{key}). An agent is not
// trusted to choose an environment variable name or a file path that the
// shell, a language runtime or a harness acts on: agent-written env names
// are checked against a deny-list and file targets against an allow-list,
// both when the secret is written and when it is delivered. Secrets written
// by users and admins are not affected.

// AgentWriterPrefix marks CreatedBy/UpdatedBy values written through the
// agent route: "agent:<agentID>".
const AgentWriterPrefix = "agent:"

// IsAgentWritten reports whether a secret was created or last updated
// through the agent route.
func IsAgentWritten(meta SecretMeta) bool {
	return strings.HasPrefix(meta.CreatedBy, AgentWriterPrefix) || strings.HasPrefix(meta.UpdatedBy, AgentWriterPrefix)
}

// agentEnvNameShape is a portable environment variable name.
var agentEnvNameShape = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// agentEnvDenyExact lists names an agent may never target: names the shell,
// a language runtime, git, gcloud or an HTTP client executes, loads code
// from, or uses to redirect traffic, trust or credentials.
// Some entries are also covered by agentEnvDenySuffixes (HTTP_PROXY,
// NODE_OPTIONS, SSL_CERT_FILE, ...). That redundancy is deliberate: each list
// stays correct on its own if the other changes.
var agentEnvDenyExact = []string{
	"BASH_ENV", "ENV", "PATH", "PROMPT_COMMAND", "PS0", "PS1", "PS2", "PS3", "PS4",
	"IFS", "CDPATH", "SHELLOPTS", "BASHOPTS", "GLOBIGNORE", "HISTFILE", "HOME", "SHELL",
	"TMPDIR", "EDITOR", "VISUAL", "PAGER", "BROWSER", "ZDOTDIR",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME",
	"PYTHONSTARTUP", "PYTHONPATH", "PYTHONHOME", "PYTHONINSPECT",
	"NODE_OPTIONS", "NODE_PATH", "NODE_EXTRA_CA_CERTS",
	"PERL5OPT", "PERL5LIB", "PERLLIB", "RUBYOPT", "RUBYLIB",
	"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "GOFLAGS", "GLIBC_TUNABLES",
	"SSH_ASKPASS", "SUDO_ASKPASS",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
	"ANTHROPIC_BASE_URL",
	// Its value is a path that redirects Application Default Credentials;
	// agent capture stores ADC as the gcloud-adc file instead.
	"GOOGLE_APPLICATION_CREDENTIALS",
	// Enables executable credential sources in an ADC external_account file.
	"GOOGLE_EXTERNAL_ACCOUNT_ALLOW_EXECUTABLES",
	// Names that make a later build, package or pager step fetch, load or
	// run something the writer chose, or write TLS session keys.
	"GOENV", "GOROOT", "GOPROXY", "GOTOOLCHAIN", "GOSUMDB", "GONOSUMDB", "GONOSUMCHECK",
	"GOINSECURE", "GOPRIVATE", "GONOPROXY", "GODEBUG",
	"CC", "CXX", "LD", "AR", "RUSTC_WRAPPER", "RUSTFLAGS",
	"PYTHONWARNINGS", "PYTHONBREAKPOINT", "PYTHONUSERBASE",
	"LESSOPEN", "LESSCLOSE", "MANPAGER", "MAKEFILES", "MAKEFLAGS", "BOTO_CONFIG", "SSLKEYLOGFILE",
}

// agentEnvDenyPrefixes lists name prefixes an agent may never target.
var agentEnvDenyPrefixes = []string{
	"LD_", "DYLD_", "GIT_", "BASH_FUNC_", "NPM_CONFIG_", "CLOUDSDK_", "CLAUDE_CODE_",
	"CGO_", "PIP_", "UV_",
	// sciontool init copies GITHUB_TOKEN* values into the scion-env file that
	// the shell rc files source; that quoting must not be fed agent values.
	"GITHUB_TOKEN",
}

// agentEnvDenyPrefixExceptions are names under a deny prefix that are allowed.
var agentEnvDenyPrefixExceptions = []string{"CLAUDE_CODE_OAUTH_TOKEN"}

// agentEnvDenySuffixes lists name suffixes an agent may never target.
var agentEnvDenySuffixes = []string{
	"_COMMAND", "_OPTIONS", "_OPTS", "_ASKPASS", "_PATH", "_HOME", "_EDITOR",
	"_PROXY", "_BASE_URL", "_CA_BUNDLE", "_CA_CERTS", "_CERT_FILE", "_PAGER",
}

// ErrAgentEnvTarget is wrapped by every AgentEnvTargetAllowed refusal.
var ErrAgentEnvTarget = errors.New("environment target not allowed for agent-written secrets")

// AgentEnvTargetAllowed reports whether an agent-written environment secret
// may target name: any portable name except the deny rules (including the
// reserved control-plane prefixes). The deny rules match case-insensitively,
// because several runtimes also honour lower-case forms (http_proxy,
// https_proxy, no_proxy).
func AgentEnvTargetAllowed(name string) error {
	if !agentEnvNameShape.MatchString(name) {
		return fmt.Errorf("%w: %q must be letters, digits and underscores, not starting with a digit", ErrAgentEnvTarget, name)
	}
	upper := strings.ToUpper(name)
	if IsReservedEnvTarget(upper) || slices.Contains(agentEnvDenyExact, upper) {
		return fmt.Errorf("%w: %q is reserved", ErrAgentEnvTarget, name)
	}
	if !slices.Contains(agentEnvDenyPrefixExceptions, upper) {
		for _, p := range agentEnvDenyPrefixes {
			if strings.HasPrefix(upper, p) {
				return fmt.Errorf("%w: %q is reserved", ErrAgentEnvTarget, name)
			}
		}
	}
	for _, s := range agentEnvDenySuffixes {
		if strings.HasSuffix(upper, s) {
			return fmt.Errorf("%w: %q is reserved", ErrAgentEnvTarget, name)
		}
	}
	return nil
}

// AgentSecretsDir is the only directory an agent-written file secret may
// target, apart from the harness credential files below.
const AgentSecretsDir = "~/.scion/agent-secrets/"

var agentSecretLeaf = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// agentCredentialFileTargets are the harness credential files an agent may
// target exactly (auth capture stores them there). They are generated from
// the required_files target_suffix entries in harnesses/*/config.yaml plus
// the ADC path; the drift guard test keeps them in step.
var agentCredentialFileTargets = []string{
	"~/.claude/.credentials.json",
	"~/.codex/auth.json",
	"~/.config/gcloud/application_default_credentials.json",
	"~/.gemini/antigravity-cli/antigravity-oauth-token",
	"~/.gemini/oauth_creds.json",
	"~/.grok/auth.json",
	"~/.local/share/opencode/auth.json",
	"~/.pi/agent/auth.json",
}

// agentCredentialFileExclusions are harness-declared credential files that
// are deliberately NOT agent-writable: ~/.copilot/config.json is also
// Copilot's settings file and holds trustedFolders, a trust control.
var agentCredentialFileExclusions = []string{"~/.copilot/config.json"}

// ErrAgentFileTarget is wrapped by every AgentFileTargetAllowed refusal.
var ErrAgentFileTarget = errors.New("file target not allowed for agent-written secrets")

// AgentFileTargetAllowed reports whether an agent-written file secret may
// target target: exactly AgentSecretsDir plus one plain file name, or
// exactly one of the harness credential files. Everything else (absolute
// paths, home rc and profile files, other home paths, nested names) is
// refused.
// AgentCredentialFileTargets returns a sorted copy of the harness credential
// files an agent may write as file secrets (besides AgentSecretsDir).
func AgentCredentialFileTargets() []string {
	out := slices.Clone(agentCredentialFileTargets)
	slices.Sort(out)
	return out
}

func AgentFileTargetAllowed(target string) error {
	if leaf, ok := strings.CutPrefix(target, AgentSecretsDir); ok {
		if agentSecretLeaf.MatchString(leaf) {
			return nil
		}
		return fmt.Errorf("%w: %q must be %s<name> with a plain file name", ErrAgentFileTarget, target, AgentSecretsDir)
	}
	if slices.Contains(agentCredentialFileExclusions, target) {
		return fmt.Errorf("%w: %q is a harness settings file and cannot be written by an agent", ErrAgentFileTarget, target)
	}
	if slices.Contains(agentCredentialFileTargets, target) {
		return nil
	}
	return fmt.Errorf("%w: %q must be %s<name> or a harness credential file", ErrAgentFileTarget, target, AgentSecretsDir)
}

// AgentWrittenTargetRefused reports whether a secret must not be delivered
// because it was written through the agent route and its target fails the
// agent target policy: an environment secret whose name (Target, or Name
// when Target is empty) AgentEnvTargetAllowed refuses, or a file secret
// whose target is empty or AgentFileTargetAllowed refuses. Secrets written
// only by users or admins are never refused here.
func AgentWrittenTargetRefused(meta SecretMeta) bool {
	if !IsAgentWritten(meta) {
		return false
	}
	switch meta.SecretType {
	case TypeEnvironment, "":
		target := meta.Target
		if target == "" {
			target = meta.Name
		}
		return AgentEnvTargetAllowed(target) != nil
	case TypeFile:
		return meta.Target == "" || AgentFileTargetAllowed(meta.Target) != nil
	}
	return false
}
