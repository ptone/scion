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

package runtimebroker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// Run fencing of a flat instance's delete and cleanup paths
// (ptone/scion#3274): no path fenced to a run acts on a newer run of the
// same agent, its record, or the agent's slug.

const (
	fenceOldRun = "run-cid-a1" // A's worker's first run (its object cid-a1)
	fenceNewRun = "run-new"
	fenceNewUID = "uid-new"
)

// newRunFenceFixture is the partition fixture with a newer live run of A's
// worker that recorded object uid-new. With oldGone, the old run's
// container is gone from the daemon (its record still lists cid-a1).
func newRunFenceFixture(t *testing.T, newRunState string, oldGone bool) *partitionFixture {
	t.Helper()
	f := newPartitionFixture(t)
	st := f.a.srv.ownership
	if err := st.BeginRun("proj-1", "agent-a1", "worker", fenceNewRun); err != nil {
		t.Fatal(err)
	}
	if err := st.AddResource("proj-1", "agent-a1", fenceNewRun, api.ResourceHandle{Kind: api.ResourceKindContainer, Name: "worker", UID: fenceNewUID}); err != nil {
		t.Fatal(err)
	}
	if newRunState != OwnershipStateProvisioning {
		if err := st.SetRunState("proj-1", "agent-a1", fenceNewRun, newRunState); err != nil {
			t.Fatal(err)
		}
	}
	if oldGone {
		f.d.mu.Lock()
		kept := f.d.objects[:0]
		for _, o := range f.d.objects {
			if o.ContainerID != "cid-a1" {
				kept = append(kept, o)
			}
		}
		f.d.objects = kept
		f.d.mu.Unlock()
	}
	return f
}

// assertNewerRunUntouched: nothing touched the newer run's object or the
// agent's leftovers by name, the record is still active with the newer run
// unchanged and its object recorded, and the slug is still held.
func assertNewerRunUntouched(t *testing.T, f *partitionFixture, newRunState string) {
	t.Helper()
	for _, c := range f.d.recorded() {
		if strings.Contains(c, fenceNewUID) || strings.HasPrefix(c, "cleanupAgentResources:") {
			t.Errorf("a path fenced to %s acted on the newer run: %s (calls %v)", fenceOldRun, c, f.d.recorded())
		}
	}
	rec, ok, err := f.a.srv.ownership.Get("proj-1", "agent-a1")
	if err != nil || !ok {
		t.Fatalf("record: ok=%v err=%v", ok, err)
	}
	if rec.State != OwnershipStateActive {
		t.Errorf("record moved to %s while %s is live", rec.State, fenceNewRun)
	}
	run := rec.Run(fenceNewRun)
	if run == nil || run.State != newRunState {
		t.Errorf("newer run = %+v, want state %s", run, newRunState)
	}
	if !rec.OwnsUID(fenceNewUID) {
		t.Errorf("the newer run's object is no longer recorded: %+v", rec.Runs)
	}
	if holder, err := f.a.srv.ownership.SlugHolder("proj-1", "worker"); err != nil || holder != "agent-a1" {
		t.Errorf("slug holder = %q (%v), want it still held", holder, err)
	}
	// The newer run can keep recording objects (its start may be in flight).
	if err := f.a.srv.ownership.AddResource("proj-1", "agent-a1", fenceNewRun, api.ResourceHandle{Kind: api.ResourceKindSecret, Name: "late", UID: "uid-late"}); err != nil {
		t.Errorf("the newer run can no longer record objects: %v", err)
	}
}

// TestFlatRunFence_OldRunDeleteSparesNewerRun is the review reproduction:
// a delete fenced to the old run removes only the old run's object and
// moves only that run to deleted.
func TestFlatRunFence_OldRunDeleteSparesNewerRun(t *testing.T) {
	for _, state := range []string{OwnershipStateProvisioning, OwnershipStateCreated} {
		t.Run(state, func(t *testing.T) {
			f := newRunFenceFixture(t, state, false)
			w := serveFlat(f.a.srv, http.MethodDelete, "/api/v1/agents/worker?projectId=proj-1&runId="+fenceOldRun+"&deleteFiles=true", "")
			if w.Code >= 300 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			assertNewerRunUntouched(t, f, state)
			rec, _, _ := f.a.srv.ownership.Get("proj-1", "agent-a1")
			if old := rec.Run(fenceOldRun); old == nil || old.State != OwnershipStateDeleted {
				t.Errorf("old run = %+v, want deleted", old)
			}
			if rec.OwnsUID("cid-a1") {
				t.Error("the old run's object is still recorded after its delete")
			}
		})
	}
}

// TestFlatRunFence_NoDeleteOrCleanupPathActsOnNewerRun is the invariant:
// every delete and cleanup path of the operation matrix (deleteAgentFenced
// in each mode, the leftover cleanup on both of its branches, the sync
// start undo, the async launch cleanup and the delete record transition),
// fenced to the old run, leaves the newer run, its objects, the record and
// the slug alone. A new delete or cleanup path belongs in this table.
func TestFlatRunFence_NoDeleteOrCleanupPathActsOnNewerRun(t *testing.T) {
	deleteURL := "/api/v1/agents/worker?projectId=proj-1&runId=" + fenceOldRun
	rows := []struct {
		name    string
		oldGone bool
		act     func(t *testing.T, f *partitionFixture)
	}{
		{"delete with files", false, func(t *testing.T, f *partitionFixture) {
			serveFlat(f.a.srv, http.MethodDelete, deleteURL+"&deleteFiles=true", "")
		}},
		{"delete runtime entry only", false, func(t *testing.T, f *partitionFixture) {
			serveFlat(f.a.srv, http.MethodDelete, deleteURL, "")
		}},
		{"soft delete", false, func(t *testing.T, f *partitionFixture) {
			serveFlat(f.a.srv, http.MethodDelete, deleteURL+"&softDelete=true", "")
		}},
		{"local-only delete", false, func(t *testing.T, f *partitionFixture) {
			serveFlat(f.a.srv, http.MethodDelete, deleteURL+"&localOnly=true&deleteFiles=true", "")
		}},
		{"delete after the old container is gone (leftover cleanup)", true, func(t *testing.T, f *partitionFixture) {
			serveFlat(f.a.srv, http.MethodDelete, deleteURL+"&deleteFiles=true", "")
		}},
		{"leftover cleanup called directly", true, func(t *testing.T, f *partitionFixture) {
			f.a.srv.cleanupLeftoverAgentResources(context.Background(), "worker", "proj-1", fenceOldRun)
		}},
		{"sync start undo of the old run", false, func(t *testing.T, f *partitionFixture) {
			o := &ownedStart{store: f.a.srv.ownership, projectID: "proj-1", agentID: "agent-a1", runID: fenceOldRun,
				handles: []api.ResourceHandle{{Kind: api.ResourceKindContainer, Name: "worker", UID: "cid-a1"}}, err: errors.New("mirror failed")}
			f.a.srv.ownedStarts.Store(fenceOldRun, o)
			_ = f.a.srv.completeOwnedStart(context.Background(), f.a.mgr, fenceOldRun, nil, true)
		}},
		{"async launch cleanup of the old launch", false, func(t *testing.T, f *partitionFixture) {
			rec := &launchRecord{ID: "launch-old", AgentID: "agent-a1", RunID: fenceOldRun,
				Handles: []agent.ResourceHandle{{Kind: api.ResourceKindContainer, Name: "worker", UID: "cid-a1"}}}
			f.a.srv.cleanupLaunchResources(f.a.mgr, rec)
		}},
		{"delete record transition", false, func(t *testing.T, f *partitionFixture) {
			od, err := f.a.srv.beginOwnedDelete("proj-1", "worker", fenceOldRun, true, true)
			if err != nil {
				t.Fatal(err)
			}
			f.a.srv.finishOwnedDelete(context.Background(), f.a.mgr, od)
		}},
	}
	for _, row := range rows {
		for _, state := range []string{OwnershipStateProvisioning, OwnershipStateCreated} {
			t.Run(row.name+"/"+state, func(t *testing.T) {
				f := newRunFenceFixture(t, state, row.oldGone)
				row.act(t, f)
				assertNewerRunUntouched(t, f, state)
			})
		}
	}
}

// TestFlatRunFence_WholeDeleteWithoutOtherLiveRun: with no other live run,
// a whole-agent delete fenced to the agent's only run still deletes the
// whole record and releases the slug.
func TestFlatRunFence_WholeDeleteWithoutOtherLiveRun(t *testing.T) {
	f := newPartitionFixture(t)
	w := serveFlat(f.a.srv, http.MethodDelete, "/api/v1/agents/worker?projectId=proj-1&runId="+fenceOldRun+"&deleteFiles=true", "")
	if w.Code >= 300 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	rec, _, _ := f.a.srv.ownership.Get("proj-1", "agent-a1")
	if rec.State != OwnershipStateDeleted {
		t.Fatalf("record = %s, want deleted", rec.State)
	}
	if holder, _ := f.a.srv.ownership.SlugHolder("proj-1", "worker"); holder != "" {
		t.Fatalf("slug still held by %s", holder)
	}
}
