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

package store

import "time"

// Restart-window rules for status reports (epic ptone/scion#1956 item 5b).
// The store applies them on the row it locks (authoritative); the hub's
// report handlers apply the same predicates to their snapshot first, so a
// dropped phase never moves a reservation. Both use these functions.

// RestartPreMint reports whether a restart is in flight and has not yet
// recorded its new run: a restart claim is held (claimHeld, claimKind
// restart), the row has a run, and that run was recorded before the claim
// was taken (runIDUpdatedAt unset or earlier than claimAt). The restart
// claim is taken before the stop leg and kept until the restart settles,
// after the start leg has recorded the new run, so the run-time check is
// what limits the rule to the window before the new run exists. A row with
// no run (one not dispatched since run IDs) is excluded: there, the dying
// container's own report is the only record of a stop the restart does not
// write itself.
func RestartPreMint(claimHeld bool, claimKind StartClaimKind, runID string, runIDUpdatedAt, claimAt *time.Time) bool {
	if !claimHeld || claimKind != StartClaimRestart || runID == "" {
		return false
	}
	if runIDUpdatedAt == nil || claimAt == nil {
		return runIDUpdatedAt == nil
	}
	return runIDUpdatedAt.Before(*claimAt)
}

// RestartPreMintReport reports whether su is a report of the row's current
// run, or of no run, while RestartPreMint holds: a broker heartbeat or a
// self-report sent with an agent token. During that window such a report
// can only come from the run being replaced (the new run does not exist
// yet), so its status fields are dropped. A report of another run is
// already dropped whole by the run check.
func RestartPreMintReport(su AgentStatusUpdate, rowRunID string) bool {
	fromReport := su.ReportSource == StatusReportSourceRuntime ||
		(su.ReportSource == StatusReportSourceAgent && su.ReportAgentToken)
	return fromReport && (su.ReportRunID == "" || su.ReportRunID == rowRunID)
}

// RunlessAgentTokenReport reports whether su is a self-report sent with an
// agent token that names no run, against a row whose run was recorded by a
// dispatch (runIDUpdatedAt set). Agent tokens are signed for their run, so
// such a token belongs to no run of this row (a pre-run or label-less
// agent): its status fields are dropped. Users and services with update
// access are not agent tokens and are unaffected; so are broker heartbeats.
func RunlessAgentTokenReport(su AgentStatusUpdate, runIDUpdatedAt *time.Time) bool {
	return su.ReportSource == StatusReportSourceAgent && su.ReportAgentToken &&
		su.ReportRunID == "" && runIDUpdatedAt != nil
}

// DropReportedStatus clears the status fields a dropped report must not
// move: Phase, Activity, ExitCode, ExitReason and Message, the field set of
// the reincarnation guard (Guard 0b). The other fields (ContainerStatus,
// the heartbeat and last-seen) still apply.
func DropReportedStatus(su *AgentStatusUpdate) {
	su.Phase = ""
	su.Activity = ""
	su.ExitCode = nil
	su.ExitReason = ""
	su.Message = ""
}
