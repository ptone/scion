// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
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
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Tests for crash consistency and cross-process concurrency over the
// durable agent state: the per-id mutex, Run's stale takeover, and the
// reconciler (one test per row of its table), plus fault injection at every
// store step of Run and Delete and two runtimes ("broker processes") with
// separate lock sets interleaving over one store and one cluster.
//
// Everything is deterministic: interleavings are forced from hooks inside
// the fakes (CreateActor, ResumeActor, DeleteActor, store calls), time is a
// fake clock, and the reconciler loop's timer is a test seam.

// fakeClock is a settable clock shared by the runtime (r.now) and the
// state fake's creationTimestamp stamping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// stampCreationTimes makes the state fake behave like the API server for
// metadata.creationTimestamp: set from clk at create, preserved across
// updates (the store's Update sends none). The reactors fall through to
// newStateFakeClientset's resourceVersion-enforcing ones.
func stampCreationTimes(cs *k8sfake.Clientset, clk *fakeClock) {
	cs.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		obj := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		if cur, err := cs.Tracker().Get(secretsGVR, action.GetNamespace(), obj.Name); err == nil {
			obj.CreationTimestamp = cur.(*corev1.Secret).CreationTimestamp
		}
		return false, nil, nil
	})
	cs.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*corev1.Secret)
		if obj.CreationTimestamp.IsZero() {
			obj.CreationTimestamp = metav1.NewTime(clk.Now())
		}
		return false, nil, nil
	})
}

// serveActorGets makes the fake ateapi's GetActor consult the actors that
// exist (createdActors): NotFound for a missing actor, otherwise the actor
// with its real UID, RUNNING and assigned (so Run's waitRunning passes).
func serveActorGets(fc *fakeControlClient) {
	fc.getActor = func(req *ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
		key := req.GetActor().GetAtespace() + "/" + req.GetActor().GetName()
		fc.mu.Lock()
		a, ok := fc.createdActors[key]
		fc.mu.Unlock()
		if !ok {
			return nil, status.Error(codes.NotFound, "actor not found")
		}
		return &ateapipb.Actor{
			Metadata: &ateapipb.ResourceMetadata{
				Atespace: a.GetMetadata().GetAtespace(),
				Name:     a.GetMetadata().GetName(),
				Uid:      a.GetMetadata().GetUid(),
			},
			Status: &ateapipb.ActorStatus{
				State:            ateapipb.ActorState_ACTOR_STATE_RUNNING,
				WorkerAssignment: &ateapipb.WorkerAssignment{WorkerPod: "w", WorkerNamespace: "ate-workers"},
			},
		}, nil
	}
}

// reconcileHarness is a stateHarness with a fake clock, server-like
// creation timestamps, a GetActor that reflects the cluster, and its own
// lock set (so tests never share the process-wide one).
type reconcileHarness struct {
	*stateHarness
	clk *fakeClock
}

func newReconcileHarness(t *testing.T) *reconcileHarness {
	t.Helper()
	h := newStateHarness(t)
	clk := newFakeClock()
	stampCreationTimes(h.cs, clk)
	serveActorGets(h.fc)
	h.rt.now = clk.Now
	h.rt.locks = newSubstrateIDLocks()
	return &reconcileHarness{stateHarness: h, clk: clk}
}

// otherProcess is a second runtime over the same store and cluster with its
// own lock set and the same clock: another broker process.
func (h *reconcileHarness) otherProcess() *SubstrateRuntime {
	rt := h.secondRuntime()
	rt.now = h.clk.Now
	rt.locks = newSubstrateIDLocks()
	return rt
}

// ageBeyondStaleness moves the clock past the pending-staleness threshold.
func (h *reconcileHarness) ageBeyondStaleness() {
	h.clk.Advance(h.rt.pendingStaleAfter() + time.Second)
}

func reconcileID(name string) string { return substrateAtespaceName(stateTestProjectID) + "/" + name }

// putState writes a state object directly, as a (possibly crashed) broker
// would have left it.
func (h *reconcileHarness) putState(t *testing.T, name string, phase substrateStatePhase, uid string) *substrateAgentState {
	t.Helper()
	st := testState(substrateAtespaceName(stateTestProjectID), name)
	st.Phase = phase
	st.ActorUID = uid
	if err := h.store.Create(context.Background(), st); err != nil {
		t.Fatalf("putState(%s): %v", name, err)
	}
	return st
}

func (h *reconcileHarness) putActor(name, uid string) {
	seedLegacyActor(h.fc, stateTestProjectID, name, uid)
}

func (h *reconcileHarness) removeActorOutOfBand(id string) {
	h.fc.mu.Lock()
	delete(h.fc.createdActors, id)
	h.fc.mu.Unlock()
}

func (h *reconcileHarness) actorExists(id string) bool {
	h.fc.mu.Lock()
	defer h.fc.mu.Unlock()
	_, ok := h.fc.createdActors[id]
	return ok
}

func (h *reconcileHarness) actorCount() int {
	h.fc.mu.Lock()
	defer h.fc.mu.Unlock()
	return len(h.fc.createdActors)
}

func (h *reconcileHarness) stateCount(t *testing.T) int {
	t.Helper()
	all, err := h.store.List(context.Background(), "")
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	return len(all)
}

func (h *reconcileHarness) stateOf(t *testing.T, id string) *substrateAgentState {
	t.Helper()
	st, err := h.store.Get(context.Background(), id)
	if errors.Is(err, errStateNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("store.Get(%s): %v", id, err)
	}
	return st
}

func (h *reconcileHarness) reconcile(t *testing.T, rt *SubstrateRuntime) substrateReconcileStats {
	t.Helper()
	stats, err := rt.reconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	return stats
}

// =====================================================================
// Reconciler: one test per row of the design §3.5 table.
// =====================================================================

// Row 1: pending, older than the staleness threshold, actor absent →
// delete the state object.
func TestSubstrateReconcile_Row1_StalePendingActorAbsent_DeletesState(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("row1")
	h.putState(t, "row1", substrateStatePending, "")
	h.ageBeyondStaleness()

	stats := h.reconcile(t, h.rt)

	if h.stateOf(t, id) != nil {
		t.Error("stale pending state object with no actor survived the sweep")
	}
	if stats.Actions[reconcilePendingNoActor] != 1 {
		t.Errorf("sweep actions = %v, want one %q", stats.Actions, reconcilePendingNoActor)
	}
	if h.calls("DeleteActor") != 0 {
		t.Error("DeleteActor called for a pending object with no actor")
	}
}

// Row 2: pending, stale, actor present with a matching UID or no UID
// recorded → delete the actor, then the state.
func TestSubstrateReconcile_Row2_StalePendingActorPresent_DeletesActorThenState(t *testing.T) {
	for _, tc := range []struct {
		name, recordedUID string
	}{
		{"uid matches", "uid-row2"},
		{"no uid recorded", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newReconcileHarness(t)
			id := reconcileID("row2")
			h.putActor("row2", "uid-row2")
			h.putState(t, "row2", substrateStatePending, tc.recordedUID)
			h.ageBeyondStaleness()

			served := h.fc.deleteActor
			var stateAtDeleteActor *substrateAgentState
			h.fc.deleteActor = func(req *ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
				stateAtDeleteActor, _ = h.store.Get(context.Background(), id)
				return served(req)
			}

			stats := h.reconcile(t, h.rt)

			if h.actorExists(id) {
				t.Error("actor of a stale pending object survived the sweep")
			}
			if h.stateOf(t, id) != nil {
				t.Error("stale pending state object survived the sweep")
			}
			if stateAtDeleteActor == nil || stateAtDeleteActor.Phase != substrateStateDeleting {
				t.Errorf("state at DeleteActor = %+v, want it still present and marked deleting (actor first, then state)", stateAtDeleteActor)
			}
			if stats.Actions[reconcilePendingActor] != 1 {
				t.Errorf("sweep actions = %v, want one %q", stats.Actions, reconcilePendingActor)
			}
		})
	}
}

// Row 3: deleting, actor present → retry DeleteActor, then delete the
// state.
func TestSubstrateReconcile_Row3_DeletingActorPresent_RetriesDeleteActorThenState(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("row3")
	h.putActor("row3", "uid-row3")
	h.putState(t, "row3", substrateStateDeleting, "uid-row3")

	stats := h.reconcile(t, h.rt)

	if h.actorExists(id) {
		t.Error("actor of a deleting object survived the sweep")
	}
	if h.stateOf(t, id) != nil {
		t.Error("deleting state object survived the sweep")
	}
	if h.calls("DeleteActorEgressPolicy") == 0 {
		t.Error("the sweep did not remove the actor's egress policy")
	}
	if stats.Actions[reconcileDeletingActor] != 1 {
		t.Errorf("sweep actions = %v, want one %q", stats.Actions, reconcileDeletingActor)
	}
}

// Row 3, DeleteActor failing: the object stays deleting for the next sweep.
func TestSubstrateReconcile_Row3_DeleteActorFailureKeepsDeletingForRetry(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("row3")
	h.putActor("row3", "uid-row3")
	h.putState(t, "row3", substrateStateDeleting, "uid-row3")
	served := h.fc.deleteActor
	h.fc.deleteActor = func(*ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
		return nil, status.Error(codes.Unavailable, "down")
	}

	stats := h.reconcile(t, h.rt)
	if st := h.stateOf(t, id); st == nil || st.Phase != substrateStateDeleting {
		t.Fatalf("state after a failed DeleteActor = %+v, want it left deleting", st)
	}
	if stats.Actions[reconcileError] != 1 {
		t.Errorf("sweep actions = %v, want one error", stats.Actions)
	}

	h.fc.deleteActor = served
	h.reconcile(t, h.rt)
	if h.stateOf(t, id) != nil || h.actorExists(id) {
		t.Error("the next sweep did not finish the delete")
	}
}

// Row 4: deleting, actor absent → delete the state.
func TestSubstrateReconcile_Row4_DeletingActorAbsent_DeletesState(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("row4")
	h.putState(t, "row4", substrateStateDeleting, "uid-row4")

	stats := h.reconcile(t, h.rt)

	if h.stateOf(t, id) != nil {
		t.Error("deleting state object with no actor survived the sweep")
	}
	if stats.Actions[reconcileDeletingNoActor] != 1 {
		t.Errorf("sweep actions = %v, want one %q", stats.Actions, reconcileDeletingNoActor)
	}
	if h.calls("DeleteActor") != 0 {
		t.Error("DeleteActor called although the actor is absent")
	}
}

// Row 5: committed, actor absent (NotFound by name, or a UID mismatch) →
// delete the state. A same-named actor with a different UID is somebody
// else's and is left running.
func TestSubstrateReconcile_Row5_CommittedActorAbsent_DeletesState(t *testing.T) {
	t.Run("actor not found", func(t *testing.T) {
		h := newReconcileHarness(t)
		id := reconcileID("row5")
		h.putState(t, "row5", substrateStateCommitted, "uid-row5")

		stats := h.reconcile(t, h.rt)

		if h.stateOf(t, id) != nil {
			t.Error("committed state object of a vanished actor survived the sweep")
		}
		if stats.Actions[reconcileCommittedNoActor] != 1 {
			t.Errorf("sweep actions = %v, want one %q", stats.Actions, reconcileCommittedNoActor)
		}
	})
	t.Run("uid mismatch", func(t *testing.T) {
		h := newReconcileHarness(t)
		id := reconcileID("row5")
		h.putActor("row5", "uid-someone-else")
		h.putState(t, "row5", substrateStateCommitted, "uid-row5")

		stats := h.reconcile(t, h.rt)

		if h.stateOf(t, id) != nil {
			t.Error("committed state object naming a different actor incarnation survived the sweep")
		}
		if !h.actorExists(id) || h.calls("DeleteActor") != 0 {
			t.Error("the sweep deleted a same-named actor it does not own")
		}
		if stats.Actions[reconcileCommittedNoActor] != 1 {
			t.Errorf("sweep actions = %v, want one %q", stats.Actions, reconcileCommittedNoActor)
		}
	})
}

// Row 6: no state object, actor present → legacy record-less actor: left
// alone, so the existing 409 + operator path is unchanged.
func TestSubstrateReconcile_Row6_LegacyRecordlessActorLeftAlone(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("legacy")
	h.putActor("legacy", "uid-legacy")

	stats := h.reconcile(t, h.rt)

	if !h.actorExists(id) {
		t.Error("the sweep removed a legacy record-less actor")
	}
	if h.calls("DeleteActor") != 0 || h.calls("DeleteActorEgressPolicy") != 0 {
		t.Error("the sweep touched a legacy record-less actor")
	}
	if stats.Examined != 0 {
		t.Errorf("sweep examined %d objects, want 0 (it starts from state objects only)", stats.Examined)
	}
	_, recordless, err := h.rt.RecordlessActors(context.Background(), stateTestProjectID)
	if err != nil || len(recordless) != 1 {
		t.Errorf("RecordlessActors() = %+v (err %v), want the legacy actor still reported record-less", recordless, err)
	}
}

// Off-table states are left alone: a fresh pending object (a live Run,
// actor or not) and a committed object whose actor exists with a matching
// UID (the criterion-5 invariant).
func TestSubstrateReconcile_LeavesLiveAgentsAlone(t *testing.T) {
	h := newReconcileHarness(t)
	h.putState(t, "fresh-no-actor", substrateStatePending, "")
	h.putActor("fresh-actor", "uid-fresh")
	h.putState(t, "fresh-actor", substrateStatePending, "uid-fresh")
	h.putActor("live", "uid-live")
	h.putState(t, "live", substrateStateCommitted, "uid-live")
	// Old enough for anything except a pending object: just below the
	// pending threshold.
	h.clk.Advance(h.rt.pendingStaleAfter() - time.Second)

	stats := h.reconcile(t, h.rt)

	for _, name := range []string{"fresh-no-actor", "fresh-actor", "live"} {
		if h.stateOf(t, reconcileID(name)) == nil {
			t.Errorf("state object %q was removed, want it left alone", name)
		}
	}
	if !h.actorExists(reconcileID("live")) || !h.actorExists(reconcileID("fresh-actor")) {
		t.Error("the sweep removed a live actor")
	}
	if stats.Actions[reconcileLeft] != 3 {
		t.Errorf("sweep actions = %v, want 3 left", stats.Actions)
	}
	// A committed agent with its actor is left alone at any age.
	h.clk.Advance(1000 * time.Hour)
	h.reconcile(t, h.rt)
	if h.stateOf(t, reconcileID("live")) == nil || !h.actorExists(reconcileID("live")) {
		t.Error("a committed agent with its actor was reaped")
	}
}

// A control-plane lookup failure never makes an agent look gone.
func TestSubstrateReconcile_ActorLookupFailureLeavesObject(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("unknown")
	h.putState(t, "unknown", substrateStateCommitted, "uid-unknown")
	h.fc.getActor = func(*ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
		return nil, status.Error(codes.Unavailable, "down")
	}
	stats := h.reconcile(t, h.rt)
	if h.stateOf(t, id) == nil {
		t.Error("state object removed although its actor could not be looked up")
	}
	if stats.Actions[reconcileError] != 1 {
		t.Errorf("sweep actions = %v, want one error", stats.Actions)
	}
}

// The sweep skips an id whose per-id lock is held by a live operation in
// this process, even when the object would otherwise be reaped.
func TestSubstrateReconcile_SkipsIDHeldByLiveOperation(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("held")
	h.putState(t, "held", substrateStatePending, "")
	h.ageBeyondStaleness()
	unlock, err := h.rt.idLocks().lock(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	stats := h.reconcile(t, h.rt)
	if h.stateOf(t, id) == nil {
		t.Fatal("the sweep reaped an object whose id is locked by a live operation")
	}
	if stats.Actions[reconcileBusy] != 1 {
		t.Errorf("sweep actions = %v, want one busy", stats.Actions)
	}

	unlock()
	h.reconcile(t, h.rt)
	if h.stateOf(t, id) != nil {
		t.Error("the object was not reaped once its id was free")
	}
}

// The per-id mutex against a real Run: a sweep that lands while Run is in
// flight (here between the write-ahead and CreateActor, with the clock
// already past the staleness threshold so the pending object would be
// reaped as "stale, no actor") must leave it alone, and Run must succeed.
func TestSubstrateReconcile_DoesNotRaceSameProcessRun(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("inflight")
	defaultCreate := h.fc.createActor
	var sweep substrateReconcileStats
	h.fc.createActor = func(req *ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
		h.ageBeyondStaleness()
		sweep = h.reconcile(t, h.rt)
		return defaultCreate(req)
	}

	if _, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "inflight")); err != nil {
		t.Fatalf("Run() error = %v, want success (the sweep must not reap an in-flight Run)", err)
	}
	if sweep.Actions[reconcileBusy] != 1 {
		t.Errorf("mid-Run sweep actions = %v, want one busy", sweep.Actions)
	}
	if st := h.stateOf(t, id); st == nil || st.Phase != substrateStateCommitted {
		t.Errorf("state after Run = %+v, want committed", st)
	}
}

// The sweep is bounded and resumes where the previous one stopped.
func TestSubstrateReconcile_BoundedAndResumes(t *testing.T) {
	h := newReconcileHarness(t)
	total := maxReconcileObjectsPerSweep + 20
	for i := 0; i < total; i++ {
		h.putState(t, fmt.Sprintf("orphan-%03d", i), substrateStateCommitted, "uid")
	}

	first := h.reconcile(t, h.rt)
	if first.Examined != maxReconcileObjectsPerSweep {
		t.Fatalf("first sweep examined %d, want the cap %d", first.Examined, maxReconcileObjectsPerSweep)
	}
	if got := h.stateCount(t); got != 20 {
		t.Fatalf("after the first sweep %d objects remain, want 20", got)
	}
	second := h.reconcile(t, h.rt)
	if second.Examined != 20 {
		t.Errorf("second sweep examined %d, want the remaining 20", second.Examined)
	}
	if got := h.stateCount(t); got != 0 {
		t.Errorf("after the second sweep %d objects remain, want 0", got)
	}
}

// A store List failure fails the sweep without touching anything.
func TestSubstrateReconcile_ListFailureTouchesNothing(t *testing.T) {
	h := newReconcileHarness(t)
	h.putState(t, "c", substrateStateCommitted, "uid")
	failSecrets(h.cs, "list", "x")
	if _, err := h.rt.reconcileOnce(context.Background()); err == nil {
		t.Fatal("reconcileOnce() error = nil, want the list failure")
	}
	if h.calls("GetActor") != 0 {
		t.Error("the sweep looked up actors although the store could not be listed")
	}
}

// =====================================================================
// Reconciler loop and state_reconcile_interval.
// =====================================================================

// TestSubstrateReconcile_LoopTiming: the first sweep runs 1m after start,
// then every state_reconcile_interval, until the context is cancelled. The
// timer is a seam, so no wall-clock time passes.
func TestSubstrateReconcile_LoopTiming(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("loop")
	h.putState(t, "loop", substrateStateCommitted, "uid-loop") // actor absent: row 5

	type timerReq struct {
		d    time.Duration
		fire chan time.Time
	}
	reqs := make(chan timerReq)
	after := func(d time.Duration) <-chan time.Time {
		fire := make(chan time.Time, 1)
		reqs <- timerReq{d, fire}
		return fire
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.rt.runReconciler(ctx, 7*time.Minute, after)
	}()

	first := <-reqs
	if first.d != stateReconcileStartupDelay {
		t.Errorf("first wait = %s, want %s after startup", first.d, stateReconcileStartupDelay)
	}
	if h.stateOf(t, id) == nil {
		t.Fatal("a sweep ran before the startup delay elapsed")
	}
	first.fire <- time.Time{}
	second := <-reqs // requested only after the first sweep finished
	if second.d != 7*time.Minute {
		t.Errorf("second wait = %s, want the configured interval 7m", second.d)
	}
	if h.stateOf(t, id) != nil {
		t.Error("the first sweep did not reconcile the orphaned object")
	}
	cancel()
	<-done
}

// stopStateReconcilersForTest cancels every running background sweep.
func stopStateReconcilersForTest() {
	substrateReconcilersMu.Lock()
	defer substrateReconcilersMu.Unlock()
	for ns, cancel := range substrateReconcilers {
		cancel()
		delete(substrateReconcilers, ns)
	}
}

// One background sweep per state namespace per process, however many
// runtimes (profiles) share it, and none without a state store.
func TestSubstrateReconcile_OneSweeperPerStateNamespace(t *testing.T) {
	stopStateReconcilersForTest()
	t.Cleanup(stopStateReconcilersForTest)
	h := newReconcileHarness(t)
	count := func() int {
		substrateReconcilersMu.Lock()
		defer substrateReconcilersMu.Unlock()
		return len(substrateReconcilers)
	}

	(&SubstrateRuntime{}).startStateReconciler()
	if n := count(); n != 0 {
		t.Fatalf("%d sweepers after starting a runtime with no state store, want 0", n)
	}
	h.rt.startStateReconciler()
	h.rt.startStateReconciler()
	h.otherProcess().startStateReconciler()
	if n := count(); n != 1 {
		t.Fatalf("%d sweepers for one state namespace, want 1", n)
	}
	stopStateReconcilersForTest()
	if n := count(); n != 0 {
		t.Errorf("%d sweepers after stop, want 0", n)
	}
}

func TestStateReconcileInterval_DefaultMinimumValidation(t *testing.T) {
	if got := stateReconcileInterval(config.V1SubstrateConfig{}); got != 10*time.Minute {
		t.Errorf("default interval = %s, want 10m", got)
	}
	if got := stateReconcileInterval(config.V1SubstrateConfig{StateReconcileInterval: "3m"}); got != 3*time.Minute {
		t.Errorf("interval for \"3m\" = %s, want 3m", got)
	}
	for _, tc := range []struct {
		value   string
		wantErr string
	}{
		{"", ""},
		{"1m", ""},
		{"90s", ""},
		{"2h", ""},
		{"59s", "below the minimum of 1m0s"},
		{"0", "below the minimum"},
		{"-5m", "below the minimum"},
		{"ten minutes", "is not a Go duration"},
	} {
		err := validateStateReconcileInterval(&config.V1SubstrateConfig{StateReconcileInterval: tc.value})
		if tc.wantErr == "" && err != nil {
			t.Errorf("validate(%q) = %v, want nil", tc.value, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("validate(%q) = %v, want an error containing %q", tc.value, err, tc.wantErr)
		}
	}
}

// NewSubstrateRuntime refuses an invalid interval as a profile error (the
// broker's startup refusal), and accepts a valid one.
func TestNewSubstrateRuntime_StateReconcileIntervalValidated(t *testing.T) {
	resetSubstrateRuntimeRegistryForTest(t)
	restore := SetSubstrateRuntimeBuilderForTest(func(sc config.V1SubstrateConfig) (*SubstrateRuntime, error) {
		return &SubstrateRuntime{cfg: sc}, nil
	})
	t.Cleanup(restore)
	base := config.V1SubstrateConfig{APIEndpoint: "api:443", RouterEndpoint: "http://router", StateNamespace: testStateNamespace}

	bad := base
	bad.StateReconcileInterval = "30s"
	if _, err := NewSubstrateRuntime(&bad); !errors.Is(err, ErrSubstrateProfileInvalid) {
		t.Errorf("NewSubstrateRuntime(interval 30s) error = %v, want ErrSubstrateProfileInvalid", err)
	}
	good := base
	good.StateReconcileInterval = "5m"
	if _, err := NewSubstrateRuntime(&good); err != nil {
		t.Errorf("NewSubstrateRuntime(interval 5m) error = %v, want success", err)
	}
}

// =====================================================================
// Run stale takeover (design §3.4 step 3) and phase-1 review M1.
// =====================================================================

// M1: a committed object whose actor is gone no longer blocks the name: a
// Run of the same name succeeds right away, from this process and from a
// fresh one.
func TestSubstrateRun_M1_CommittedActorAbsent_TakenOverImmediately(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runner func(h *reconcileHarness) *SubstrateRuntime
	}{
		{"same process", func(h *reconcileHarness) *SubstrateRuntime { return h.rt }},
		{"new process", func(h *reconcileHarness) *SubstrateRuntime { return h.otherProcess() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newReconcileHarness(t)
			ctx := context.Background()
			cfg := stateTestRunConfig(stateTestProjectID, "m1")
			id, err := h.rt.Run(ctx, cfg)
			if err != nil {
				t.Fatalf("first Run() error = %v", err)
			}
			old := h.stateOf(t, id)
			h.removeActorOutOfBand(id)

			if _, err := tc.runner(h).Run(ctx, cfg); err != nil {
				t.Fatalf("Run() of a name held only by an orphaned committed object error = %v, want success right away", err)
			}
			st := h.stateOf(t, id)
			if st == nil || st.Phase != substrateStateCommitted || st.ControlToken == old.ControlToken {
				t.Errorf("state after the takeover Run = %+v, want a new committed object with a new token", st)
			}
			if !h.actorExists(id) {
				t.Error("no actor after the takeover Run")
			}
		})
	}
}

// M1, the sweep path: the reconciler removes the orphan, after which a Run
// of the name succeeds.
func TestSubstrateRun_M1_CommittedActorAbsent_ReusableAfterSweep(t *testing.T) {
	h := newReconcileHarness(t)
	ctx := context.Background()
	cfg := stateTestRunConfig(stateTestProjectID, "m1")
	id, err := h.rt.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	h.removeActorOutOfBand(id)

	stats := h.reconcile(t, h.otherProcess())
	if stats.Actions[reconcileCommittedNoActor] != 1 || h.stateOf(t, id) != nil {
		t.Fatalf("sweep actions = %v, want the orphaned committed object removed", stats.Actions)
	}
	if _, err := h.rt.Run(ctx, cfg); err != nil {
		t.Fatalf("Run() after the sweep error = %v, want success", err)
	}
	if st := h.stateOf(t, id); st == nil || st.Phase != substrateStateCommitted {
		t.Errorf("state after Run = %+v, want committed", st)
	}
}

// Negative control for M1: a FRESH pending object from another instance (a
// live Run there) makes Run fail and is NOT deleted.
func TestSubstrateRun_FreshPendingFromAnotherInstanceIsNotTakenOver(t *testing.T) {
	h := newReconcileHarness(t)
	ctx := context.Background()
	id := reconcileID("contended")
	other := h.putState(t, "contended", substrateStatePending, "")
	h.clk.Advance(h.rt.pendingStaleAfter() - time.Second) // old, but not stale

	_, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "contended"))
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("Run() over a fresh pending object error = %v, want 'already in use'", err)
	}
	st := h.stateOf(t, id)
	if st == nil || st.version != other.version || st.ControlToken != other.ControlToken {
		t.Errorf("the other instance's pending object = %+v, want it untouched", st)
	}
	if h.calls("CreateActor") != 0 {
		t.Error("CreateActor called although the id is claimed by a live Run")
	}
}

// A stale pending object with no actor is an orphan Run takes over.
func TestSubstrateRun_StalePendingNoActorTakenOver(t *testing.T) {
	h := newReconcileHarness(t)
	h.putState(t, "abandoned", substrateStatePending, "")
	h.ageBeyondStaleness()

	if _, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "abandoned")); err != nil {
		t.Fatalf("Run() over a stale pending object with no actor error = %v, want success", err)
	}
	if st := h.stateOf(t, reconcileID("abandoned")); st == nil || st.Phase != substrateStateCommitted {
		t.Errorf("state after Run = %+v, want committed", st)
	}
}

// Not orphans, so not taken over: a stale pending object whose actor
// exists (the reconciler's row 2, which deletes the actor too), a committed
// object whose actor exists with the matching UID (the criterion-5
// invariant), a deleting object, and any object whose actor cannot be
// looked up.
func TestSubstrateRun_NonOrphansAreNotTakenOver(t *testing.T) {
	type setup func(t *testing.T, h *reconcileHarness)
	for _, tc := range []struct {
		name    string
		setup   setup
		wantErr string
	}{
		{"stale pending with actor", func(t *testing.T, h *reconcileHarness) {
			h.putActor("held", "uid-held")
			h.putState(t, "held", substrateStatePending, "")
			h.ageBeyondStaleness()
		}, "already in use"},
		{"committed with matching actor", func(t *testing.T, h *reconcileHarness) {
			h.putActor("held", "uid-held")
			h.putState(t, "held", substrateStateCommitted, "uid-held")
			h.clk.Advance(1000 * time.Hour)
		}, "already in use"},
		{"deleting", func(t *testing.T, h *reconcileHarness) {
			h.putState(t, "held", substrateStateDeleting, "uid-held")
			h.clk.Advance(1000 * time.Hour)
		}, "being deleted; retry"},
		{"committed, actor lookup fails", func(t *testing.T, h *reconcileHarness) {
			h.putState(t, "held", substrateStateCommitted, "uid-held")
			h.fc.getActor = func(*ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
				return nil, status.Error(codes.Unavailable, "down")
			}
		}, "already in use"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newReconcileHarness(t)
			tc.setup(t, h)
			before := h.stateOf(t, reconcileID("held"))

			_, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "held"))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Run() error = %v, want %q", err, tc.wantErr)
			}
			after := h.stateOf(t, reconcileID("held"))
			if after == nil || after.version != before.version {
				t.Errorf("state object after the refused Run = %+v, want it untouched", after)
			}
			if h.calls("CreateActor") != 0 {
				t.Error("CreateActor called for a refused Run")
			}
		})
	}
}

// The takeover is a compare-and-swap: if the orphan changed between the
// read and the delete (another process acted on it), Run does not take it
// over.
func TestSubstrateRun_TakeoverLosesCASRace(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("cas")
	h.putState(t, "cas", substrateStateCommitted, "uid-gone")
	// Another process touches the object right after Run's GetActor.
	h.fc.getActor = func(req *ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
		st, err := h.store.Get(context.Background(), id)
		if err == nil {
			_ = h.store.Update(context.Background(), st)
		}
		return nil, status.Error(codes.NotFound, "actor not found")
	}

	_, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "cas"))
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("Run() error = %v, want 'already in use' after losing the takeover CAS", err)
	}
	if h.stateOf(t, id) == nil {
		t.Error("the object was deleted although it changed after Run read it")
	}
}

// =====================================================================
// Per-id mutex.
// =====================================================================

func TestSubstrateIDLocks(t *testing.T) {
	l := newSubstrateIDLocks()
	ctx := context.Background()

	unlockA, err := l.lock(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.tryLock("a"); ok {
		t.Fatal("tryLock took a held id")
	}
	unlockB, ok := l.tryLock("b")
	if !ok {
		t.Fatal("tryLock failed on a free id")
	}
	unlockB()

	// A waiter blocks until release (no sleeps: the waiter can only finish
	// after unlockA, and it reports through acquired).
	acquired := make(chan func())
	go func() {
		u, err := l.lock(ctx, "a")
		if err != nil {
			t.Error(err)
			close(acquired)
			return
		}
		acquired <- u
	}()
	select {
	case <-acquired:
		t.Fatal("waiter acquired a held id")
	default:
	}
	unlockA()
	unlockA() // idempotent
	u := <-acquired
	u()

	// A cancelled wait returns the context error.
	unlockA, _ = l.lock(ctx, "a")
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := l.lock(cctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("lock with a cancelled context = %v, want context.Canceled", err)
	}
	unlockA()

	l.mu.Lock()
	n := len(l.held) + len(l.waiting)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("%d ids still held after every release, want 0", n)
	}
}

// A same-process Delete issued while Run is in flight waits for it, then
// deletes the agent Run committed: neither half-runs.
func TestSubstrateDelete_WaitsForSameProcessRun(t *testing.T) {
	h := newReconcileHarness(t)
	id := reconcileID("serial")
	ctx := context.Background()
	deleteDone := make(chan error, 1)
	h.fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
		go func() { deleteDone <- h.rt.Delete(ctx, id) }()
		// Deterministic: wait until that Delete is parked on the id lock.
		waitForLockWaiter(t, h.rt.idLocks(), id)
		return &ateapipb.ResumeActorResponse{}, nil
	}

	if _, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "serial")); err != nil {
		t.Fatalf("Run() error = %v, want success (the same-process Delete waits)", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if h.stateOf(t, id) != nil || h.actorExists(id) {
		t.Error("the agent survived the Delete that waited for its Run")
	}
}

// waitForLockWaiter blocks until some goroutine is waiting for id. It
// polls the lock set's waiter count with a yield, never a sleep; the
// deadline only bounds a broken test.
func waitForLockWaiter(t *testing.T, l *substrateIDLocks, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for l.waiters(id) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no goroutine ever waited for %s", id)
		}
		goruntime.Gosched()
	}
}

// =====================================================================
// Fault injection: a store wrapper over the real store that "kills the
// process" (panics, so no cleanup runs) or fails at store step N.
// =====================================================================

// errProcessDied is the panic value faultStore uses to model the broker
// process dying at a store step.
var errProcessDied = errors.New("process died")

type faultMode int

const (
	dieBefore faultMode = iota // die before step N takes effect
	dieAfter                   // die right after step N took effect
	failOnce                   // step N returns an error; the process lives on
	failFrom                   // step N and every later store call fail (the store is unreachable)
)

func (m faultMode) String() string {
	return [...]string{"die-before", "die-after", "fail-once", "fail-from"}[m]
}

// faultStore wraps the real AgentStateStore and injects a fault at the
// Nth call (1-based) to Create/Get/Update/Delete. List is not counted.
type faultStore struct {
	AgentStateStore
	at   int
	mode faultMode

	mu    sync.Mutex
	calls int
	ops   []string
}

var errInjected = errors.New("injected store failure")

func (f *faultStore) step(op string, do func() error) error {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.ops = append(f.ops, op)
	f.mu.Unlock()
	switch {
	case n == f.at && f.mode == dieBefore:
		panic(errProcessDied)
	case n == f.at && f.mode == dieAfter:
		if err := do(); err != nil {
			return err
		}
		panic(errProcessDied)
	case n == f.at && f.mode == failOnce, n >= f.at && f.mode == failFrom:
		return errInjected
	}
	return do()
}

func (f *faultStore) Create(ctx context.Context, s *substrateAgentState) error {
	return f.step("Create", func() error { return f.AgentStateStore.Create(ctx, s) })
}

func (f *faultStore) Get(ctx context.Context, id string) (*substrateAgentState, error) {
	var out *substrateAgentState
	err := f.step("Get", func() error {
		var err error
		out, err = f.AgentStateStore.Get(ctx, id)
		return err
	})
	return out, err
}

func (f *faultStore) Update(ctx context.Context, s *substrateAgentState) error {
	return f.step("Update", func() error { return f.AgentStateStore.Update(ctx, s) })
}

func (f *faultStore) Delete(ctx context.Context, id, version string) error {
	return f.step("Delete", func() error { return f.AgentStateStore.Delete(ctx, id, version) })
}

// callSurvivingDeath runs fn and reports whether it "died" (panicked with
// errProcessDied); any other panic is re-raised.
func callSurvivingDeath(fn func() error) (err error, died bool) {
	defer func() {
		if p := recover(); p != nil {
			if p != errProcessDied { //nolint:errorlint // sentinel identity
				panic(p)
			}
			died = true
		}
	}()
	return fn(), false
}

// TestSubstrateFaultInjection_Run kills or fails Run at each of its three
// store steps (1 Create pending, 2 Update actor UID, 3 Update committed),
// then has a fresh process sweep after the staleness threshold. Whenever
// the caller saw a failure, the sweep converges to no state object and no
// actor; when the process died after the commit (the agent is live), the
// sweep leaves it alone and it stays manageable.
func TestSubstrateFaultInjection_Run(t *testing.T) {
	for step := 1; step <= 3; step++ {
		for _, mode := range []faultMode{dieBefore, dieAfter, failOnce, failFrom} {
			t.Run(fmt.Sprintf("step%d/%s", step, mode), func(t *testing.T) {
				h := newReconcileHarness(t)
				id := reconcileID("crashy")
				fs := &faultStore{AgentStateStore: h.rt.state, at: step, mode: mode}
				h.rt.state = fs

				err, died := callSurvivingDeath(func() error {
					_, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "crashy"))
					return err
				})
				callerSawFailure := died || err != nil
				agentLive := step == 3 && mode == dieAfter
				if !callerSawFailure {
					t.Fatalf("Run() succeeded with a fault at step %d (%s); ops %v", step, mode, fs.ops)
				}

				// A new broker process, later: empty cache, fresh locks.
				t.Cleanup(WipeSubstrateAgentStateForTest())
				h.ageBeyondStaleness()
				next := h.otherProcess()
				h.reconcile(t, next)

				if agentLive {
					st := h.stateOf(t, id)
					if st == nil || st.Phase != substrateStateCommitted || !h.actorExists(id) {
						t.Fatalf("committed agent with its actor after a post-commit death = state %+v actor %v, want it left alone", st, h.actorExists(id))
					}
					if err := next.Delete(context.Background(), id); err != nil {
						t.Fatalf("Delete() of the surviving agent error = %v", err)
					}
				}
				if n := h.stateCount(t); n != 0 {
					t.Errorf("%d orphaned state objects after the sweep, want 0 (ops %v)", n, fs.ops)
				}
				if n := h.actorCount(); n != 0 {
					t.Errorf("%d orphaned actors after the sweep, want 0 (ops %v)", n, fs.ops)
				}
				// The name is reusable.
				if _, err := next.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "crashy")); err != nil {
					t.Errorf("Run() of the name after convergence error = %v", err)
				}
			})
		}
	}
}

// TestSubstrateFaultInjection_RunDiesAtClusterSteps covers the "process
// dies here" points between store steps: right after CreateActor (actor
// exists, state pending with no UID) and right after ResumeActor (state
// pending with the UID).
func TestSubstrateFaultInjection_RunDiesAtClusterSteps(t *testing.T) {
	for _, point := range []string{"after CreateActor", "after ResumeActor"} {
		t.Run(point, func(t *testing.T) {
			h := newReconcileHarness(t)
			switch point {
			case "after CreateActor":
				defaultCreate := h.fc.createActor
				h.fc.createActor = func(req *ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
					_, _ = defaultCreate(req)
					panic(errProcessDied)
				}
			case "after ResumeActor":
				h.fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
					panic(errProcessDied)
				}
			}
			_, died := callSurvivingDeath(func() error {
				_, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "crashy"))
				return err
			})
			if !died {
				t.Fatal("Run() did not reach the crash point")
			}
			if h.stateCount(t) != 1 || h.actorCount() != 1 {
				t.Fatalf("before the sweep: %d state objects, %d actors; want the crash leftovers 1 and 1", h.stateCount(t), h.actorCount())
			}

			t.Cleanup(WipeSubstrateAgentStateForTest())
			// Not yet stale: a sweep must leave the (possibly live) Run alone.
			h.reconcile(t, h.otherProcess())
			if h.stateCount(t) != 1 || h.actorCount() != 1 {
				t.Fatal("a sweep before the staleness threshold reaped a pending Run")
			}
			h.ageBeyondStaleness()
			h.reconcile(t, h.otherProcess())
			if n, a := h.stateCount(t), h.actorCount(); n != 0 || a != 0 {
				t.Errorf("after the sweep: %d state objects, %d actors; want 0 and 0", n, a)
			}
		})
	}
}

// TestSubstrateFaultInjection_Delete kills or fails Delete at each of its
// three store steps (1 Get, 2 Update deleting, 3 Delete state). The sweep
// then converges: if the delete took effect at all (the object reached
// "deleting"), the agent is fully gone; if it never did, the agent is left
// intact (its caller saw a failure and retries) and a retried Delete
// completes.
func TestSubstrateFaultInjection_Delete(t *testing.T) {
	for step := 1; step <= 3; step++ {
		for _, mode := range []faultMode{dieBefore, dieAfter, failOnce, failFrom} {
			t.Run(fmt.Sprintf("step%d/%s", step, mode), func(t *testing.T) {
				h := newReconcileHarness(t)
				ctx := context.Background()
				id, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "doomed"))
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				fs := &faultStore{AgentStateStore: h.rt.state, at: step, mode: mode}
				h.rt.state = fs

				err, died := callSurvivingDeath(func() error { return h.rt.Delete(ctx, id) })
				if !died && err == nil {
					t.Fatalf("Delete() succeeded with a fault at step %d (%s); ops %v", step, mode, fs.ops)
				}

				t.Cleanup(WipeSubstrateAgentStateForTest())
				next := h.otherProcess()
				h.reconcile(t, next)

				reachedDeleting := step > 2 || (step == 2 && mode == dieAfter)
				if reachedDeleting {
					if n, a := h.stateCount(t), h.actorCount(); n != 0 || a != 0 {
						t.Errorf("after the sweep: %d state objects, %d actors; want 0 and 0 (ops %v)", n, a, fs.ops)
					}
					return
				}
				// The delete never took effect: the agent is intact and
				// committed, the sweep left it, and a retry completes.
				st := h.stateOf(t, id)
				if st == nil || st.Phase != substrateStateCommitted || !h.actorExists(id) {
					t.Fatalf("agent after a Delete that never took effect = state %+v actor %v, want it intact", st, h.actorExists(id))
				}
				if err := next.Delete(ctx, id); err != nil {
					t.Fatalf("retried Delete() error = %v", err)
				}
				if n, a := h.stateCount(t), h.actorCount(); n != 0 || a != 0 {
					t.Errorf("after the retried Delete: %d state objects, %d actors; want 0 and 0", n, a)
				}
			})
		}
	}
}

// =====================================================================
// Cross-process concurrency: two runtimes with separate lock sets over one
// store and one cluster, interleaved at every Run step.
// =====================================================================

// hookStore runs before(op, n) ahead of the nth call of each op on the
// wrapped store, letting a test interleave another process at an exact
// store step.
type hookStore struct {
	AgentStateStore
	mu     sync.Mutex
	counts map[string]int
	before func(op string, n int)
}

func (s *hookStore) hook(op string) {
	s.mu.Lock()
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	s.counts[op]++
	n := s.counts[op]
	s.mu.Unlock()
	if s.before != nil {
		s.before(op, n)
	}
}

func (s *hookStore) Update(ctx context.Context, st *substrateAgentState) error {
	s.hook("Update")
	return s.AgentStateStore.Update(ctx, st)
}

func (s *hookStore) Create(ctx context.Context, st *substrateAgentState) error {
	s.hook("Create")
	return s.AgentStateStore.Create(ctx, st)
}

// assertStoreAndClusterAgree: either the agent fully exists (a committed
// state object whose actor exists with the recorded UID) or nothing does.
func assertStoreAndClusterAgree(t *testing.T, h *reconcileHarness, id string) (exists bool) {
	t.Helper()
	st := h.stateOf(t, id)
	actor := h.actorExists(id)
	switch {
	case st == nil && !actor:
		return false
	case st != nil && st.Phase == substrateStateCommitted && actor:
		return true
	}
	t.Fatalf("store and cluster disagree: state %+v, actor exists %v", st, actor)
	return false
}

// TestSubstrateCrossProcess_DeleteInterleavedIntoRun: process B deletes the
// id at each step of process A's Run. A always loses with the documented
// "deleted while starting" error, B's Delete succeeds, and nothing leaks.
// After the commit, the order is simply Run then Delete.
func TestSubstrateCrossProcess_DeleteInterleavedIntoRun(t *testing.T) {
	type point struct {
		name   string
		inject func(h *reconcileHarness, hs *hookStore, del func())
	}
	for _, p := range []point{
		{"before CreateActor", func(h *reconcileHarness, _ *hookStore, del func()) {
			defaultCreate := h.fc.createActor
			h.fc.createActor = func(req *ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
				del()
				return defaultCreate(req)
			}
		}},
		{"after CreateActor, before UID record", func(_ *reconcileHarness, hs *hookStore, del func()) {
			hs.before = func(op string, n int) {
				if op == "Update" && n == 1 {
					del()
				}
			}
		}},
		{"after UID record (resume)", func(h *reconcileHarness, _ *hookStore, del func()) {
			h.fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
				del()
				return &ateapipb.ResumeActorResponse{}, nil
			}
		}},
		{"after bootstrap, before commit", func(_ *reconcileHarness, hs *hookStore, del func()) {
			hs.before = func(op string, n int) {
				if op == "Update" && n == 2 {
					del()
				}
			}
		}},
	} {
		t.Run(p.name, func(t *testing.T) {
			h := newReconcileHarness(t)
			ctx := context.Background()
			id := reconcileID("contested")
			procB := h.otherProcess()
			hs := &hookStore{AgentStateStore: h.rt.state}
			h.rt.state = hs
			var delErr error
			deleted := false
			p.inject(h, hs, func() {
				if !deleted {
					deleted = true
					delErr = procB.Delete(ctx, id)
				}
			})

			_, runErr := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "contested"))
			if !deleted {
				t.Fatal("the interleaving point was never reached")
			}
			if delErr != nil {
				t.Errorf("process B Delete() error = %v, want success", delErr)
			}
			// At the resume point the actor is already gone, so Run fails
			// in its readiness wait before reaching the commit CAS; at
			// every other point the CAS reports the documented error.
			wantMsg := "deleted while starting"
			if p.name == "after UID record (resume)" {
				wantMsg = ""
			}
			if runErr == nil || !strings.Contains(runErr.Error(), wantMsg) {
				t.Errorf("process A Run() error = %v, want a failure containing %q", runErr, wantMsg)
			}
			if assertStoreAndClusterAgree(t, h, id) {
				t.Error("the agent exists although its Delete won")
			}
			substrateAgentStateMu.Lock()
			_, cached := substrateControlTokens[id]
			substrateAgentStateMu.Unlock()
			if cached {
				t.Error("the losing Run cached a control token")
			}
		})
	}
}

// TestSubstrateCrossProcess_RunInterleavedIntoDelete: process B runs the
// id while process A's Delete is between marking it deleting and removing
// it. B is refused with the documented retry error and creates nothing;
// once A's Delete finishes, B's retry succeeds.
func TestSubstrateCrossProcess_RunInterleavedIntoDelete(t *testing.T) {
	h := newReconcileHarness(t)
	ctx := context.Background()
	cfg := stateTestRunConfig(stateTestProjectID, "contested")
	id, err := h.rt.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	procB := h.otherProcess()
	served := h.fc.deleteActor
	var bErr error
	h.fc.deleteActor = func(req *ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
		_, bErr = procB.Run(ctx, cfg)
		return served(req)
	}
	creates := h.calls("CreateActor")

	if err := h.rt.Delete(ctx, id); err != nil {
		t.Fatalf("process A Delete() error = %v", err)
	}
	if bErr == nil || !strings.Contains(bErr.Error(), "being deleted; retry") {
		t.Errorf("process B Run() during the Delete error = %v, want 'being deleted; retry'", bErr)
	}
	if h.calls("CreateActor") != creates {
		t.Error("the refused Run created an actor")
	}
	if assertStoreAndClusterAgree(t, h, id) {
		t.Error("the agent exists after its Delete")
	}
	h.fc.deleteActor = served
	if _, err := procB.Run(ctx, cfg); err != nil {
		t.Fatalf("process B retried Run() error = %v", err)
	}
	if !assertStoreAndClusterAgree(t, h, id) {
		t.Error("the retried Run left no agent")
	}
}

// TestSubstrateCrossProcess_TwoRunsSameID: process B runs the id while
// process A's Run is in flight. B sees A's fresh pending claim, is refused
// with "already in use", and never takes it over; A succeeds.
func TestSubstrateCrossProcess_TwoRunsSameID(t *testing.T) {
	h := newReconcileHarness(t)
	ctx := context.Background()
	cfg := stateTestRunConfig(stateTestProjectID, "contested")
	procB := h.otherProcess()
	var bErr error
	h.fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
		_, bErr = procB.Run(ctx, cfg)
		return &ateapipb.ResumeActorResponse{}, nil
	}

	id, err := h.rt.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("process A Run() error = %v, want success", err)
	}
	if bErr == nil || !strings.Contains(bErr.Error(), "already in use") {
		t.Errorf("process B Run() error = %v, want 'already in use'", bErr)
	}
	if !assertStoreAndClusterAgree(t, h, id) {
		t.Error("process A's agent is missing")
	}
}

// TestSubstrateCrossProcess_ConcurrentRunAndDelete runs process A's Run and
// process B's Delete of the same id truly concurrently, many times (the
// -race detector watches the lock-free paths). Whatever the interleaving,
// exactly one outcome holds and the store and cluster agree; a sweep then
// changes nothing that is live.
func TestSubstrateCrossProcess_ConcurrentRunAndDelete(t *testing.T) {
	const rounds = 40
	for i := 0; i < rounds; i++ {
		h := newReconcileHarness(t)
		ctx := context.Background()
		name := fmt.Sprintf("race-%02d", i)
		id := reconcileID(name)
		procB := h.otherProcess()

		start := make(chan struct{})
		var wg sync.WaitGroup
		var runErr, delErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, runErr = h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, name))
		}()
		go func() {
			defer wg.Done()
			<-start
			delErr = procB.Delete(ctx, id)
		}()
		close(start)
		wg.Wait()

		if delErr != nil {
			t.Errorf("round %d: Delete() error = %v", i, delErr)
		}
		exists := assertStoreAndClusterAgree(t, h, id)
		if exists != (runErr == nil) {
			t.Errorf("round %d: Run() error = %v but agent exists = %v", i, runErr, exists)
		}
		h.ageBeyondStaleness()
		h.reconcile(t, procB)
		if assertStoreAndClusterAgree(t, h, id) != exists {
			t.Errorf("round %d: the sweep changed a converged outcome", i)
		}
	}
}

// =====================================================================
// No credential leak from the reconciler or the takeover (§3.7 / §8.4).
// =====================================================================

// TestSubstrateReconcile_NoCredentialLeak drives every reconciler row and
// Run's takeover through success and failure paths, with control-plane and
// store errors whose messages embed the control token and every exec
// secret, and asserts none of them appears in a returned error or a log
// line.
func TestSubstrateReconcile_NoCredentialLeak(t *testing.T) {
	logs := captureRuntimeLog(t)
	h := newReconcileHarness(t)
	ctx := context.Background()
	var surfaced []string
	note := func(err error) {
		if err != nil {
			surfaced = append(surfaced, err.Error())
		}
	}

	// A real agent, so the real token and exec secrets exist.
	cfg := stateTestRunConfig(stateTestProjectID, "secretive")
	id, err := h.rt.Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	token := h.stateOf(t, id).ControlToken
	secrets := []string{token, stateTestSecretValue, stateTestHarnessSecret}
	leaky := fmt.Sprintf("backend failure near %s %s %s", token, stateTestSecretValue, stateTestHarnessSecret)

	// Success paths: takeover (committed, actor gone) and every row.
	h.removeActorOutOfBand(id)
	_, err = h.rt.Run(ctx, cfg)
	note(err)
	h.putState(t, "r1", substrateStatePending, "")
	h.putActor("r2", "u2")
	h.putState(t, "r2", substrateStatePending, "u2")
	h.putActor("r3", "u3")
	h.putState(t, "r3", substrateStateDeleting, "u3")
	h.putState(t, "r4", substrateStateDeleting, "u4")
	h.putState(t, "r5", substrateStateCommitted, "u5")
	h.putActor("r6", "u6")
	h.ageBeyondStaleness()
	_, err = h.rt.reconcileOnce(ctx)
	note(err)

	// Failure paths: control-plane errors embedding the secrets.
	h.putActor("f1", "uf1")
	h.putState(t, "f1", substrateStateDeleting, "uf1")
	h.putState(t, "f2", substrateStateCommitted, "uf2")
	h.fc.deleteActor = func(*ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
		return nil, status.Error(codes.Internal, leaky)
	}
	served := h.fc.getActor
	h.fc.getActor = func(req *ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
		if req.GetActor().GetName() == "f2" {
			return nil, status.Error(codes.Internal, leaky)
		}
		return served(req)
	}
	_, err = h.rt.reconcileOnce(ctx)
	note(err)
	// Takeover with a failing actor lookup.
	h.putState(t, "f2-run", substrateStateCommitted, "x")
	h.fc.getActor = func(*ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
		return nil, status.Error(codes.Internal, leaky)
	}
	_, err = h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "f2-run"))
	note(err)
	h.fc.getActor = served

	// Store errors embedding the secrets, at each verb the sweep uses.
	for _, verb := range []string{"list", "get", "update", "delete"} {
		h.putState(t, "s-"+verb, substrateStatePending, "")
		h.ageBeyondStaleness()
		h.cs.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New(leaky))
		})
		_, err := h.rt.reconcileOnce(ctx)
		note(err)
		h.cs.ReactionChain = h.cs.ReactionChain[1:]
	}

	all := strings.Join(surfaced, "\n") + "\n" + logs.String()
	if !strings.Contains(logs.String(), "reconciler") {
		t.Fatal("test setup: no reconciler log lines were captured")
	}
	for _, s := range secrets {
		if s == "" {
			t.Fatal("test setup: empty secret value")
		}
		if strings.Contains(all, s) {
			t.Errorf("credential material leaked into an error or log line:\n%s", all)
		}
	}
}
