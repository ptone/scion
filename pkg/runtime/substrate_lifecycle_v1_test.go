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

	if err := rt.Delete(context.Background(), RunRef{ID: "scion-proj/agent-a"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.actors) != 1 || c.actors[0].GetOptions().GetUid() != fakeActorUID {
		t.Errorf("DeleteActor requests = %v, want one with Options.Uid %q", c.actors, fakeActorUID)
	}
	if len(c.egress) != 1 || c.egress[0].GetName() != "default" {
		t.Errorf("DeleteActorEgressPolicy requests = %v, want one with Name \"default\"", c.egress)
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
