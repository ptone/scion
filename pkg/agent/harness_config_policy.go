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

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/harness"
)

// ErrHarnessConfigPolicy marks a launch refused by the harness-config policy
// attached to the context (config.ContextWithHarnessConfigPolicy). The
// policy's own error is wrapped alongside it, so callers can recover its
// details with errors.As.
var ErrHarnessConfigPolicy = errors.New("harness-config refused by policy")

// CheckHarnessConfigPolicy evaluates the context's harness-config policy, if
// any, against the harness-config launch has resolved. Policy is evaluated
// where launch resolves the harness-config: after template and
// harness-config resolution (resolveTemplateAndHarnessConfig, used by
// Preflight and ProvisionAgent) and at each harness construction
// (ProvisionAgent and Start), always with the effective entry
// (harness.EffectiveConfig) exactly as it will run. Returns nil when no
// policy is attached.
func CheckHarnessConfigPolicy(ctx context.Context, name string, entry config.HarnessConfigEntry) error {
	policy := config.HarnessConfigPolicyFromContext(ctx)
	if policy == nil {
		return nil
	}
	if err := policy(name, entry); err != nil {
		return fmt.Errorf("%w: %w", ErrHarnessConfigPolicy, err)
	}
	return nil
}

// ErrHarnessConfigNotEvaluated matches (errors.Is) a refusal when a
// harness-config policy is attached but the harness-config could not be
// evaluated while a provisioner wrapper is staged. The refusal itself is a
// *HarnessConfigNotEvaluatedError, which names the harness-config.
var ErrHarnessConfigNotEvaluated = errors.New("harness-config could not be evaluated by policy")

// HarnessConfigNotEvaluatedError names the harness-config the policy could
// not evaluate. Cause is the resolution error, for logs only.
type HarnessConfigNotEvaluatedError struct {
	Name  string
	Cause error
}

func (e *HarnessConfigNotEvaluatedError) Error() string {
	return fmt.Sprintf("harness-config %q could not be evaluated by policy: %v", e.Name, e.Cause)
}

// Is reports a match for ErrHarnessConfigNotEvaluated.
func (e *HarnessConfigNotEvaluatedError) Is(target error) bool {
	return target == ErrHarnessConfigNotEvaluated
}

// harnessAfterResolveError decides what Start does when harness.Resolve fails
// for harnessName. Launch does not run a staged provisioner the policy has
// not evaluated:
//   - a policy is attached and a provisioner wrapper is staged in agentHome:
//     refuse (ErrHarnessConfigPolicy, ErrHarnessConfigNotEvaluated), since
//     the entry could not be evaluated;
//   - otherwise (no policy, or nothing staged): fall back to
//     harness.New(harnessType), which never constructs a container-script
//     harness.
//
// No policy and "policy attached but entry not evaluable" stay distinct: with
// no policy the fallback is unchanged.
func harnessAfterResolveError(ctx context.Context, agentHome, harnessName, harnessType string, resolveErr error) (api.Harness, error) {
	if config.HarnessConfigPolicyFromContext(ctx) != nil && agentHome != "" && harness.HarnessProvisionHookStaged(agentHome) {
		return nil, fmt.Errorf("%w: %w", ErrHarnessConfigPolicy, &HarnessConfigNotEvaluatedError{Name: harnessName, Cause: resolveErr})
	}
	return harness.New(harnessType), nil
}

// resetStagedProvisioning prepares the agent home's staged container-script
// provisioning state for harness h, before h is provisioned, with or without
// a policy:
//   - h is not container-script: clear the provisioner wrapper and the whole
//     staged bundle (harness.ClearStagedProvisioning), so no provisioner runs
//     and sciontool init does not load an earlier provisioner's state;
//   - h is container-script: clear the whole staged bundle
//     (harness.ClearStagedBundle), including inputs/, so nothing a previous
//     provisioning, the workload or copied home content left there is visible
//     to this provisioner. The caller restages the control-plane inputs
//     (ProvisionAgent writes them; Start restores them with
//     restoreControlPlaneInputs) and h restages its own bundle and wrapper.
func resetStagedProvisioning(h api.Harness, agentHome string) error {
	if _, ok := h.(*harness.ContainerScriptHarness); ok {
		return harness.ClearStagedBundle(agentHome)
	}
	return harness.ClearStagedProvisioning(agentHome)
}

// controlPlaneInputsDirName is the directory, in the agent directory (outside
// the agent home the container can write), holding the control plane's copy
// of the per-agent inputs ProvisionAgent stages for a container-script
// harness (instructions, system prompt, resolved skills). It lies outside
// every container mount scion computes for the agent (pinned by
// pkg/runtime's TestHarnessInputsRecordOutsideScionMounts). Author-configured
// volumes are a separately tracked capability, outside the scope of this
// record.
const controlPlaneInputsDirName = config.HarnessInputsRecordDirName

func stagedInputsDir(agentHome string) string {
	return filepath.Join(agentHome, ".scion", "harness", "inputs")
}

// snapshotControlPlaneInputs records the inputs ProvisionAgent has just
// staged (after resetStagedProvisioning cleared the bundle, so the control
// plane is their only writer) into agentDir/harness-inputs, replacing any
// earlier copy. Only regular files are copied.
func snapshotControlPlaneInputs(agentDir, agentHome string) error {
	dst := filepath.Join(agentDir, controlPlaneInputsDirName)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("reset control-plane inputs: %w", err)
	}
	// The record exists (possibly empty) once the control plane has staged
	// inputs, so seedControlPlaneInputsIfAbsent never applies afterwards.
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create control-plane inputs record: %w", err)
	}
	return copyRegularFiles(stagedInputsDir(agentHome), dst)
}

// legacyInputNames are the control-plane inputs seedControlPlaneInputsIfAbsent
// takes from an agent home staged before the control plane recorded them.
var legacyInputNames = []string{"instructions.md", "system-prompt.md", "resolved-skills.json"}

// seedControlPlaneInputsIfAbsent creates the control plane's record of the
// per-agent inputs for an agent provisioned before that record existed. It
// relies on the record lying outside every container mount scion computes
// (see controlPlaneInputsDirName); author-configured volumes are a separately
// tracked capability, outside this change's scope. Only
// when agentDir/harness-inputs is absent, it copies exactly
// instructions.md, system-prompt.md and resolved-skills.json (fixed names,
// no recursion, no globs) from the agent home's current inputs/ into the
// record, once. Each entry is checked with Lstat and accepted only as a
// regular file (mode&os.ModeType == 0): symlinks and every other non-regular
// type are skipped with a warning and never followed. Accepted files are
// copied by content into new files. A warning names the agent when the
// record is seeded. It reports whether it seeded. Re-provisioning the agent
// replaces the record with freshly staged control-plane inputs.
func seedControlPlaneInputsIfAbsent(agentDir, agentHome, agentID string) (bool, error) {
	record := filepath.Join(agentDir, controlPlaneInputsDirName)
	if _, err := os.Lstat(record); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	src := stagedInputsDir(agentHome)
	var seeded bool
	for _, name := range legacyInputNames {
		path := filepath.Join(src, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeType != 0 {
			slog.Warn("harness input not seeded: not a regular file", "agent_id", agentID, "file", name, "mode", info.Mode().Type().String())
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		if err := os.MkdirAll(record, 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(filepath.Join(record, name), data, 0o644); err != nil {
			return false, err
		}
		seeded = true
	}
	if seeded {
		slog.Warn("harness inputs seeded once from pre-existing inputs; re-provision to refresh", "agent_id", agentID)
	}
	return seeded, nil
}

// restoreControlPlaneInputs restages the control plane's copy of the
// per-agent inputs into the agent home's (cleared) inputs/ before a
// container-script harness is provisioned on start, so inputs/ holds only
// control-plane content. Inputs written per launch (auth candidates,
// telemetry, MCP servers) are staged later in Start. With no recorded copy,
// inputs/ stays empty.
func restoreControlPlaneInputs(agentDir, agentHome string) error {
	dst := stagedInputsDir(agentHome)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clear staged inputs: %w", err)
	}
	src := filepath.Join(agentDir, controlPlaneInputsDirName)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	return copyRegularFiles(src, dst)
}

// copyRegularFiles copies the regular files directly in src into dst
// (created 0755). A missing src copies nothing.
func copyRegularFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
