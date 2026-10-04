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
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Crash consistency and same-process serialisation for the durable agent
// state (substrate_state.go):
//
//   - substrateIDLocks is the per-id mutex Run, Delete and the reconciler
//     take, so a background sweep can never interleave with a live Run or
//     Delete of the same agent in this process. Across processes the
//     resourceVersion compare-and-swap on the state object, plus the
//     pending-staleness threshold, are what keep them apart.
//   - takeOverOrphanedState is Run's stale takeover: an id claimed by a
//     state object whose agent is provably gone is released at once instead
//     of blocking reuse of the name until the next sweep.
//   - reconcileOnce is the bounded background sweep that converges every
//     state object a crashed or failed Run/Delete left behind.
//
// Nothing here logs or wraps state data: log lines carry counts, the
// state object's (hashed) name, and gRPC status codes only.

const (
	// defaultStateReconcileInterval is the sweep period when
	// runtimes.<name>.substrate.state_reconcile_interval is unset.
	defaultStateReconcileInterval = 10 * time.Minute
	// minStateReconcileInterval is the smallest accepted
	// state_reconcile_interval.
	minStateReconcileInterval = time.Minute
	// stateReconcileStartupDelay is how long after startup the first sweep
	// runs.
	stateReconcileStartupDelay = time.Minute
	// maxReconcileObjectsPerSweep bounds how many state objects one sweep
	// examines (each costs a GetActor, and possibly a few writes). A sweep
	// resumes after the last id the previous one examined, so a larger
	// population is still covered over consecutive sweeps.
	maxReconcileObjectsPerSweep = 256
	// reconcileOpTimeout bounds each object's examine-and-act step.
	reconcileOpTimeout = 30 * time.Second
)

// stateReconcileInterval resolves sc.StateReconcileInterval. NewSubstrateRuntime
// has already validated it (validateStateReconcileInterval), so an
// unparsable value here only comes from a runtime built by another path
// (tests) and falls back to the default.
func stateReconcileInterval(sc config.V1SubstrateConfig) time.Duration {
	if sc.StateReconcileInterval == "" {
		return defaultStateReconcileInterval
	}
	d, err := time.ParseDuration(sc.StateReconcileInterval)
	if err != nil || d < minStateReconcileInterval {
		return defaultStateReconcileInterval
	}
	return d
}

// validateStateReconcileInterval checks the optional
// state_reconcile_interval: empty (the 10m default), or a Go duration of at
// least one minute.
func validateStateReconcileInterval(sc *config.V1SubstrateConfig) error {
	if sc.StateReconcileInterval == "" {
		return nil
	}
	d, err := time.ParseDuration(sc.StateReconcileInterval)
	if err != nil {
		return fmt.Errorf("substrate: runtimes.<name>.substrate.state_reconcile_interval %q is not a Go duration (e.g. \"10m\")", sc.StateReconcileInterval)
	}
	if d < minStateReconcileInterval {
		return fmt.Errorf("substrate: runtimes.<name>.substrate.state_reconcile_interval %q is below the minimum of %s", sc.StateReconcileInterval, minStateReconcileInterval)
	}
	return nil
}

// ---------------------------------------------------------------------
// Per-id mutex
// ---------------------------------------------------------------------

// substrateIDLocks is an in-process mutex per agent id. An entry exists only
// while its id is held, so the map never grows beyond the number of
// in-flight operations.
type substrateIDLocks struct {
	mu      sync.Mutex
	held    map[string]chan struct{}
	waiting map[string]int // goroutines blocked in lock, per id
}

func newSubstrateIDLocks() *substrateIDLocks {
	return &substrateIDLocks{held: make(map[string]chan struct{}), waiting: make(map[string]int)}
}

// substrateProcessIDLocks is the process-wide lock set every
// SubstrateRuntime uses by default: per-agent state is process-wide (see
// substrateAgentStateMu), so its serialisation must be too.
var substrateProcessIDLocks = newSubstrateIDLocks()

// lock blocks until id is free or ctx is done. The returned func releases
// it and must be called exactly once.
func (l *substrateIDLocks) lock(ctx context.Context, id string) (func(), error) {
	for {
		unlock, wait := l.acquire(id)
		if unlock != nil {
			return unlock, nil
		}
		l.addWaiter(id, 1)
		select {
		case <-wait:
			l.addWaiter(id, -1)
		case <-ctx.Done():
			l.addWaiter(id, -1)
			return nil, ctx.Err()
		}
	}
}

func (l *substrateIDLocks) addWaiter(id string, delta int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.waiting[id] += delta; l.waiting[id] <= 0 {
		delete(l.waiting, id)
	}
}

// waiters reports how many goroutines are blocked waiting for id.
func (l *substrateIDLocks) waiters(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waiting[id]
}

// tryLock takes id only if it is free right now.
func (l *substrateIDLocks) tryLock(id string) (func(), bool) {
	unlock, _ := l.acquire(id)
	return unlock, unlock != nil
}

// acquire returns either a release func (id taken) or a channel closed when
// the current holder releases it.
func (l *substrateIDLocks) acquire(id string) (func(), <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ch, busy := l.held[id]; busy {
		return nil, ch
	}
	ch := make(chan struct{})
	l.held[id] = ch
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.held, id)
			l.mu.Unlock()
			close(ch)
		})
	}, nil
}

// idLocks returns the lock set r serialises on.
func (r *SubstrateRuntime) idLocks() *substrateIDLocks {
	if r.locks != nil {
		return r.locks
	}
	return substrateProcessIDLocks
}

// ---------------------------------------------------------------------
// Orphan classification
// ---------------------------------------------------------------------

// pendingStaleAfter is the age beyond which a pending state object cannot
// belong to a live Run in any process: 2 × (template wait + healthz
// timeout). A live Run writes its pending object after the template wait
// and commits it within one healthz timeout plus a few bounded RPCs, so it
// is always younger than this.
func (r *SubstrateRuntime) pendingStaleAfter() time.Duration {
	healthz := r.healthzTimeout
	if healthz <= 0 {
		healthz = defaultHealthzTimeout
	}
	return 2 * (templateReadyTimeout(r.cfg) + healthz)
}

// pendingIsStale reports whether st (pending) is older than the staleness
// threshold. An object whose age is unknown is never stale.
func (r *SubstrateRuntime) pendingIsStale(st *substrateAgentState) bool {
	if st.created.IsZero() {
		return false
	}
	return r.now().Sub(st.created) > r.pendingStaleAfter()
}

// actorPresence is the result of looking up a state object's actor.
type actorPresence int

const (
	// actorAbsent: no actor by that name, or one whose UID differs from
	// the UID the state object recorded (a different incarnation).
	actorAbsent actorPresence = iota
	// actorPresent: an actor by that name whose UID matches the recorded
	// one, or any actor by that name when no UID was recorded yet.
	actorPresent
)

// lookupStateActor finds st's actor by name. Any lookup failure other than
// NotFound is returned as an error, and callers then leave the object alone
// (fail closed: an unreachable control plane never makes an agent look
// gone).
func (r *SubstrateRuntime) lookupStateActor(ctx context.Context, st *substrateAgentState) (actorPresence, error) {
	atespace, actorName, err := splitSubstrateID(st.ID)
	if err != nil {
		return actorAbsent, err
	}
	actor, err := r.client.GetActor(ctx, &ateapipb.GetActorRequest{Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: actorName}})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return actorAbsent, nil
		}
		return actorAbsent, err
	}
	if st.ActorUID != "" && actor.GetMetadata().GetUid() != st.ActorUID {
		return actorAbsent, nil
	}
	return actorPresent, nil
}

// evictSubstrateAgentCache drops id's cached token and exec secrets and the
// record cached under uid, after the state object they mirrored is gone.
func evictSubstrateAgentCache(id, uid string) {
	substrateAgentStateMu.Lock()
	defer substrateAgentStateMu.Unlock()
	delete(substrateControlTokens, id)
	delete(substrateExecSecrets, id)
	if uid != "" {
		delete(substrateAgentRecords, uid)
	}
}

// grpcCode renders err as its gRPC status code only: a control-plane error
// message is never logged, since nothing guarantees it is free of data.
func grpcCode(err error) string {
	return status.Code(err).String()
}

// takeOverOrphanedState is Run's stale takeover (design §3.4 step 3),
// called with id's lock held after store.Create returned errStateExists.
// It reports whether the existing object was an orphan it removed, in
// which case Run retries the claim once. An object is an orphan when it is
// committed and its actor is absent (NotFound, or a different UID), or
// pending, older than the staleness threshold, and without an actor. A
// fresh pending object is a live Run in another process and is never taken
// over; a deleting object belongs to a Delete. A vanished object (deleted
// between the Create and this read) also reports true, so the claim is
// retried.
func (r *SubstrateRuntime) takeOverOrphanedState(ctx context.Context, id string) bool {
	existing, err := r.state.Get(ctx, id)
	if errors.Is(err, errStateNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	switch existing.Phase {
	case substrateStateCommitted:
	case substrateStatePending:
		if !r.pendingIsStale(existing) {
			return false
		}
	default:
		return false
	}
	presence, err := r.lookupStateActor(ctx, existing)
	if err != nil || presence == actorPresent {
		return false
	}
	name := substrateStateObjectName(id)
	if err := r.state.Delete(ctx, id, existing.version); err != nil {
		runtimeLog.Warn("substrate: could not take over orphaned agent state",
			"object", name, "phase", string(existing.Phase), "error", err)
		return false
	}
	evictSubstrateAgentCache(id, existing.ActorUID)
	runtimeLog.Info("substrate: took over orphaned agent state",
		"object", name, "phase", string(existing.Phase))
	return true
}

// ---------------------------------------------------------------------
// Reconciler
// ---------------------------------------------------------------------

// reconcileAction names what the sweep did with one state object; the
// value is also the log/metric label.
type reconcileAction string

const (
	reconcileLeft             reconcileAction = "left"
	reconcileBusy             reconcileAction = "busy"
	reconcileGone             reconcileAction = "gone"
	reconcileError            reconcileAction = "error"
	reconcilePendingNoActor   reconcileAction = "pending-stale-actor-absent"
	reconcilePendingActor     reconcileAction = "pending-stale-actor-present"
	reconcileDeletingActor    reconcileAction = "deleting-actor-present"
	reconcileDeletingNoActor  reconcileAction = "deleting-actor-absent"
	reconcileCommittedNoActor reconcileAction = "committed-actor-absent"
)

// substrateReconcileStats counts one sweep's outcomes.
type substrateReconcileStats struct {
	Examined int
	Actions  map[reconcileAction]int
}

func (s substrateReconcileStats) logAttrs() []any {
	attrs := []any{"examined", s.Examined}
	keys := make([]string, 0, len(s.Actions))
	for k := range s.Actions {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	for _, k := range keys {
		attrs = append(attrs, k, s.Actions[reconcileAction(k)])
	}
	return attrs
}

// substrateReconciler holds the sweep's cursor between runs.
type substrateReconciler struct {
	mu     sync.Mutex
	cursor string // the last id the previous sweep examined
}

// reconcileOnce runs one bounded sweep over the state objects (design
// §3.5). Every object is examined under its per-id lock (skipped if a live
// Run/Delete in this process holds it) and acted on with compare-and-swap,
// so it can race neither a same-process operation nor, past the staleness
// threshold, a live Run in another process. The table it implements:
//
//	state object                actor                          action
//	pending, stale              absent                         delete the state
//	pending, stale              present (UID matches / none)   delete the actor, then the state
//	deleting                    present                        retry DeleteActor, then delete the state
//	deleting                    absent                         delete the state
//	committed                   absent (NotFound/UID mismatch) delete the state
//	(none)                      present                        left alone (legacy record-less)
//
// Everything else (a fresh pending object, a committed object with its
// actor) is left alone. A sweep only ever starts from state objects, so an
// actor with no state object is never touched.
func (r *SubstrateRuntime) reconcileOnce(ctx context.Context) (substrateReconcileStats, error) {
	stats := substrateReconcileStats{Actions: map[reconcileAction]int{}}
	if r.state == nil {
		return stats, nil
	}
	states, err := r.state.List(ctx, "")
	if err != nil {
		return stats, fmt.Errorf("substrate: reconcile agent state: %w", err)
	}
	ids := make([]string, 0, len(states))
	for _, st := range states {
		ids = append(ids, st.ID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)

	rc := r.reconcilerState()
	rc.mu.Lock()
	cursor := rc.cursor
	rc.mu.Unlock()
	// Resume after the previous sweep's last id, wrapping around.
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > cursor })
	ordered := append(slices.Clone(ids[start:]), ids[:start]...)
	if len(ordered) > maxReconcileObjectsPerSweep {
		ordered = ordered[:maxReconcileObjectsPerSweep]
	}

	for _, id := range ordered {
		if ctx.Err() != nil {
			break
		}
		stats.Examined++
		stats.Actions[r.reconcileID(ctx, id)]++
		rc.mu.Lock()
		rc.cursor = id
		rc.mu.Unlock()
	}
	if len(ordered) == len(ids) {
		// Covered everything: the next sweep starts from the beginning.
		rc.mu.Lock()
		rc.cursor = ""
		rc.mu.Unlock()
	}
	return stats, nil
}

func (r *SubstrateRuntime) reconcilerState() *substrateReconciler {
	substrateReconcilersMu.Lock()
	defer substrateReconcilersMu.Unlock()
	if r.reconciler == nil {
		r.reconciler = &substrateReconciler{}
	}
	return r.reconciler
}

// reconcileID examines and, if it is an orphan, converges one state
// object. It re-reads the object under the lock, so it always acts on the
// current version.
func (r *SubstrateRuntime) reconcileID(parent context.Context, id string) reconcileAction {
	unlock, ok := r.idLocks().tryLock(id)
	if !ok {
		return reconcileBusy
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(parent, reconcileOpTimeout)
	defer cancel()
	name := substrateStateObjectName(id)

	st, err := r.state.Get(ctx, id)
	if errors.Is(err, errStateNotFound) {
		return reconcileGone
	}
	if err != nil {
		runtimeLog.Warn("substrate: reconciler could not read agent state", "object", name, "error", err)
		return reconcileError
	}
	if st.Phase == substrateStatePending && !r.pendingIsStale(st) {
		return reconcileLeft
	}
	presence, err := r.lookupStateActor(ctx, st)
	if err != nil {
		runtimeLog.Warn("substrate: reconciler could not look up actor", "object", name, "code", grpcCode(err))
		return reconcileError
	}

	var action reconcileAction
	switch {
	case st.Phase == substrateStateCommitted && presence == actorPresent:
		return reconcileLeft
	case st.Phase == substrateStateCommitted:
		action = reconcileCommittedNoActor
	case st.Phase == substrateStatePending && presence == actorAbsent:
		action = reconcilePendingNoActor
	case st.Phase == substrateStatePending:
		// The broker died mid-Run and its caller saw a failure. Claim the
		// object as deleting first (CAS), so a crash from here on leaves a
		// deleting object the next sweep finishes.
		action = reconcilePendingActor
		st.Phase = substrateStateDeleting
		if err := r.state.Update(ctx, st); err != nil {
			if errors.Is(err, errStateConflict) || errors.Is(err, errStateNotFound) {
				return reconcileLeft
			}
			runtimeLog.Warn("substrate: reconciler could not mark agent state deleting", "object", name, "error", err)
			return reconcileError
		}
	case presence == actorPresent: // deleting
		action = reconcileDeletingActor
	default: // deleting, actor absent
		action = reconcileDeletingNoActor
	}

	if action == reconcilePendingActor || action == reconcileDeletingActor {
		if err := r.deleteActorAndPolicy(ctx, id); err != nil {
			runtimeLog.Warn("substrate: reconciler could not delete actor; will retry",
				"object", name, "rule", string(action), "code", grpcCode(err))
			return reconcileError
		}
	}
	if err := r.state.Delete(ctx, id, st.version); err != nil {
		if errors.Is(err, errStateConflict) {
			// Changed under us (another process): look again next sweep.
			return reconcileLeft
		}
		runtimeLog.Warn("substrate: reconciler could not delete agent state", "object", name, "rule", string(action), "error", err)
		return reconcileError
	}
	evictSubstrateAgentCache(id, st.ActorUID)
	runtimeLog.Info("substrate: reconciler removed orphaned agent state", "object", name, "rule", string(action))
	return action
}

// deleteActorAndPolicy removes id's egress policy and actor, treating
// NotFound as success, the way Delete does.
func (r *SubstrateRuntime) deleteActorAndPolicy(ctx context.Context, id string) error {
	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		return err
	}
	ref := &ateapipb.ObjectRef{Atespace: atespace, Name: actorName}
	if _, err := r.client.DeleteActorEgressPolicy(ctx, &ateapipb.DeleteActorEgressPolicyRequest{Actor: ref}); err != nil && status.Code(err) != codes.NotFound {
		return err
	}
	if _, err := r.client.DeleteActor(ctx, &ateapipb.DeleteActorRequest{Actor: ref, AnyState: true}); err != nil && status.Code(err) != codes.NotFound {
		return err
	}
	return nil
}

// runReconciler sweeps once stateReconcileStartupDelay after it starts and
// then every interval, until ctx is done. after is the timer (time.After in
// production; a test seam).
func (r *SubstrateRuntime) runReconciler(ctx context.Context, interval time.Duration, after func(time.Duration) <-chan time.Time) {
	wait := stateReconcileStartupDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		}
		stats, err := r.reconcileOnce(ctx)
		if err != nil {
			runtimeLog.Warn("substrate: agent state reconcile sweep failed", "error", err)
		} else {
			runtimeLog.Info("substrate: agent state reconcile sweep", stats.logAttrs()...)
		}
		wait = interval
	}
}

// substrateReconcilers records which state namespaces already have a
// reconciler in this process. NewSubstrateRuntime builds one runtime per
// distinct config, and several can share a state namespace; one sweep per
// namespace is enough (concurrent ones would be safe — per-id locks and
// CAS — just redundant).
var (
	substrateReconcilersMu sync.Mutex
	substrateReconcilers   = map[string]context.CancelFunc{}
)

// startStateReconciler starts r's background sweep unless this process
// already sweeps r's state namespace. It runs for the life of the process
// (runtimes are memoized and never closed).
func (r *SubstrateRuntime) startStateReconciler() {
	if r.state == nil {
		return
	}
	ns := strings.TrimSpace(r.cfg.StateNamespace)
	substrateReconcilersMu.Lock()
	defer substrateReconcilersMu.Unlock()
	if _, running := substrateReconcilers[ns]; running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	substrateReconcilers[ns] = cancel
	go r.runReconciler(ctx, stateReconcileInterval(r.cfg), time.After)
}
