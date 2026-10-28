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

	"github.com/GoogleCloudPlatform/scion/pkg/brokercontrol"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// verifyCreatedMounts re-checks the binds of a container that Run has just
// created, before the start goes any further: the runtime's own view of the
// binds is checked against the Runtime Broker control paths, which bounds a
// path component swapped between the mount-time check and the runtime's
// mount. An inspect failure is retried once at once. A refused bind, or a
// second inspect failure, stops the container and fails the start; the
// workspace is kept. Runtimes that bind no broker-host paths (no
// runtime.MountInspector) have nothing to verify.
func (m *AgentManager) verifyCreatedMounts(ctx context.Context, ref runtime.RunRef, agentName string) error {
	insp, ok := m.Runtime.(runtime.MountInspector)
	if !ok {
		return nil
	}
	binds, err := insp.InspectBindMounts(ctx, ref.ID)
	if err != nil {
		binds, err = insp.InspectBindMounts(ctx, ref.ID)
	}
	if err == nil {
		err = runtime.CheckBindMounts(binds)
		if err == nil {
			return nil
		}
	}
	slog.Warn("Start: stopping an agent whose container mounts could not be verified clear of Runtime Broker control paths",
		"agent", agentName, "code", brokercontrol.ErrorCode, "error", err)
	if stopErr := m.Runtime.Stop(ctx, ref); stopErr != nil {
		slog.Error("Start: failed to stop the agent after a refused container mount",
			"agent", agentName, "error", stopErr)
	}
	if errors.Is(err, brokercontrol.ErrControlPathMount) {
		return fmt.Errorf("agent %q stopped: %w", agentName, err)
	}
	return fmt.Errorf("agent %q stopped: %w: %v", agentName, brokercontrol.ErrControlPathMount, err)
}
