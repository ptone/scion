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

import (
	"strings"
	"time"
)

// LiveWork is live work an agent's container saw on its last heartbeat
// (ptone/scion#7855, ptone/scion#9751): a process doing CPU work, a tool call
// in flight, an attached shell, an active exec run, or a post-startup low-CPU
// background process. sciontool init's liveness sampler produces it; the wire
// shape matches pkg/sciontool/liveness.LiveWork.
type LiveWork struct {
	// Kind says what was seen: "process" (CPU work), "tool" (a live
	// tool-call process), "attached", "exec", or "background" (post-startup
	// low-CPU process).
	Kind string `json:"kind,omitempty"`
	// SuppressStall reports whether this work keeps the agent from being
	// marked stalled. Work that does not still protects the agent from
	// auto-suspend.
	SuppressStall bool `json:"suppressStall,omitempty"`
	// Detail is a short display string (process name, pid, CPU time). It
	// is not stored.
	Detail string `json:"detail,omitempty"`
}

// LiveWorkKindBackground is the Kind for Tier 3 post-startup low-CPU
// background processes (ptone/scion#9751). It never suppresses STALLED and is
// bounded by the Hub's runtime-tiered BackgroundLivenessCaps.
const LiveWorkKindBackground = "background"

const (
	// DefaultBackgroundLivenessCapLocal is the default maximum duration a
	// low-CPU background process keeps a local-runtime agent (docker, podman,
	// container) from auto-suspending after its last activity/CPU work.
	DefaultBackgroundLivenessCapLocal = 2 * time.Hour
	// DefaultBackgroundLivenessCapReserved is the default maximum duration a
	// low-CPU background process keeps a reserving/billed-runtime agent
	// (kubernetes, cloudrun, substrate) from auto-suspending.
	DefaultBackgroundLivenessCapReserved = 30 * time.Minute
)

// BackgroundLivenessCaps holds the Hub's effective runtime-tiered extension
// caps for LiveWorkKindBackground (ptone/scion#9751). A zero or negative cap
// disables background liveness protection for that runtime tier.
type BackgroundLivenessCaps struct {
	Local    time.Duration
	Reserved time.Duration
}

// IsReservedLivenessRuntime reports whether runtime holds hard cluster or
// hosted instance reservations (Kubernetes pod requests, Cloud Run / Substrate
// instance allocation) even at zero realized CPU, so it uses the reserved
// background liveness cap.
func IsReservedLivenessRuntime(runtime string) bool {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "kubernetes", "k8s", "remote", "cloudrun", "cloudrun-sandbox", "substrate":
		return true
	default:
		return false
	}
}

// BackgroundLivenessCapFor returns the effective cap for runtime under caps.
// A nil caps pointer falls back to the compiled defaults.
func BackgroundLivenessCapFor(runtime string, caps *BackgroundLivenessCaps) time.Duration {
	if IsReservedLivenessRuntime(runtime) {
		if caps == nil {
			return DefaultBackgroundLivenessCapReserved
		}
		return caps.Reserved
	}
	if caps == nil {
		return DefaultBackgroundLivenessCapLocal
	}
	return caps.Local
}

// BackgroundLivenessAllowed reports whether a LiveWorkKindBackground heartbeat
// at now is within the runtime's background liveness cap measured from the
// agent's most recent real activity anchor (the latest of lastActivityEvent,
// lastLiveWorkAt, lastDeliveryAt, falling back to startedAt when none are set).
// A non-positive cap or missing anchor returns false.
func BackgroundLivenessAllowed(now time.Time, runtime string, caps *BackgroundLivenessCaps, lastActivityEvent, lastLiveWorkAt, lastDeliveryAt, startedAt *time.Time) bool {
	capDur := BackgroundLivenessCapFor(runtime, caps)
	if capDur <= 0 {
		return false
	}
	var anchor *time.Time
	for _, ts := range []*time.Time{lastActivityEvent, lastLiveWorkAt, lastDeliveryAt} {
		if ts == nil || ts.IsZero() {
			continue
		}
		if anchor == nil || ts.After(*anchor) {
			anchor = ts
		}
	}
	if anchor == nil && startedAt != nil && !startedAt.IsZero() {
		anchor = startedAt
	}
	if anchor == nil {
		return false
	}
	return now.Sub(*anchor) < capDur
}

// LiveWorkFreshness is how recent LastLiveProcessAt must be for
// HasRecentLiveWork: three container heartbeats.
const LiveWorkFreshness = 90 * time.Second

// HasRecentLiveWork reports whether the agent's container reported live
// work of any kind within LiveWorkFreshness of now. A sweep that would stop
// the agent (auto-suspend of a stalled agent, or of an idle one) skips an
// agent for which this is true: live work is never suspended.
func (a *Agent) HasRecentLiveWork(now time.Time) bool {
	if a == nil || a.LastLiveProcessAt == nil {
		return false
	}
	return now.Sub(*a.LastLiveProcessAt) < LiveWorkFreshness
}
