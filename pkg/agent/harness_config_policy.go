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

	"github.com/GoogleCloudPlatform/scion/pkg/config"
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
