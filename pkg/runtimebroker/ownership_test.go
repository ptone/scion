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
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
