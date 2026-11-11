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
	"log/slog"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/tmuxdeliver"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// harnessReadyTimings are the readiness wait timings; zero fields use the
// defaults (tmuxdeliver.Timings.WithDefaults). Tests set them.
type harnessReadyTimings = tmuxdeliver.Timings

// readyMemoStore remembers readiness outcomes per init-run nonce for one
// manager (tmuxdeliver.MemoStore), keyed by container ID.
type readyMemoStore struct {
	tmuxdeliver.MemoStore
}

// clear forgets every outcome (Close).
func (s *readyMemoStore) clear() { s.Clear() }

// waitHarnessReady waits until the agent's harness is ready to read a pasted
// message, polling with one exec per probe (tmuxdeliver.Wait; see
// tmuxdeliver.ParseProbe for what "ready" means per mode). It is MessageBuffer.waitReady for buffered
// flushes.
//
//   - It runs in the asynchronous buffered flush, after the runtime has
//     already answered the hub, so senders never block on it.
//   - Interrupts are delivered synchronously by Message and never wait.
//   - A message can therefore sit in memory for up to the deadline (90s by
//     default) before it is pasted; that window is covered by the hub
//     re-send tracked in ptone/scion#4487.
//   - A blocking wait option for senders is out of scope (ptone/scion#4485).
//   - An interrupt sent after a buffered message can be pasted before it,
//     overtaking it by up to the wait deadline.
//
// It waits once per flush, never per retry, and does not hold the injection
// lock. If readiness first appears during the wait, or the ready marker was
// written less than the settle period ago (even on the first probe), it
// sleeps the settle period before returning. If a probe fails and the
// container is gone (the exec reports it missing, or the runtime lookup
// shows no running container), it returns at once so delivery fails as it
// does today, instead of probing until the deadline. At the deadline it logs a
// warning and returns, and the message is pasted as before; that is not a
// delivery failure.
//
// The settle and deadline come from the init-run file's line 2 when the
// image writes one (see tmuxdeliver.ParseProbe), checked on every probe; the
// deadline is still measured from the start of the flush. Otherwise the
// manager's timings apply.
//
// Outcomes are remembered per init-run nonce (the manager's readyMemos): once a
// hook-mode wait for nonce N reaches the deadline, later flushes paste as
// soon as a probe shows the same nonce still not ready, and the warning is
// logged once per nonce. Once N has been seen ready, a later wait does not
// settle unless the marker is fresh.
//
// It returns at once when ctx is cancelled (MessageBuffer.Close), so shutdown
// never waits. Any error resolving the target returns at once too: delivery
// then reports it as before.
func (m *AgentManager) waitHarnessReady(ctx context.Context, agentID, projectID string) {
	target, matches, err := m.findMessageTargetCounted(ctx, agentID, projectID)
	if err != nil {
		return
	}
	// An owned manager never probes among several same-named agents of
	// different projects: return before any probe, and delivery then
	// refuses the name (deliverImmediate).
	if err := m.refuseAmbiguousOwnedTarget(agentID, projectID, matches); err != nil {
		slog.Debug("harness readiness not waited for: ambiguous target", "agent", agentID, "error", err)
		return
	}

	res := tmuxdeliver.Wait(ctx, tmuxdeliver.WaitConfig{
		Runner:  m.execRunner(target.ContainerID),
		Timings: m.readyTimings,
		Memo:    &m.readyMemos.MemoStore,
		Key:     target.ContainerID,
		Gone: func(ctx context.Context, probeErr error) bool {
			return m.readinessTargetGone(ctx, agentID, projectID, probeErr)
		},
	})
	switch res.Outcome {
	case tmuxdeliver.WaitSettled:
		slog.Info("message delivery waited for harness readiness",
			"agent", agentID,
			"mode", res.Mode,
			"waited_ms", res.Waited.Milliseconds(),
		)
	case tmuxdeliver.WaitRunTimedOut:
		slog.Debug("harness readiness: this run already reached the deadline; delivering without waiting",
			"agent", agentID, "reason", res.Reason)
	case tmuxdeliver.WaitTargetGone:
		slog.Debug("harness readiness wait stopped: container gone", "agent", agentID, "error", res.ProbeErr)
	case tmuxdeliver.WaitTimedOut:
		if res.FirstTimeout {
			slog.Warn("harness not ready by deadline; delivering message anyway",
				"agent", agentID,
				"mode", res.Mode,
				"waited_ms", res.Waited.Milliseconds(),
				"reason", res.Reason,
			)
		}
	}
}

// readinessTargetGone reports whether a failed probe means the agent's
// container is gone: the exec says the container does not exist, or the
// same lookup Message uses answers definitively that no running container
// matches. A lookup that itself fails is not taken as gone.
func (m *AgentManager) readinessTargetGone(ctx context.Context, agentID, projectID string, probeErr error) bool {
	if errors.Is(probeErr, runtime.ErrContainerNotFound) {
		return true
	}
	lctx, cancel := context.WithTimeout(ctx, tmuxdeliver.ReadyProbeTimeout)
	defer cancel()
	return m.checkDeliveryTarget(lctx, agentID, projectID) != nil
}
