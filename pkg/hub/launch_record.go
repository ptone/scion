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
	"encoding/json"
	"errors"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Launch ids for hub-initiated starts and restarts (design §3.4).
//
// Before each start or restart dispatch the hub records a launch id on the
// agent row (store.RecordLaunch) and sends it to the broker as "launchId".
// The broker labels a new container with it and answers with the id the
// running container actually carries ("effectiveLaunchId"): the proposed id
// for a new container, the reused container's own label, or "" for an
// unlabelled one. When that differs from the proposed id, the hub adopts it
// (store.AdoptLaunchID), so the row always names the container that runs.
//
// A dispatch handed to the owning hub instance carries the proposed id in its
// dispatch args, and the owner sends that same id rather than recording a
// new one.
//
// When a start or restart fails, the row keeps the proposed id unless the
// failure is confirmed as not acted on (launchNotActed) and this call
// recorded the id. Then no broker acted on the proposed id and no dispatch
// row carries it, so the hub sets the row back to the id it held before,
// again only while the row still holds the proposed id. A failure whose
// outcome is unknown keeps the proposed id: the broker may have acted on it,
// and the next start records a fresh one.

// maxRestartResponseBytes bounds how much of a restart response the hub reads.
const maxRestartResponseBytes = 1 << 20

type launchIDContextKey struct{}

// withDispatchLaunchID returns ctx carrying the launch id a deferred start or
// restart was proposed with, so the owner's dispatch reuses it.
func withDispatchLaunchID(ctx context.Context, launchID string) context.Context {
	if launchID == "" {
		return ctx
	}
	return context.WithValue(ctx, launchIDContextKey{}, launchID)
}

func dispatchLaunchIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(launchIDContextKey{}).(string)
	return id
}

// errDispatchNotHandedOff marks a cross-node lifecycle op that failed before
// its dispatch row was written: no hub instance will send it.
var errDispatchNotHandedOff = errors.New("dispatch not handed off")

// launchProposal is the launch id a start or restart sends. recorded is true
// when this call recorded id, with previous the id the row held before; it
// is false for a deferred dispatch reusing the id it was proposed with.
type launchProposal struct {
	id       string
	previous string
	recorded bool
}

// proposeLaunchID returns the launch id to send with a start or restart of
// kind. A deferred dispatch reuses the id it was proposed with; otherwise a
// new id is recorded on the agent row.
//
// When the agent has a launch in flight, recording is refused and so is the
// dispatch: proposeLaunchID returns an error wrapping
// store.ErrLaunchInFlight and the caller makes no broker call. Any other
// recording failure lets the dispatch go ahead without an id, exactly as
// before launch ids existed.
func (d *HTTPAgentDispatcher) proposeLaunchID(ctx context.Context, agent *store.Agent, kind string) (launchProposal, error) {
	if id := dispatchLaunchIDFromContext(ctx); id != "" {
		return launchProposal{id: id}, nil
	}
	if d.store == nil {
		return launchProposal{}, nil
	}
	id, previous, err := d.store.RecordLaunch(ctx, agent.ID, kind)
	if errors.Is(err, store.ErrLaunchInFlight) {
		return launchProposal{}, err
	}
	if err != nil {
		d.log.Warn("launch id: record failed; dispatching without one",
			"agent_id", agent.ID, "kind", kind, "error", err)
		return launchProposal{}, nil
	}
	agent.LaunchID = id
	return launchProposal{id: id, previous: previous, recorded: true}, nil
}

// adoptEffectiveLaunchID records the id the broker reports for the running
// container when it differs from the proposed one. A response without the
// field (an older broker) leaves the proposed id in place.
func (d *HTTPAgentDispatcher) adoptEffectiveLaunchID(ctx context.Context, agent *store.Agent, proposed string, resp *RemoteAgentResponse) {
	if proposed == "" || resp == nil || resp.EffectiveLaunchID == nil || d.store == nil {
		return
	}
	effective := *resp.EffectiveLaunchID
	if effective == proposed {
		return
	}
	adopted, err := d.store.AdoptLaunchID(ctx, agent.ID, proposed, effective)
	if err != nil {
		d.log.Warn("launch id: adopting the effective id failed",
			"agent_id", agent.ID, "proposed", proposed, "effective", effective, "error", err)
		return
	}
	if !adopted {
		// A later launch has recorded its own id; leave it.
		d.log.Info("launch id: proposal superseded; effective id not adopted",
			"agent_id", agent.ID, "proposed", proposed, "effective", effective)
		return
	}
	agent.LaunchID = effective
}

// launchNotActed reports whether a failed start or restart is confirmed as
// not acted on: the broker marked its error as written before any container
// action, or the op failed before its dispatch row was written. Every other
// failure, including timeouts, transport errors and broker errors without
// the marker, is an unknown outcome.
func launchNotActed(err error) bool {
	if errors.Is(err, errDispatchNotHandedOff) {
		return true
	}
	var statusErr *brokerStatusError
	return errors.As(err, &statusErr) && statusErr.NotActed
}

// settleFailedLaunch handles the launch id of a failed start or restart. A
// failure confirmed as not acted on sets the row back to the previous id
// when this call recorded the proposal and the row still holds it. Every
// other failure keeps the proposed id: the broker may have acted on it.
func (d *HTTPAgentDispatcher) settleFailedLaunch(ctx context.Context, agent *store.Agent, op string, p launchProposal, err error) {
	if p.id == "" {
		return
	}
	if !p.recorded || !launchNotActed(err) || d.store == nil {
		d.log.Info("launch id: dispatch failed; keeping the proposed id",
			"agent_id", agent.ID, "op", op, "launch_id", p.id, "error", err)
		return
	}
	reverted, adoptErr := d.store.AdoptLaunchID(context.WithoutCancel(ctx), agent.ID, p.id, p.previous)
	switch {
	case adoptErr != nil:
		d.log.Warn("launch id: restoring the previous id failed",
			"agent_id", agent.ID, "op", op, "launch_id", p.id, "previous", p.previous, "error", adoptErr)
	case !reverted:
		d.log.Info("launch id: proposal superseded; previous id not restored",
			"agent_id", agent.ID, "op", op, "launch_id", p.id, "previous", p.previous)
	default:
		agent.LaunchID = p.previous
		d.log.Info("launch id: dispatch not acted on; previous id restored",
			"agent_id", agent.ID, "op", op, "launch_id", p.id, "previous", p.previous, "error", err)
	}
}

// decodeRestartResponse parses a restart response body. The broker has
// already accepted the restart, so a body that does not parse only loses the
// response fields.
func decodeRestartResponse(body []byte) *RemoteAgentResponse {
	if len(body) == 0 {
		return nil
	}
	var resp RemoteAgentResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	return &resp
}
