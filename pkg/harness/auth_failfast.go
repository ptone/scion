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
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// ErrNoAuthSatisfied is wrapped by the error CheckStagedAuth returns when
// the staged auth candidates cannot satisfy the harness.
var ErrNoAuthSatisfied = errors.New("no auth method satisfied")

// stagedAuthCandidates is the subset of auth-candidates.json (written by
// ApplyAuthSettings) that the container-side auth selection reads.
type stagedAuthCandidates struct {
	ExplicitType    string            `json:"explicit_type"`
	EnvVars         []string          `json:"env_vars"`
	EnvSecretFiles  map[string]string `json:"env_secret_files"`
	FileSecretFiles map[string]string `json:"file_secret_files"`
	Files           []struct {
		ContainerPath string `json:"container_path"`
	} `json:"files"`
}

// AuthCheckInputs is what CheckStagedAuth knows about the container besides
// the staged auth-candidates.json.
type AuthCheckInputs struct {
	// AgentHome is the broker-side agent home: it holds the staged
	// auth-candidates.json and is bind-mounted over the container home.
	AgentHome string
	// Env is the start env. Counting it only makes the check more
	// permissive; the provisioner itself does not read it (see below).
	Env map[string]string
	// ContainerHome is the container user's home (e.g. /home/scion), used
	// to resolve absolute mount targets. Empty: absolute targets fail open.
	ContainerHome string
	// MountTargets are the targets of the volumes mounted into the
	// container, as configured ("~/..." or absolute).
	MountTargets []string
}

// CheckStagedAuth reports, before a container is started, a start that the
// container-side provisioner is certain to reject because no auth method is
// satisfied. It reads the auth-candidates.json staged in the agent home, the
// same file the provisioner reads.
//
// The provisioner's rule lives in Python (harnesses/scion_harness.py,
// ProvisionContext.select_auth, with each harness's AuthSpec in its
// provision.py), so the two cannot share code. Env selection reads only the
// staged candidates and secret files: the provisioner runs with a minimal
// environment (HOME, PATH, LANG, TZ and SCION_*), so env_fallback sees no
// credential env. File selection also accepts a file that exists in the
// container at the method's target path. The agent home is bind-mounted
// over the container home, so such a file is either in the agent home or
// supplied by a volume mounted at the target or at one of its ancestor
// directories. The check looks at both, and fails open for a mount target it
// cannot resolve. harnesses/auth_failfast_pin_test.go pins the check against
// the real Python selection for every shipped harness: every Go rejection
// must be a provisioner failure.
//
// It returns an error only when the harness declares auth types and either:
//
//  1. an explicit auth type is staged, Go knows that type, and nothing
//     satisfies it (the provisioner never falls back to no-auth for an
//     explicit type); or
//  2. no explicit type is staged, the harness has no no_auth behaviour, and
//     nothing satisfies any declared type. All shipped harnesses declare
//     no_auth, so this branch applies only to custom harness-configs.
//
// A type counts as satisfied when any credential it names is available:
//   - an env key in env_vars or env_secret_files, or set in the start env;
//   - a required file staged as a file secret, staged as an env secret of
//     the same name, or mapped to its target;
//   - a file already present in the agent home at its target (for example,
//     written by an earlier provision before a restart);
//   - a volume mounted at its target or at an ancestor directory.
//
// This is more permissive than both the harness-config auth.types and the
// provisioner, so where they differ the start is allowed.
//
// The error names the harness, the auth types it accepts and the credential
// names it looks for. It never includes values.
func CheckStagedAuth(harnessName string, authMeta *config.HarnessAuthMetadata, noAuth *config.HarnessNoAuthConfig, in AuthCheckInputs) error {
	if authMeta == nil || len(authMeta.Types) == 0 {
		return nil
	}
	staged, err := readStagedAuthCandidates(in.AgentHome)
	if err != nil {
		// An unreadable candidates file is not this check's concern; the
		// provisioner reports it with its own error.
		return nil
	}

	available := func(t api.HarnessAuthTypeMetadata) bool {
		return stagedSatisfiesType(t, staged, in)
	}

	explicit := strings.TrimSpace(staged.ExplicitType)
	if explicit != "" {
		t, ok := authMeta.Types[explicit]
		if !ok || available(t) {
			return nil
		}
		return fmt.Errorf("%w: %s harness: auth type %q is selected but none of its credentials are available (%s); accepted auth types: %s",
			ErrNoAuthSatisfied, harnessName, explicit, describeAuthType(t), strings.Join(sortedTypeNames(authMeta), ", "))
	}

	if noAuth != nil && strings.TrimSpace(noAuth.Behavior) != "" {
		return nil
	}
	for _, t := range authMeta.Types {
		if available(t) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s harness: no auth method is satisfied and the harness does not allow starting without credentials; provide credentials for one of the accepted auth types: %s",
		ErrNoAuthSatisfied, harnessName, strings.Join(sortedTypeNames(authMeta), ", "))
}

// CheckStagedAuth applies the package-level CheckStagedAuth to this
// harness-config's auth metadata and no_auth behaviour.
func (c *ContainerScriptHarness) CheckStagedAuth(in AuthCheckInputs) error {
	return CheckStagedAuth(c.entry.Harness, c.entry.Auth, c.entry.NoAuthConfig, in)
}

func readStagedAuthCandidates(agentHome string) (stagedAuthCandidates, error) {
	var staged stagedAuthCandidates
	data, err := os.ReadFile(filepath.Join(agentHome, ".scion", "harness", "inputs", "auth-candidates.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return staged, nil
		}
		return staged, err
	}
	if err := json.Unmarshal(data, &staged); err != nil {
		return staged, err
	}
	return staged, nil
}

// stagedSatisfiesType reports whether any credential type t names is
// available to the container. See CheckStagedAuth for why this is broader
// than the provisioner's own matching.
func stagedSatisfiesType(t api.HarnessAuthTypeMetadata, staged stagedAuthCandidates, in AuthCheckInputs) bool {
	if len(t.RequiredEnv) == 0 && len(t.RequiredFiles) == 0 {
		return true
	}
	hasEnv := func(k string) bool {
		if k == "" {
			return false
		}
		if _, ok := staged.EnvSecretFiles[k]; ok {
			return true
		}
		for _, name := range staged.EnvVars {
			if name == k {
				return true
			}
		}
		return in.Env[k] != ""
	}
	for _, group := range t.RequiredEnv {
		for _, k := range group.AnyOf {
			if hasEnv(k) {
				return true
			}
		}
	}
	for _, rf := range t.RequiredFiles {
		if _, ok := staged.FileSecretFiles[rf.Name]; ok && rf.Name != "" {
			return true
		}
		// A provisioner may read the same credential as an env secret of
		// the file's name (antigravity's oauth-token reads AGY_TOKEN).
		if hasEnv(rf.Name) {
			return true
		}
		for _, k := range rf.AlternativeEnvKeys {
			if hasEnv(k) {
				return true
			}
		}
		for _, target := range requiredFileTargets(rf) {
			for _, f := range staged.Files {
				if strings.HasSuffix(strings.TrimRight(f.ContainerPath, "/"), target) {
					return true
				}
			}
			if in.AgentHome != "" {
				if info, err := os.Stat(filepath.Join(in.AgentHome, filepath.FromSlash(target))); err == nil && !info.IsDir() {
					return true
				}
			}
			for _, mt := range in.MountTargets {
				if mountCoversTarget(mt, target, in.ContainerHome) {
					return true
				}
			}
		}
	}
	return false
}

// mountCoversTarget reports whether a volume mounted at mountTarget may
// supply the home-relative path target (leading "/"): the mount is at target
// or at one of its ancestor directories, up to the home itself. It fails open
// (true) for a mount target it cannot resolve: one with variables, a relative
// one, or an absolute one when the container home is unknown.
func mountCoversTarget(mountTarget, target, containerHome string) bool {
	mt := strings.TrimSpace(mountTarget)
	var rel string
	switch {
	case mt == "" || strings.Contains(mt, "$"):
		return true
	case mt == "~":
		rel = "/"
	case strings.HasPrefix(mt, "~/"):
		rel = mt[1:]
	case strings.HasPrefix(mt, "/"):
		if containerHome == "" {
			return true
		}
		home := path.Clean(containerHome)
		mt = path.Clean(mt)
		if mt != home && !strings.HasPrefix(mt, home+"/") {
			return false
		}
		rel = "/" + strings.TrimPrefix(mt, home)
	default:
		return true
	}
	rel = path.Clean(rel)
	target = path.Clean(target)
	return rel == "/" || rel == target || strings.HasPrefix(target, rel+"/")
}

// requiredFileTargets returns the home-relative paths (with a leading "/")
// where the container looks for required file rf.
func requiredFileTargets(rf api.HarnessAuthFileRequirement) []string {
	var out []string
	if suffix := strings.TrimRight(rf.TargetSuffix, "/"); suffix != "" {
		if !strings.HasPrefix(suffix, "/") {
			suffix = "/" + suffix
		}
		out = append(out, suffix)
	}
	if rf.Field == googleAppCredentialsField {
		out = append(out, strings.TrimPrefix(adcContainerPath, "~"))
	}
	return out
}

// describeAuthType lists the credential names auth type t accepts.
func describeAuthType(t api.HarnessAuthTypeMetadata) string {
	var parts []string
	for _, group := range t.RequiredEnv {
		if len(group.AnyOf) > 0 {
			parts = append(parts, "env "+strings.Join(group.AnyOf, " or "))
		}
	}
	for _, rf := range t.RequiredFiles {
		name := rf.Name
		if name == "" {
			name = rf.Field
		}
		parts = append(parts, "file "+name)
	}
	return "expects " + strings.Join(parts, ", ")
}

func sortedTypeNames(authMeta *config.HarnessAuthMetadata) []string {
	names := make([]string, 0, len(authMeta.Types))
	for name := range authMeta.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
