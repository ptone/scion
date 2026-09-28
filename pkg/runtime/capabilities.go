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

package runtime

import (
	"context"
	"errors"
)

// ErrLogsNotSupported is returned by GetLogs when a runtime cannot serve
// per-agent logs at all — for example because the underlying platform has no
// per-actor log RPC, or because logs are only readable at a level shared
// across tenants and would leak another agent's output. runtimebroker maps
// this, via errors.Is, to a fixed 501 response, so a wrapped error's own
// text never reaches a caller in its place. A runtime that supports logs
// never returns this.
var ErrLogsNotSupported = errors.New("agent logs are not available on this runtime")

// RecordlessActor identifies one actor a RecordlessActorProber found running
// with no in-memory record of which request created it — for example, after
// the runtime's own process restarted. UID should be a globally unique,
// backend-assigned identifier (not derived from Name alone), so a caller
// checking more than one runtime instance can dedupe by actor identity
// rather than by a name that can collide across instances.
type RecordlessActor struct {
	Name string
	UID  string
}

// RecordlessActorProber is an optional capability a Runtime may implement
// when it cannot always tell a project-scoped caller "not found" apart from
// "this process lost the record that would prove it" — the substrate
// runtime is one example, since its actors carry no labels the broker can
// query after a restart. RecordlessActors reports the runtime's own scope
// for projectID (for example, a namespace) and the names of any actor in it
// with no such record, so a caller can turn a would-be not-found into an
// explicit, distinguishable error instead of an idempotent success that
// would silently orphan the actor.
//
// A runtime that always keeps an authoritative record of what it is running
// does not need to implement this interface; the type assertion simply
// fails for it, and its callers' behavior is unchanged.
type RecordlessActorProber interface {
	RecordlessActors(ctx context.Context, projectID string) (scope string, actors []RecordlessActor, err error)
}

// PerProfileInstancesRuntime is an optional capability a Runtime may
// implement to say that its instances are bound to a specific profile's
// configuration, so a request naming a different profile of the same
// runtime type needs its own manager rather than the one already built for
// the default runtime. For most runtimes a matching type means the same
// backend and the default manager can serve any profile of that type; a
// runtime whose instance carries profile-specific settings (for example a
// per-profile egress policy) cannot be shared that way.
//
// A runtime that does not implement this interface, or reports false, keeps
// the type-only behavior: a profile resolving to the default runtime's type
// is served by the default manager.
type PerProfileInstancesRuntime interface {
	PerProfileInstances() bool
}

// HasPerProfileInstances reports whether rt implements
// PerProfileInstancesRuntime and reports true.
func HasPerProfileInstances(rt Runtime) bool {
	pp, ok := rt.(PerProfileInstancesRuntime)
	return ok && pp.PerProfileInstances()
}
