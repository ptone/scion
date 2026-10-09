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

//go:build !no_sqlite

package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/brokerhost"
	"github.com/GoogleCloudPlatform/scion/pkg/brokeridentity"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker"
)

// Shared execution scopes (ptone/scion#3274, P2.3): instances on one
// execution scope are hosted together; unresolved ownership on a scope
// refuses every instance of that scope before any activates.

func sharedScopeIdentity(t *testing.T, globalDir, key, daemonID string) *brokeridentity.Identity {
	t.Helper()
	id, err := brokeridentity.LoadOrCreate(brokeridentity.InstanceDir(globalDir, key), key, "docker",
		brokeridentity.ExecutionScope{Type: "docker", Docker: &brokeridentity.DockerScope{DaemonID: daemonID}}, nil)
	require.NoError(t, err)
	return id
}

func serverOf(t *testing.T, h *brokerhost.Host, key string) *runtimebroker.Server {
	t.Helper()
	for _, a := range h.Active() {
		if a.Context.Instance.Key == key {
			return a.Server
		}
	}
	t.Fatalf("instance %s is not active", key)
	return nil
}

func listedOn(t *testing.T, srv *runtimebroker.Server) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Agents []api.AgentInfo `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	var ids []string
	for _, a := range resp.Agents {
		ids = append(ids, a.ContainerID)
	}
	sort.Strings(ids)
	return ids
}

// TestFlatSharedScope_ConcurrentOperationsStayPartitioned: two instances on
// one Docker daemon both activate through the production preflight; each
// lists only its own agents, and concurrent deletes and lists from both
// instances act only on each instance's own objects (one instance's delete
// of the other's agent touches nothing).
func TestFlatSharedScope_ConcurrentOperationsStayPartitioned(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := t.TempDir()
	idA := sharedScopeIdentity(t, globalDir, "docker-a", "shared-daemon")
	idB := sharedScopeIdentity(t, globalDir, "docker-b", "shared-daemon")
	d := &partitionDaemon{objects: []api.AgentInfo{
		partitionObject(idA.RuntimeBrokerID, "agent-a1", "worker", "cid-a1"),
		partitionObject(idA.RuntimeBrokerID, "agent-a2", "helper", "cid-a2"),
		partitionObject(idB.RuntimeBrokerID, "agent-b1", "coder", "cid-b1"),
		partitionObject(idB.RuntimeBrokerID, "agent-b2", "tester", "cid-b2"),
	}}
	h := preparePartitionHostOn(t, globalDir, map[string]*partitionDaemon{"docker-a": d, "docker-b": d}, nil)
	for _, st := range h.Status() {
		require.Equal(t, brokerhost.StateActive, st.State, "%s: %s", st.Key, st.Error)
	}
	a, b := serverOf(t, h, "docker-a"), serverOf(t, h, "docker-b")
	assert.Equal(t, []string{"cid-a1", "cid-a2"}, listedOn(t, a))
	assert.Equal(t, []string{"cid-b1", "cid-b2"}, listedOn(t, b))

	del := func(srv *runtimebroker.Server, slug string) int {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/"+slug+"?projectId=proj-1", nil))
		return rec.Code
	}
	var wg sync.WaitGroup
	codes := make(chan [2]string, 16)
	for i := 0; i < 4; i++ {
		wg.Add(4)
		go func() { defer wg.Done(); del(a, "worker") }()
		go func() { defer wg.Done(); del(b, "coder") }()
		go func() {
			defer wg.Done()
			if c := del(a, "tester"); c < 300 && c != http.StatusAccepted {
				codes <- [2]string{"docker-a deleted docker-b's tester", http.StatusText(c)}
			}
		}()
		go func() { defer wg.Done(); _ = listedOn(t, b) }()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		t.Errorf("%s (%s)", c[0], c[1])
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, call := range d.deletes {
		switch call {
		case "cid-a1", "stop:cid-a1", "cid-b1", "stop:cid-b1":
		default:
			t.Errorf("a delete reached %s, which neither delete targeted", call)
		}
	}
	assert.Contains(t, d.deletes, "cid-a1")
	assert.Contains(t, d.deletes, "cid-b1")
}

// TestFlatSharedScope_UnresolvedHistoricalObjectRefusesTheWholeGroup: an
// unlabeled (historical) agent object on a shared daemon refuses every
// instance on that daemon before any is activated; an instance on another
// daemon activates.
func TestFlatSharedScope_UnresolvedHistoricalObjectRefusesTheWholeGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := t.TempDir()
	shared := &partitionDaemon{objects: []api.AgentInfo{{Name: "old-agent", ContainerID: "cid-old", Labels: map[string]string{
		"scion.agent": "true", "scion.name": "old-agent", "scion.project_id": "proj-1"}}}}
	other := &partitionDaemon{}
	h := preparePartitionHostOn(t, globalDir,
		map[string]*partitionDaemon{"docker-a": shared, "docker-b": shared, "docker-c": other},
		map[string]string{"docker-c": "other-daemon"})
	st := map[string]brokerhost.InstanceStatus{}
	for _, s := range h.Status() {
		st[s.Key] = s
	}
	for _, k := range []string{"docker-a", "docker-b"} {
		assert.Equal(t, brokerhost.StateRefused, st[k].State, k)
		assert.Equal(t, "ownership_unresolved", st[k].Reason, k)
	}
	assert.Equal(t, brokerhost.StateActive, st["docker-c"].State, "another daemon is another scope")
	shared.mu.Lock()
	assert.Empty(t, shared.deletes, "nothing on the shared daemon was touched")
	shared.mu.Unlock()
}

// TestFlatSharedScope_OneInstancesUnresolvedObjectRefusesItsSiblings: an
// object labelled for instance A with incomplete labels is unresolved for A
// only (B ignores another instance's object), yet it refuses B too: every
// instance on the scope is refused before any activates.
func TestFlatSharedScope_OneInstancesUnresolvedObjectRefusesItsSiblings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := t.TempDir()
	idA := sharedScopeIdentity(t, globalDir, "docker-a", "shared-daemon")
	sharedScopeIdentity(t, globalDir, "docker-b", "shared-daemon")
	d := &partitionDaemon{objects: []api.AgentInfo{{Name: "half", ContainerID: "cid-half", Labels: map[string]string{
		"scion.agent": "true", api.LabelRuntimeBrokerID: idA.RuntimeBrokerID}}}}
	h := preparePartitionHostOn(t, globalDir, map[string]*partitionDaemon{"docker-a": d, "docker-b": d}, nil)
	for _, s := range h.Status() {
		assert.Equal(t, brokerhost.StateRefused, s.State, s.Key)
		assert.Equal(t, "ownership_unresolved", s.Reason, s.Key)
	}
}
