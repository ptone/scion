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

// ErrHarnessConfigNotEvaluated is wrapped (with ErrHarnessConfigPolicy) into
// a refusal when a harness-config policy is attached but the harness-config
// could not be evaluated while a provisioner wrapper is staged.
var ErrHarnessConfigNotEvaluated = errors.New("harness configuration not permitted by policy")

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
		return nil, fmt.Errorf("%w: %w (harness-config %q: %v)", ErrHarnessConfigPolicy, ErrHarnessConfigNotEvaluated, harnessName, resolveErr)
	}
	return harness.New(harnessType), nil
}

// clearProvisionHookUnlessContainerScript clears a staged provisioner wrapper
// when h is not a container-script harness (see
// harness.ClearHarnessProvisionHook). It applies with or without a policy:
// a wrapper only ever runs for the container-script harness the current
// launch resolved, and was allowed to resolve.
func clearProvisionHookUnlessContainerScript(h api.Harness, agentHome string) error {
	if _, ok := h.(*harness.ContainerScriptHarness); ok {
		return nil
	}
	return harness.ClearHarnessProvisionHook(agentHome)
}
