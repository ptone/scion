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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

func handle(uid string) api.ResourceHandle {
	return api.ResourceHandle{Kind: "container", Name: "obj-" + uid, UID: uid}
}

func TestOwnershipStore_RunLifecycle(t *testing.T) {
	dir := t.TempDir()
	s := NewOwnershipStore(dir, "broker-a")

	_, ok, err := s.Get("proj-1", "agent-1")
	require.NoError(t, err)
	assert.False(t, ok, "no record before provisioning")

	require.NoError(t, s.BeginRun("proj-1", "agent-1", "worker", "run-1"))
	rec, ok, err := s.Get("proj-1", "agent-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "broker-a", rec.RuntimeBrokerID)
	assert.Equal(t, OwnershipStateActive, rec.State)
	assert.Equal(t, OwnershipStateProvisioning, rec.Run("run-1").State)
	rev := rec.Revision

	require.NoError(t, s.AddResource("proj-1", "agent-1", "run-1", handle("uid-1")))
	require.NoError(t, s.AddResource("proj-1", "agent-1", "run-1", handle("uid-1")), "idempotent")
	require.NoError(t, s.SetRunState("proj-1", "agent-1", "run-1", OwnershipStateCreated))
	rec, _, _ = s.Get("proj-1", "agent-1")
	assert.Greater(t, rec.Revision, rev, "every write increases the revision")
	assert.True(t, rec.OwnsUID("uid-1"))
	assert.False(t, rec.OwnsUID("uid-2"))
	assert.Len(t, rec.Run("run-1").Resources, 1)

	// States never move backwards.
	assert.ErrorIs(t, s.SetRunState("proj-1", "agent-1", "run-1", OwnershipStateProvisioning), ErrOwnershipStateOrder)

	// An append never creates a record or run.
	assert.ErrorIs(t, s.AddResource("proj-1", "agent-9", "run-1", handle("x")), ErrOwnershipNotRecorded)
	assert.ErrorIs(t, s.AddResource("proj-1", "agent-1", "run-9", handle("x")), ErrOwnershipNotRecorded)
	assert.Error(t, s.AddResource("proj-1", "agent-1", "run-1", api.ResourceHandle{}), "a UID is required")

	info, err := os.Stat(filepath.Join(dir, "ownership"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	info, err = os.Stat(filepath.Join(dir, "ownership", "proj-1", "agent-1.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestOwnershipStore_DeleteVersusLateCallback: once deleting, a late handle
// callback cannot add to the record, a deleted run cannot be revived, and
// the tombstone stays.
func TestOwnershipStore_DeleteVersusLateCallback(t *testing.T) {
	s := NewOwnershipStore(t.TempDir(), "broker-a")
	require.NoError(t, s.BeginRun("proj-1", "agent-1", "worker", "run-1"))
	require.NoError(t, s.AddResource("proj-1", "agent-1", "run-1", handle("uid-1")))
	require.NoError(t, s.SetRecordState("proj-1", "agent-1", OwnershipStateDeleting))

	assert.ErrorIs(t, s.AddResource("proj-1", "agent-1", "run-1", handle("uid-late")), ErrOwnershipStateOrder)
	assert.ErrorIs(t, s.SetRecordState("proj-1", "agent-1", OwnershipStateActive), ErrOwnershipStateOrder, "deleting never reverts")
	assert.ErrorIs(t, s.BeginRun("proj-1", "agent-1", "worker", "run-2"), ErrOwnershipStateOrder, "a deleting record takes no run")

	require.NoError(t, s.SetRecordState("proj-1", "agent-1", OwnershipStateDeleted))
	assert.ErrorIs(t, s.BeginRun("proj-1", "agent-1", "worker", "run-1"), ErrOwnershipStateOrder, "a terminal run is never revived")
	assert.ErrorIs(t, s.AddResource("proj-1", "agent-1", "run-1", handle("uid-late")), ErrOwnershipStateOrder)
	rec, ok, err := s.Get("proj-1", "agent-1")
	require.NoError(t, err)
	require.True(t, ok, "the tombstone stays")
	assert.Equal(t, OwnershipStateDeleted, rec.State)
	assert.False(t, rec.OwnsUID("uid-1"), "a deleted record owns nothing for operations")
}

// TestOwnershipStore_SameSlugNewAgentID: a new agent ID cannot take a slug
// held by a live agent; after the holder is deleted and its objects are
// confirmed absent, its slug is released (tombstone kept) and the new agent
// may use it.
func TestOwnershipStore_SameSlugNewAgentID(t *testing.T) {
	s := NewOwnershipStore(t.TempDir(), "broker-a")
	require.NoError(t, s.BeginRun("proj-1", "agent-old", "worker", "run-1"))
	require.NoError(t, s.AddResource("proj-1", "agent-old", "run-1", handle("uid-1")))

	assert.ErrorIs(t, s.BeginRun("proj-1", "agent-new", "worker", "run-2"), ErrOwnershipSlugHeld)
	_, ok, _ := s.Get("proj-1", "agent-new")
	assert.False(t, ok, "nothing recorded for the refused agent")

	// Release requires deleted + every object confirmed absent.
	assert.ErrorIs(t, s.ReleaseSlug("proj-1", "agent-old"), ErrOwnershipStateOrder)
	require.NoError(t, s.SetRecordState("proj-1", "agent-old", OwnershipStateDeleted))
	assert.Error(t, s.ReleaseSlug("proj-1", "agent-old"), "uid-1 not confirmed absent")
	require.NoError(t, s.MarkAbsent("proj-1", "agent-old", "uid-1"))
	require.NoError(t, s.ReleaseSlug("proj-1", "agent-old"))

	require.NoError(t, s.BeginRun("proj-1", "agent-new", "worker", "run-2"))
	holder, err := s.SlugHolder("proj-1", "worker")
	require.NoError(t, err)
	assert.Equal(t, "agent-new", holder)
	_, ok, _ = s.Get("proj-1", "agent-old")
	assert.True(t, ok, "the old tombstone stays")
}

// TestOwnershipStore_ParallelHandleAppends: concurrent callbacks for one
// run lose no handle.
func TestOwnershipStore_ParallelHandleAppends(t *testing.T) {
	s := NewOwnershipStore(t.TempDir(), "broker-a")
	require.NoError(t, s.BeginRun("proj-1", "agent-1", "worker", "run-1"))
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, s.AddResource("proj-1", "agent-1", "run-1", handle(fmt.Sprintf("uid-%d", i))))
		}(i)
	}
	wg.Wait()
	rec, _, err := s.Get("proj-1", "agent-1")
	require.NoError(t, err)
	assert.Len(t, rec.Run("run-1").Resources, 25)
	assert.EqualValues(t, 26, rec.Revision, "one revision per write")
}

func TestOwnershipStore_KeysListAndForeignRecords(t *testing.T) {
	dir := t.TempDir()
	s := NewOwnershipStore(dir, "broker-a")
	require.NoError(t, s.BeginRun("proj-1", "agent-1", "worker", "run-1"))
	require.NoError(t, s.BeginRun("proj-0", "agent-2", "helper", "run-2"))

	list, err := s.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "proj-0", list[0].ProjectID, "sorted by key")
	leftovers, _ := filepath.Glob(filepath.Join(dir, "ownership", "proj-1", ".tmp-*"))
	assert.Empty(t, leftovers)

	for _, key := range [][3]string{{"..", "a", "s"}, {"p", "../a", "s"}, {"p/q", "a", "s"}, {"", "a", "s"}, {"p", "", "s"}, {"p", "a", ""}, {"p", "a\\b", "s"}, {"p", "by-slug", "s"}, {"p", "a", "../s"}} {
		err := s.BeginRun(key[0], key[1], key[2], "run")
		assert.True(t, errors.Is(err, ErrOwnershipKeyInvalid), "%v: %v", key, err)
	}

	// A record written by another instance is never read as this one's.
	other := NewOwnershipStore(dir, "broker-b")
	_, _, err = other.Get("proj-1", "agent-1")
	assert.ErrorIs(t, err, ErrOwnershipUnreadable)

	// A corrupt record is unresolved (an error), not "no record".
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ownership", "proj-1", "agent-1.json"), []byte("{"), 0o600))
	_, _, err = s.Get("proj-1", "agent-1")
	assert.ErrorIs(t, err, ErrOwnershipUnreadable)
	_, err = s.List()
	assert.Error(t, err)
}

func TestOwnershipStore_RepairSlugIndex(t *testing.T) {
	dir := t.TempDir()
	s := NewOwnershipStore(dir, "broker-a")
	require.NoError(t, s.BeginRun("proj-1", "agent-1", "worker", "run-1"))
	// A crash lost the index entry: repaired from the live record.
	require.NoError(t, os.Remove(filepath.Join(dir, "ownership", "proj-1", "by-slug", "worker")))
	problems, err := s.RepairSlugIndex()
	require.NoError(t, err)
	assert.Empty(t, problems)
	holder, _ := s.SlugHolder("proj-1", "worker")
	assert.Equal(t, "agent-1", holder)

	// An index naming another agent while a live record claims the slug is
	// reported, not reassigned.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ownership", "proj-1", "by-slug", "worker"), []byte("agent-x\n"), 0o600))
	problems, err = s.RepairSlugIndex()
	require.NoError(t, err)
	require.Len(t, problems, 1)
	holder, _ = s.SlugHolder("proj-1", "worker")
	assert.Equal(t, "agent-x", holder, "left as found for the operator")
}

func labelledObject(labels map[string]string) api.AgentInfo {
	l := map[string]string{api.LabelRuntimeBrokerID: "broker-a", "scion.project_id": "proj-1", "agent_id": "agent-1", "scion.name": "worker", api.LabelRunID: "run-1"}
	for k, v := range labels {
		if v == "" {
			delete(l, k)
		} else {
			l[k] = v
		}
	}
	return api.AgentInfo{Name: "worker", ContainerID: "cid-1", Labels: l}
}

// TestOwnershipStore_Reconstruct: a labelled object with complete metadata
// re-creates its record; incomplete or contradictory metadata is
// unresolved; a terminal run is never revived.
func TestOwnershipStore_Reconstruct(t *testing.T) {
	s := NewOwnershipStore(t.TempDir(), "broker-a")
	require.NoError(t, s.Reconstruct(labelledObject(nil)))
	rec, ok, err := s.Get("proj-1", "agent-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, OwnershipStateCreated, rec.Run("run-1").State)
	assert.True(t, rec.OwnsUID("cid-1"))
	require.NoError(t, s.Reconstruct(labelledObject(nil)), "idempotent")

	for name, labels := range map[string]map[string]string{
		"owner label alone": {"scion.project_id": "", "agent_id": "", "scion.name": "", api.LabelRunID: ""},
		"no run":            {api.LabelRunID: ""},
		"no agent ID":       {"agent_id": ""},
		"another owner":     {api.LabelRuntimeBrokerID: "broker-b"},
		"slug contradicts":  {"scion.name": "other"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, s.Reconstruct(labelledObject(labels)))
		})
	}

	require.NoError(t, s.SetRunState("proj-1", "agent-1", "run-1", OwnershipStateDeleted))
	o := labelledObject(nil)
	o.ContainerID = "cid-new"
	assert.Error(t, s.Reconstruct(o), "a terminal run is never revived")
}

// TestOwnershipStore_ConflictingKeysRefused: a key another configured
// instance claims too is refused for reads and writes; other keys and the
// raw listing are unaffected.
func TestOwnershipStore_ConflictingKeysRefused(t *testing.T) {
	s := NewOwnershipStore(t.TempDir(), "rb-a")
	for _, a := range [][2]string{{"agent-1", "one"}, {"agent-2", "two"}} {
		if err := s.BeginRun("proj", a[0], a[1], "run-"+a[0]); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := s.LiveKeys()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{OwnershipAgentKey("proj", "agent-1"), OwnershipSlugKey("proj", "one"), OwnershipAgentKey("proj", "agent-2"), OwnershipSlugKey("proj", "two")}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("LiveKeys = %v, want %v", keys, want)
	}

	s.SetConflicting(map[string]bool{OwnershipAgentKey("proj", "agent-1"): true, OwnershipSlugKey("proj", "three"): true})
	if _, _, err := s.Get("proj", "agent-1"); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("Get of a conflicting agent: %v", err)
	}
	if err := s.BeginRun("proj", "agent-1", "one", "run-new"); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("BeginRun of a conflicting agent: %v", err)
	}
	if err := s.SetRecordState("proj", "agent-1", OwnershipStateDeleting); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("state change of a conflicting agent: %v", err)
	}
	if err := s.BeginRun("proj", "agent-3", "three", "run-3"); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("BeginRun claiming a conflicting slug: %v", err)
	}
	if _, ok, _ := s.Get("proj", "agent-3"); ok {
		t.Fatal("a refused claim created a record")
	}
	if _, ok, err := s.Get("proj", "agent-2"); err != nil || !ok {
		t.Fatalf("an unrelated key is affected: ok=%v err=%v", ok, err)
	}
	if recs, err := s.List(); err != nil || len(recs) != 2 {
		t.Fatalf("List = %d records, %v", len(recs), err)
	}
	if !s.ConflictingLabels(map[string]string{"scion.project_id": "proj", "agent_id": "agent-1", "scion.name": "one"}) ||
		!s.ConflictingLabels(map[string]string{"scion.project_id": "proj", "agent_id": "agent-9", "scion.name": "three"}) ||
		s.ConflictingLabels(map[string]string{"scion.project_id": "proj", "agent_id": "agent-2", "scion.name": "two"}) {
		t.Fatal("ConflictingLabels does not match the conflicting keys")
	}
}

// TestLaunchHandleOwned: launch cleanup on a flat instance may delete an
// object its records hold (live or deleting), or one from the journal of
// its own launch whose recording failed; never an unknown, absent,
// deleted-record or conflicting-key object.
func TestLaunchHandleOwned(t *testing.T) {
	s := &Server{ownership: NewOwnershipStore(t.TempDir(), "rb-a"), agentLifecycleLog: slog.Default()}
	st := s.ownership
	for _, a := range []string{"agent-1", "agent-2", "agent-3"} {
		require.NoError(t, st.BeginRun("proj", a, "slug-"+a, "run-1"))
		require.NoError(t, st.AddResource("proj", a, "run-1", handle("uid-"+a)))
	}
	require.NoError(t, st.SetRecordState("proj", "agent-2", OwnershipStateDeleting))
	require.NoError(t, st.SetRecordState("proj", "agent-3", OwnershipStateDeleting))
	require.NoError(t, st.MarkAbsent("proj", "agent-3", "uid-agent-3"))

	assert.True(t, s.launchHandleOwned(handle("uid-agent-1")), "recorded by a live record")
	assert.True(t, s.launchHandleOwned(handle("uid-agent-2")), "recorded by a deleting record")
	assert.False(t, s.launchHandleOwned(handle("uid-agent-3")), "confirmed absent")
	assert.False(t, s.launchHandleOwned(handle("uid-unknown")), "never recorded")
	assert.False(t, s.launchHandleOwned(api.ResourceHandle{Name: "no-uid"}), "no UID")

	// A launch whose recording failed: its journal's objects become this
	// instance's to clean up.
	o := &ownedStart{store: st, projectID: "proj", agentID: "agent-1", runID: "run-2"}
	o.handles = []api.ResourceHandle{handle("uid-unmirrored")}
	o.err = errors.New("disk full")
	s.ownedStarts.Store("run-2", o)
	assert.False(t, s.launchHandleOwned(handle("uid-unmirrored")))
	require.Error(t, s.completeOwnedStart(context.Background(), nil, "run-2", errors.New("start failed"), false))
	assert.True(t, s.launchHandleOwned(handle("uid-unmirrored")), "journal of a launch whose recording failed")

	st.SetConflicting(map[string]bool{OwnershipAgentKey("proj", "agent-1"): true})
	assert.False(t, s.launchHandleOwned(handle("uid-agent-1")), "a conflicting key's object is not cleaned up")
}

// TestOwnershipStore_RepairSlugIndexReadsEntries: an index entry left for a
// tombstone whose objects are all confirmed absent (a crash between marking
// deleted and releasing) is released, so a new agent can take the slug; an
// entry naming a missing record or a tombstone with an object not yet
// confirmed absent is reported or kept, never reassigned.
func TestOwnershipStore_RepairSlugIndexReadsEntries(t *testing.T) {
	dir := t.TempDir()
	s := NewOwnershipStore(dir, "broker-a")
	// A tombstone whose release was lost.
	require.NoError(t, s.BeginRun("proj-1", "agent-old", "worker", "run-1"))
	require.NoError(t, s.AddResource("proj-1", "agent-old", "run-1", handle("uid-1")))
	require.NoError(t, s.MarkAbsent("proj-1", "agent-old", "uid-1"))
	require.NoError(t, s.SetRecordState("proj-1", "agent-old", OwnershipStateDeleted))
	// A tombstone with an object still recorded keeps its reservation.
	require.NoError(t, s.BeginRun("proj-1", "agent-busy", "busy", "run-1"))
	require.NoError(t, s.AddResource("proj-1", "agent-busy", "run-1", handle("uid-2")))
	require.NoError(t, s.SetRecordState("proj-1", "agent-busy", OwnershipStateDeleted))
	// An index entry naming an agent with no record.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ownership", "proj-1", "by-slug", "ghost"), []byte("agent-ghost\n"), 0o600))

	problems, err := s.RepairSlugIndex()
	require.NoError(t, err)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "agent-ghost")

	holder, _ := s.SlugHolder("proj-1", "worker")
	assert.Empty(t, holder, "the tombstone's slug is released")
	require.NoError(t, s.BeginRun("proj-1", "agent-new", "worker", "run-2"), "a new agent can take the released slug")
	holder, _ = s.SlugHolder("proj-1", "busy")
	assert.Equal(t, "agent-busy", holder, "not released while an object is not confirmed absent")
	holder, _ = s.SlugHolder("proj-1", "ghost")
	assert.Equal(t, "agent-ghost", holder, "left as found for the operator")
}

// TestOwnershipStore_ReconstructPodAndNewUID: a Kubernetes object (listed
// by pod name, without its UID) re-establishes the record and run but no
// handle; a container with a new ID under the same name and run of a live
// record is recorded as a separate object and the old one is kept.
func TestOwnershipStore_ReconstructPodAndNewUID(t *testing.T) {
	s := NewOwnershipStore(t.TempDir(), "broker-a")
	labels := map[string]string{api.LabelRuntimeBrokerID: "broker-a", "scion.project_id": "proj-1", "agent_id": "agent-1",
		"scion.name": "worker", api.LabelRunID: "run-1"}
	pod := api.AgentInfo{Name: "worker", ContainerID: "proj-1--worker", Runtime: "kubernetes", Labels: labels}
	require.NoError(t, s.Reconstruct(pod))
	rec, ok, err := s.Get("proj-1", "agent-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, rec.Run("run-1"))
	assert.Equal(t, OwnershipStateCreated, rec.Run("run-1").State)
	assert.Empty(t, rec.Run("run-1").Resources, "no handle from a pod name")
	require.NoError(t, s.Reconstruct(pod), "repeating is a no-op")

	labels2 := map[string]string{api.LabelRuntimeBrokerID: "broker-a", "scion.project_id": "proj-1", "agent_id": "agent-2",
		"scion.name": "helper", api.LabelRunID: "run-1"}
	require.NoError(t, s.Reconstruct(api.AgentInfo{Name: "helper", ContainerID: "cid-old", Labels: labels2}))
	require.NoError(t, s.Reconstruct(api.AgentInfo{Name: "helper", ContainerID: "cid-new", Labels: labels2}))
	rec, _, _ = s.Get("proj-1", "agent-2")
	assert.True(t, rec.OwnsUID("cid-old"), "the old object is kept until its absence is established")
	assert.True(t, rec.OwnsUID("cid-new"), "the new object is recorded separately")
	assert.Len(t, rec.Run("run-1").Resources, 2)
}

// TestOwnershipStore_ReconstructPodWithUID: a pod listed with its metadata
// UID and namespace (the Kubernetes runtime's List) is recorded as a pod
// handle carrying that UID, namespace and pod name; and a later complete
// read that lists the same pod by UID keeps it recorded.
func TestOwnershipStore_ReconstructPodWithUID(t *testing.T) {
	s := NewOwnershipStore(t.TempDir(), "broker-a")
	pod := api.AgentInfo{Name: "worker", ContainerID: "proj-1--worker", Runtime: "kubernetes",
		Kubernetes: &api.AgentK8sMetadata{Namespace: "agents", PodName: "proj-1--worker", UID: "pod-uid-1"},
		Labels: map[string]string{api.LabelRuntimeBrokerID: "broker-a", "scion.project_id": "proj-1", "agent_id": "agent-1",
			"scion.name": "worker", api.LabelRunID: "run-1"}}
	require.NoError(t, s.Reconstruct(pod))
	rec, _, err := s.Get("proj-1", "agent-1")
	require.NoError(t, err)
	res := rec.Run("run-1").Resources
	require.Len(t, res, 1)
	assert.Equal(t, OwnedResource{Kind: api.ResourceKindPod, Namespace: "agents", Name: "proj-1--worker", UID: "pod-uid-1", State: OwnedResourceRecorded}, res[0])

	n, err := s.ReconcileAbsent([]api.AgentInfo{pod})
	require.NoError(t, err)
	assert.Zero(t, n)
	rec, _, _ = s.Get("proj-1", "agent-1")
	assert.True(t, rec.OwnsUID("pod-uid-1"))
}
