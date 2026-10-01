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

package hub

import (
	"context"
	"log/slog"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Credential revoke reasons recorded on the agent_credentials row (mirrors
// the existing agent_deleted/agent_suspended/refreshed reasons already used
// by the delete, suspend and refresh paths).
const (
	// agentCredentialRevokeReasonCreateFailed covers every create/provision
	// path: sync create, provision/reprovision, create-with-gather (including
	// the handler-level "required env still missing" failure, which the
	// dispatcher itself reports as success), finalize-env, and a failed
	// async-create launch report.
	agentCredentialRevokeReasonCreateFailed = "create_failed"

	// agentCredentialRevokeReasonStartFailed covers a failed DispatchAgentStart
	// (fresh start, resume from suspended/stopped, post-reprovision start,
	// wake). It is never used for DispatchAgentRestart or
	// DispatchAgentResetAuth, which dispatch to a presumed-running agent —
	// revoking every active credential there risks revoking the one a still-
	// running container is actively using over a dispatch failure that says
	// nothing about that container's health.
	agentCredentialRevokeReasonStartFailed = "start_failed"
)

// agentCredentialRevokeTimeout bounds the best-effort revoke call so it
// cannot hang a request/dispatch path indefinitely.
const agentCredentialRevokeTimeout = 5 * time.Second

// revokeAgentCredentialsBestEffort revokes every active credential for
// agentID, logging a warning on failure. It never returns an error: callers
// use it after a create/launch failure has already been decided, and a
// revoke-store failure must not mask or replace that original error.
//
// It detaches from ctx's cancellation before calling the store, because ctx
// may already be cancelled or past its deadline by the time a dispatch
// failure is being handled (the request that triggered the failure may be
// unwinding) — see agent_dm_delivery.go's identical pattern for finalization
// calls made from an error path.
func revokeAgentCredentialsBestEffort(ctx context.Context, credStore store.AgentCredentialStore, agentID, reason string) {
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentCredentialRevokeTimeout)
	defer cancel()
	if _, err := credStore.RevokeAgentCredentialsByAgent(revokeCtx, agentID, "system", reason); err != nil {
		slog.Warn("Failed to revoke agent credentials", "agent_id", agentID, "reason", reason, "error", err)
	}
}

// isUnconfirmedLaunchError reports whether launchError marks a launch that
// the Hub declared dead on its own, without the broker ever confirming the
// container actually stopped (the reaper in pkg/store/entadapter/
// launch_reaper.go: reapDeadline/reapStaleness). A resume
// dispatch (DispatchAgentStart) must not revoke-by-agent on failure in this
// case: the prior credential may still be in active use by a container the
// Hub has simply lost contact with (e.g. a k8s pod partitioned from the Hub),
// and the start/resume retry path's existing container-name-conflict handling
// (handlers_agent_create_helpers.go isContainerNameConflict) exists precisely
// because that container can still be there.
//
// Every other LaunchError value — including a broker-confirmed crash (exit
// code via heartbeat) or a broker-confirmed launch failure (a "failed"
// launch report) — means the broker itself told the Hub the container is
// gone, so revoking on a subsequent start failure is safe. New LaunchError
// values must be added here only if they are likewise declared by the Hub
// on silence rather than confirmed by the broker: ptone/scion#2485's planned
// container_missing, for example, comes from the broker's own inventory
// reconciliation and should keep revoking, not be added to this list.
func isUnconfirmedLaunchError(launchError string) bool {
	switch launchError {
	case store.LaunchErrorLaunchTimeout, store.LaunchErrorBrokerLost:
		return true
	default:
		return false
	}
}
