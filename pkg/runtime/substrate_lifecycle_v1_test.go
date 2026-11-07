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
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
)

// deleteCapture records DeleteActor and DeleteActorEgressPolicy requests.
type deleteCapture struct {
	mu     sync.Mutex
	actors []*ateapi.DeleteActorRequest
	egress []*ateapi.DeleteActorEgressPolicyRequest
}

func (c *deleteCapture) install(fc *fakeControlClient, deleteErr error) {
	fc.deleteActor = func(req *ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.actors = append(c.actors, proto.Clone(req).(*ateapi.DeleteActorRequest))
		if deleteErr != nil {
			return nil, deleteErr
		}
		return &ateapi.Actor{}, nil
	}
	fc.deleteActorEgressPolicy = func(req *ateapi.DeleteActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.egress = append(c.egress, proto.Clone(req).(*ateapi.DeleteActorEgressPolicyRequest))
		return &ateapi.EgressPolicy{}, nil
	}
}

// Delete sends the uid the pre-delete GetActor read as a DeleteOptions
// precondition, and names the egress policy "default".
func TestSubstrateDelete_SendsUIDPreconditionAndPolicyName(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	var c deleteCapture
	c.install(fc, nil)
	fc.getActorEgressPolicy = func(*ateapi.GetActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
		return &ateapi.EgressPolicy{Metadata: &ateapi.ResourceMetadata{Name: "default", Uid: fakePolicyUID}}, nil
	}

	if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.actors) != 1 || c.actors[0].GetOptions().GetUid() != fakeActorUID {
		t.Errorf("DeleteActor requests = %v, want one with Options.Uid %q", c.actors, fakeActorUID)
	}
	if len(c.egress) != 1 || c.egress[0].GetName() != "default" || c.egress[0].GetOptions().GetUid() != fakePolicyUID {
		t.Errorf("DeleteActorEgressPolicy requests = %v, want one with Name \"default\" and Options.Uid %q", c.egress, fakePolicyUID)
	}
}

// When GetActor fails (the record-less path), the uid is unknown and the
// delete carries no options.
func TestSubstrateDelete_UnknownUIDSendsNoOptions(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	fc.getActor = func(*ateapi.GetActorRequest) (*ateapi.Actor, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}
	var c deleteCapture
	c.install(fc, nil)

	if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.actors) != 1 || c.actors[0].GetOptions() != nil {
		t.Errorf("DeleteActor requests = %v, want one with no Options", c.actors)
	}
}

// A FailedPrecondition on a delete that carried a uid is the typed
// incarnation-changed error, and the in-memory state is left alone.
func TestSubstrateDelete_UIDPreconditionFailureIsTypedAndKeepsState(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	id, err := rt.Run(context.Background(), testSubstrateRunConfig())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var c deleteCapture
	c.install(fc, status.Error(codes.FailedPrecondition, "uid precondition failed"))

	err = rt.Delete(context.Background(), RunRef{ID: id})
	if !errors.Is(err, ErrSubstrateIncarnationChanged) {
		t.Fatalf("Delete() error = %v, want errors.Is(err, ErrSubstrateIncarnationChanged)", err)
	}
	substrateAgentStateMu.Lock()
	_, hasToken := substrateControlTokens[id]
	_, hasRecord := substrateAgentRecords[fakeActorUID]
	substrateAgentStateMu.Unlock()
	if !hasToken || !hasRecord {
		t.Errorf("after an incarnation-changed refusal: token present %v, record present %v; want both kept", hasToken, hasRecord)
	}
}

// Without a uid, a FailedPrecondition is not the incarnation error, and
// Aborted is never mapped to it.
func TestSubstrateDelete_NoUIDOrAbortedNotIncarnationChanged(t *testing.T) {
	t.Run("no uid sent", func(t *testing.T) {
		rec := &callRecorder{}
		rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
		defer closeServer()
		fc.getActor = func(*ateapi.GetActorRequest) (*ateapi.Actor, error) {
			return nil, status.Error(codes.Unavailable, "transient")
		}
		var c deleteCapture
		c.install(fc, status.Error(codes.FailedPrecondition, "something else"))
		err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"})
		if err == nil || errors.Is(err, ErrSubstrateIncarnationChanged) {
			t.Errorf("Delete() error = %v, want a plain error, not ErrSubstrateIncarnationChanged", err)
		}
	})
	t.Run("aborted with uid", func(t *testing.T) {
		rec := &callRecorder{}
		rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
		defer closeServer()
		var c deleteCapture
		c.install(fc, status.Error(codes.Aborted, "concurrent write"))
		err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"})
		if err == nil || errors.Is(err, ErrSubstrateIncarnationChanged) {
			t.Errorf("Delete() error = %v, want a plain error, not ErrSubstrateIncarnationChanged", err)
		}
	})
}

// Run's failure cleanup deletes only the actor this Run created: it sends
// the uid CreateActor returned, and names the egress policy "default".
func TestSubstrateRun_CleanupSendsCreatedUID(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	var c deleteCapture
	c.install(fc, nil)
	fc.resumeActor = func(*ateapi.ResumeActorRequest) (*ateapi.ResumeActorResponse, error) {
		return nil, status.Error(codes.Internal, "resume failed")
	}

	if _, err := rt.Run(context.Background(), testSubstrateRunConfig()); err == nil {
		t.Fatal("Run() = nil error, want the resume failure")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.actors) != 1 || c.actors[0].GetOptions().GetUid() != fakeActorUID {
		t.Errorf("cleanup DeleteActor requests = %v, want one with Options.Uid %q", c.actors, fakeActorUID)
	}
	if len(c.egress) != 1 || c.egress[0].GetName() != "default" {
		t.Errorf("cleanup DeleteActorEgressPolicy requests = %v, want one with Name \"default\"", c.egress)
	}
}

// substrateDeleteActorRequest omits options for an empty uid.
func TestSubstrateDeleteActorRequest(t *testing.T) {
	if req := substrateDeleteActorRequest("a", "n", ""); req.GetOptions() != nil {
		t.Errorf("empty uid: Options = %v, want nil", req.GetOptions())
	}
	if req := substrateDeleteActorRequest("a", "n", "u-1"); req.GetOptions().GetUid() != "u-1" || req.GetActor().GetAtespace() != "a" || req.GetActor().GetName() != "n" {
		t.Errorf("request = %v, want actor a/n with Options.Uid u-1", req)
	}
}

// List keeps the empty atespace (all atespaces) and pages with ListOptions;
// RecordlessActors lists its one atespace the same way.
func TestSubstrateList_UsesListOptions(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	var mu sync.Mutex
	var reqs []*ateapi.ListActorsRequest
	fc.listActors = func(req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, proto.Clone(req).(*ateapi.ListActorsRequest))
		if req.GetOptions().GetPageToken() == "" {
			return &ateapi.ListActorsResponse{NextPageToken: "p2"}, nil
		}
		return &ateapi.ListActorsResponse{}, nil
	}

	if _, err := rt.List(context.Background(), nil); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	mu.Lock()
	if len(reqs) != 2 {
		t.Fatalf("List() made %d ListActors calls, want 2 (one per page)", len(reqs))
	}
	for i, r := range reqs {
		if r.GetAtespace() != "" || r.GetOptions().GetPageSize() != substrateListPageSize {
			t.Errorf("List() request[%d] = %v, want empty atespace and page size %d", i, r, substrateListPageSize)
		}
	}
	if reqs[1].GetOptions().GetPageToken() != "p2" {
		t.Errorf("second page token = %q, want p2", reqs[1].GetOptions().GetPageToken())
	}
	reqs = nil
	mu.Unlock()

	atespace, _, err := rt.RecordlessActors(context.Background(), "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatalf("RecordlessActors() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) == 0 || reqs[0].GetAtespace() != atespace || reqs[0].GetOptions().GetPageSize() != substrateListPageSize {
		t.Errorf("RecordlessActors() requests = %v, want atespace %q with page size %d", reqs, atespace, substrateListPageSize)
	}
}

// The phase map uses identifiers only; every v1 state maps to a phase.
func TestSubstratePhase_V1States(t *testing.T) {
	for state, want := range map[ateapi.ActorState]string{
		ateapi.ActorState_ACTOR_STATE_RESUMING:    "provisioning",
		ateapi.ActorState_ACTOR_STATE_RUNNING:     "running",
		ateapi.ActorState_ACTOR_STATE_SUSPENDING:  "stopping",
		ateapi.ActorState_ACTOR_STATE_SUSPENDED:   "stopped",
		ateapi.ActorState_ACTOR_STATE_CRASHED:     "error",
		ateapi.ActorState_ACTOR_STATE_DELETING:    "stopping",
		ateapi.ActorState_ACTOR_STATE_REVERTING:   "provisioning",
		ateapi.ActorState_ACTOR_STATE_UNSPECIFIED: "unknown",
	} {
		if got := substratePhase(state); got != want {
			t.Errorf("substratePhase(%v) = %q, want %q", state, got, want)
		}
	}
}

// policyServer is a fake for the egress policy RPCs that keeps one current
// policy uid (empty = no policy) and refuses a uid-conditioned delete whose
// uid differs, like the v1 server's DeleteOptions precondition.
type policyServer struct {
	mu        sync.Mutex
	current   string // the policy uid the server holds now
	readUID   string // the uid GetActorEgressPolicy reports
	deletes   []*ateapi.DeleteActorEgressPolicyRequest
	deleteErr error // forced error for the policy delete, when set
}

func (p *policyServer) install(fc *fakeControlClient) {
	fc.getActorEgressPolicy = func(*ateapi.GetActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.readUID == "" {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return &ateapi.EgressPolicy{Metadata: &ateapi.ResourceMetadata{Name: "default", Uid: p.readUID}}, nil
	}
	fc.deleteActorEgressPolicy = func(req *ateapi.DeleteActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.deletes = append(p.deletes, proto.Clone(req).(*ateapi.DeleteActorEgressPolicyRequest))
		if p.deleteErr != nil {
			return nil, p.deleteErr
		}
		if p.current == "" {
			return nil, status.Error(codes.NotFound, "not found")
		}
		if u := req.GetOptions().GetUid(); u != "" && u != p.current {
			return nil, status.Error(codes.FailedPrecondition, "uid precondition failed")
		}
		p.current = ""
		return &ateapi.EgressPolicy{}, nil
	}
}

// indexOf returns the position of name in calls, or -1.
func indexOf(calls []string, name string) int {
	for i, c := range calls {
		if c == name {
			return i
		}
	}
	return -1
}

// Design v0.5.5 §3.5 lifecycle invariant, test 1: a later
// incarnation's policy survives a delete aimed at the earlier incarnation.
func TestSubstrateDelete_LaterIncarnationPolicySurvives(t *testing.T) {
	t.Run("actor swapped: actor refused, policy never deleted", func(t *testing.T) {
		rec := &callRecorder{}
		rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
		defer closeServer()
		// readUID pol-old with current pol-new models the policy being
		// replaced by a later incarnation between the read and the delete.
		ps := &policyServer{current: "pol-new", readUID: "pol-old"}
		ps.install(fc)
		fc.deleteActor = func(*ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
			return nil, status.Error(codes.FailedPrecondition, "uid precondition failed")
		}
		err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"})
		if !errors.Is(err, ErrSubstrateIncarnationChanged) {
			t.Fatalf("Delete() error = %v, want ErrSubstrateIncarnationChanged", err)
		}
		if len(ps.deletes) != 0 || ps.current != "pol-new" {
			t.Errorf("policy deletes = %v, current = %q; want no policy delete and pol-new intact", ps.deletes, ps.current)
		}
	})
	t.Run("policy swapped after the actor delete: refused, not deleted", func(t *testing.T) {
		rec := &callRecorder{}
		rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
		defer closeServer()
		ps := &policyServer{current: "pol-new", readUID: "pol-old"}
		ps.install(fc)
		if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err != nil {
			t.Fatalf("Delete() error = %v, want nil (this Run's actor is gone; the later policy is not its to remove)", err)
		}
		if len(ps.deletes) != 1 || ps.deletes[0].GetOptions().GetUid() != "pol-old" || ps.current != "pol-new" {
			t.Errorf("policy deletes = %v, current = %q; want one delete carrying pol-old, refused, pol-new intact", ps.deletes, ps.current)
		}
	})
}

// Test 2: DeleteActor failing (or refused) leaves the policy in place:
// there is no policy delete call.
func TestSubstrateDelete_ActorDeleteFailureKeepsPolicy(t *testing.T) {
	for name, aerr := range map[string]error{
		"transient": status.Error(codes.Unavailable, "unavailable"),
		"refused":   status.Error(codes.FailedPrecondition, "uid precondition failed"),
	} {
		t.Run(name, func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			ps := &policyServer{current: "pol-1", readUID: "pol-1"}
			ps.install(fc)
			fc.deleteActor = func(*ateapi.DeleteActorRequest) (*ateapi.Actor, error) { return nil, aerr }
			if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err == nil {
				t.Fatal("Delete() error = nil, want the actor delete error")
			}
			if indexOf(rec.list(), "DeleteActorEgressPolicy") >= 0 || ps.current != "pol-1" {
				t.Errorf("calls = %v, policy = %q; want no policy delete and the policy in place", rec.list(), ps.current)
			}
		})
	}
}

// Test 3: the normal path deletes the actor, then the policy, each with its
// own uid.
func TestSubstrateDelete_ActorThenPolicyOrder(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	ps := &policyServer{current: "pol-1", readUID: "pol-1"}
	ps.install(fc)
	var actorReq *ateapi.DeleteActorRequest
	fc.deleteActor = func(req *ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
		actorReq = proto.Clone(req).(*ateapi.DeleteActorRequest)
		return &ateapi.Actor{}, nil
	}
	if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	calls := rec.list()
	a, p := indexOf(calls, "DeleteActor"), indexOf(calls, "DeleteActorEgressPolicy")
	if a < 0 || p < 0 || a > p {
		t.Errorf("calls = %v, want DeleteActor before DeleteActorEgressPolicy", calls)
	}
	if actorReq.GetOptions().GetUid() != fakeActorUID || len(ps.deletes) != 1 || ps.deletes[0].GetOptions().GetUid() != "pol-1" || ps.current != "" {
		t.Errorf("actor delete %v, policy deletes %v (current %q); want each carrying its uid and the policy deleted", actorReq, ps.deletes, ps.current)
	}
}

// Test 4: the actor delete succeeds and the policy delete fails: Delete
// returns an error, no runnable actor remains, and the orphan policy is
// reported without echoing any value.
func TestSubstrateDelete_PolicyDeleteFailsAfterActorDeleted(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	ps := &policyServer{current: "pol-1", readUID: "pol-1", deleteErr: status.Error(codes.Internal, "tok-internal pol-1 detail")}
	ps.install(fc)
	actorDeleted := false
	fc.deleteActor = func(*ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
		actorDeleted = true
		return &ateapi.Actor{}, nil
	}
	err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"})
	if err == nil {
		t.Fatal("Delete() error = nil, want the orphan-policy error")
	}
	if !actorDeleted {
		t.Error("the actor was not deleted")
	}
	if !strings.Contains(err.Error(), "orphan policy") {
		t.Errorf("error = %q, want it to report the orphan policy", err)
	}
	for _, leak := range []string{"pol-1", "tok-internal"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error = %q echoes %q", err, leak)
		}
	}
	// NotFound on the policy delete counts as already deleted.
	ps2 := &policyServer{current: "", readUID: "pol-1"}
	ps2.install(fc)
	if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err != nil {
		t.Errorf("Delete() with the policy gone by the delete: error = %v, want nil", err)
	}
}

// Test 5: a policy read that returns NotFound means no policy: Delete
// deletes the actor and makes no policy delete.
func TestSubstrateDelete_NoPolicyFoundNoPolicyDelete(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	ps := &policyServer{current: "pol-late", readUID: ""}
	ps.install(fc)
	if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	calls := rec.list()
	if indexOf(calls, "DeleteActor") < 0 {
		t.Errorf("calls = %v, want DeleteActor", calls)
	}
	if indexOf(calls, "DeleteActorEgressPolicy") >= 0 || ps.current != "pol-late" {
		t.Errorf("calls = %v, policy = %q; want no DeleteActorEgressPolicy and pol-late intact", calls, ps.current)
	}
}

// Run's failure cleanup follows the same order: DeleteActor (with the
// CreateActor uid) first, then the policy delete with the uid
// CreateActorEgressPolicy returned; a failed actor delete keeps the policy.
func TestSubstrateRun_CleanupActorThenPolicy(t *testing.T) {
	t.Run("order and uids", func(t *testing.T) {
		rec := &callRecorder{}
		rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
		defer closeServer()
		ps := &policyServer{current: "pol-run"}
		ps.install(fc)
		fc.createActorEgressPolicy = func(*ateapi.CreateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
			return &ateapi.EgressPolicy{Metadata: &ateapi.ResourceMetadata{Name: "default", Uid: "pol-run"}}, nil
		}
		fc.resumeActor = func(*ateapi.ResumeActorRequest) (*ateapi.ResumeActorResponse, error) {
			return nil, status.Error(codes.Internal, "resume failed")
		}
		if _, err := rt.Run(context.Background(), testSubstrateRunConfig()); err == nil {
			t.Fatal("Run() = nil error, want the resume failure")
		}
		calls := rec.list()
		a, p := indexOf(calls, "DeleteActor"), indexOf(calls, "DeleteActorEgressPolicy")
		if a < 0 || p < 0 || a > p {
			t.Errorf("calls = %v, want cleanup DeleteActor before DeleteActorEgressPolicy", calls)
		}
		if len(ps.deletes) != 1 || ps.deletes[0].GetOptions().GetUid() != "pol-run" {
			t.Errorf("cleanup policy deletes = %v, want one carrying the created uid pol-run", ps.deletes)
		}
	})
	t.Run("failed actor delete keeps the policy", func(t *testing.T) {
		rec := &callRecorder{}
		rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
		defer closeServer()
		fc.resumeActor = func(*ateapi.ResumeActorRequest) (*ateapi.ResumeActorResponse, error) {
			return nil, status.Error(codes.Internal, "resume failed")
		}
		fc.deleteActor = func(*ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
			return nil, status.Error(codes.Unavailable, "unavailable")
		}
		if _, err := rt.Run(context.Background(), testSubstrateRunConfig()); err == nil {
			t.Fatal("Run() = nil error, want the resume failure")
		}
		if indexOf(rec.list(), "DeleteActorEgressPolicy") >= 0 {
			t.Errorf("calls = %v, want no policy delete after a failed actor delete", rec.list())
		}
	})
}

// Test 6: a policy read that fails with anything but NotFound returns an
// error before any delete: no DeleteActor, no policy delete, and the error
// carries the status code only.
func TestSubstrateDelete_PolicyReadErrorDeletesNothing(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	fc.getActorEgressPolicy = func(*ateapi.GetActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
		return nil, status.Error(codes.Unavailable, "tok-unavailable pol-1 detail")
	}
	err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"})
	if err == nil {
		t.Fatal("Delete() error = nil, want the policy read error")
	}
	calls := rec.list()
	if indexOf(calls, "DeleteActor") >= 0 || indexOf(calls, "DeleteActorEgressPolicy") >= 0 {
		t.Errorf("calls = %v, want no DeleteActor and no DeleteActorEgressPolicy", calls)
	}
	if !strings.Contains(err.Error(), codes.Unavailable.String()) {
		t.Errorf("error = %q, want the status code", err)
	}
	for _, leak := range []string{"pol-1", "tok-unavailable"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error = %q echoes %q", err, leak)
		}
	}
}

// Test 7: when the policy create (Step 5) fails, its uid is unknown and
// Run's cleanup deletes the actor but issues no policy delete.
func TestSubstrateRun_CleanupNoPolicyDeleteWithoutUID(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	fc.createActorEgressPolicy = func(*ateapi.CreateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
		return nil, status.Error(codes.Internal, "egress create failed")
	}
	if _, err := rt.Run(context.Background(), testSubstrateRunConfig()); err == nil {
		t.Fatal("Run() = nil error, want the policy create failure")
	}
	calls := rec.list()
	if indexOf(calls, "DeleteActor") < 0 {
		t.Errorf("calls = %v, want the cleanup DeleteActor", calls)
	}
	if indexOf(calls, "DeleteActorEgressPolicy") >= 0 {
		t.Errorf("calls = %v, want no DeleteActorEgressPolicy without a policy uid", calls)
	}
	if indexOf(calls, "ResumeActor") >= 0 {
		t.Errorf("calls = %v, want no ResumeActor after a failed policy create", calls)
	}
}
