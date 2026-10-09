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

package brokerhost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/brokeridentity"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker"
)

func dockerInstance(key, name string) config.V1RuntimeBrokerInstanceConfig {
	return config.V1RuntimeBrokerInstanceConfig{Key: key, Name: name,
		RuntimeTarget: &config.V1RuntimeTargetConfig{Type: "docker"}}
}

// fakeActivator records activations and refusals; refuse lists keys whose
// Hub binding is refused.
type fakeActivator struct {
	mu        sync.Mutex
	refuse    map[string]error
	activated []string
	refused   map[string]error
}

func (a *fakeActivator) Activate(_ context.Context, c Candidate) (*Activation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.refuse[c.Instance.Key]; err != nil {
		return nil, err
	}
	a.activated = append(a.activated, c.Instance.Key)
	return &Activation{}, nil
}

func (a *fakeActivator) Refused(inst config.V1RuntimeBrokerInstanceConfig, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refused == nil {
		a.refused = map[string]error{}
	}
	a.refused[inst.Key] = err
}

type hostFixture struct {
	globalDir string
	activator *fakeActivator
	daemons   map[string]string // instance key -> daemon ID
	probeErr  map[string]error
	built     []string
}

func newFixture(t *testing.T) *hostFixture {
	t.Helper()
	return &hostFixture{globalDir: t.TempDir(), activator: &fakeActivator{}, daemons: map[string]string{}, probeErr: map[string]error{}}
}

func (f *hostFixture) config(t *testing.T, instances ...config.V1RuntimeBrokerInstanceConfig) Config {
	t.Helper()
	return Config{
		GlobalDir: f.globalDir,
		Instances: instances,
		Mode:      ModeColocated,
		NewRuntime: func(_ context.Context, inst config.V1RuntimeBrokerInstanceConfig) (runtime.Runtime, error) {
			return &runtime.MockRuntime{NameFunc: func() string { return "docker" }}, nil
		},
		ProbeScope: func(_ context.Context, inst config.V1RuntimeBrokerInstanceConfig, _ runtime.Runtime) (brokeridentity.ExecutionScope, error) {
			if err := f.probeErr[inst.Key]; err != nil {
				return brokeridentity.ExecutionScope{}, err
			}
			d := f.daemons[inst.Key]
			if d == "" {
				d = "daemon-" + inst.Key
			}
			return brokeridentity.ExecutionScope{Type: "docker", Docker: &brokeridentity.DockerScope{DaemonID: d}}, nil
		},
		Activator: f.activator,
		BuildServer: func(ic InstanceContext) (*runtimebroker.Server, error) {
			f.built = append(f.built, ic.Instance.Key)
			cfg := runtimebroker.DefaultServerConfig()
			cfg.BrokerID = ic.Identity.RuntimeBrokerID
			cfg.BrokerName = ic.Instance.Name
			cfg.StateDir = t.TempDir()
			cfg.FlatInstance = &runtimebroker.FlatInstanceConfig{Identity: ic.Identity, Instance: ic.Instance, HubInProcess: true}
			return runtimebroker.New(cfg, ic.Manager, ic.Runtime), nil
		},
	}
}

func statusByKey(h *Host) map[string]InstanceStatus {
	out := map[string]InstanceStatus{}
	for _, st := range h.Status() {
		out[st.Key] = st
	}
	return out
}

func TestHost_TwoInstancesStableIdentitiesAcrossRestart(t *testing.T) {
	f := newFixture(t)
	insts := []config.V1RuntimeBrokerInstanceConfig{dockerInstance("docker-a", "a"), dockerInstance("docker-b", "b")}

	h, err := New(f.config(t, insts...))
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))
	first := statusByKey(h)
	require.Equal(t, StateActive, first["docker-a"].State)
	require.Equal(t, StateActive, first["docker-b"].State)
	assert.NotEqual(t, first["docker-a"].RuntimeBrokerID, first["docker-b"].RuntimeBrokerID, "two distinct Runtime Broker IDs")
	assert.Len(t, h.Active(), 2)
	assert.True(t, h.Ready())

	srvA, ok := h.Instance(first["docker-a"].RuntimeBrokerID)
	require.True(t, ok)
	srvB, ok := h.Instance(first["docker-b"].RuntimeBrokerID)
	require.True(t, ok)
	assert.NotSame(t, srvA, srvB)
	_, ok = h.Instance("unknown-id")
	assert.False(t, ok, "no fallback to any instance for an unknown ID")

	// A restart (new host over the same state) keeps both identities.
	h2, err := New(f.config(t, insts...))
	require.NoError(t, err)
	require.NoError(t, h2.Prepare(context.Background()))
	again := statusByKey(h2)
	assert.Equal(t, first["docker-a"].RuntimeBrokerID, again["docker-a"].RuntimeBrokerID)
	assert.Equal(t, first["docker-b"].RuntimeBrokerID, again["docker-b"].RuntimeBrokerID)
	assert.Equal(t, first["docker-a"].RuntimeTargetID, again["docker-a"].RuntimeTargetID)
}

func TestHost_InvalidConfigurationRefusesWholeProcess(t *testing.T) {
	f := newFixture(t)
	for name, insts := range map[string][]config.V1RuntimeBrokerInstanceConfig{
		"duplicate key": {dockerInstance("a", "x"), dockerInstance("a", "y")},
		"invalid key":   {dockerInstance("Bad_Key", "x"), dockerInstance("b", "y")},
		"none":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(f.config(t, insts...))
			require.Error(t, err)
		})
	}
	assert.Empty(t, f.built, "no instance is built")
}

// TestHost_DuplicateScopeRefusedBeforeServingWork: two instances on one
// Docker daemon are both refused in pass 1, before any Hub activation; an
// instance on its own scope still activates.
func TestHost_DuplicateScopeRefusedBeforeServingWork(t *testing.T) {
	f := newFixture(t)
	f.daemons["docker-a"] = "shared-daemon"
	f.daemons["docker-b"] = "shared-daemon"
	h, err := New(f.config(t, dockerInstance("docker-a", "a"), dockerInstance("docker-b", "b"), dockerInstance("docker-c", "c")))
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))

	st := statusByKey(h)
	assert.Equal(t, StateRefused, st["docker-a"].State)
	assert.Equal(t, StateRefused, st["docker-b"].State)
	assert.Equal(t, StateActive, st["docker-c"].State)
	assert.Equal(t, []string{"docker-c"}, f.activator.activated, "a conflict-refused instance is never activated with the Hub")
	assert.Equal(t, []string{"docker-c"}, f.built)
	for _, k := range []string{"docker-a", "docker-b"} {
		var sc *ScopeConflictError
		require.True(t, errors.As(f.activator.refused[k], &sc), "refusal reported for %s", k)
		assert.Equal(t, []string{"docker-a", "docker-b"}, sc.Instances)
		assert.Contains(t, st[k].Error, "shared-daemon")
	}
	assert.False(t, h.Ready())
	// Refused instances keep their local identities (no cleanup).
	_, err = os.Stat(filepath.Join(brokeridentity.InstanceDir(f.globalDir, "docker-a"), brokeridentity.IdentityFileName))
	assert.NoError(t, err)
}

func TestHost_RefusalRejectsOnlyThatInstance(t *testing.T) {
	f := newFixture(t)
	f.probeErr["docker-a"] = brokeridentity.ErrExecutionScopeUnidentified
	f.activator.refuse = map[string]error{"docker-b": errors.New("runtime_target_ack_missing (activate): not bound")}
	h, err := New(f.config(t, dockerInstance("docker-a", "a"), dockerInstance("docker-b", "b"), dockerInstance("docker-c", "c")))
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))

	st := statusByKey(h)
	assert.Equal(t, StateRefused, st["docker-a"].State)
	assert.Empty(t, st["docker-a"].RuntimeBrokerID, "refused before an identity was loaded")
	assert.Equal(t, StateRefused, st["docker-b"].State)
	assert.Contains(t, st["docker-b"].Error, "runtime_target_ack_missing")
	assert.Equal(t, StateActive, st["docker-c"].State)
	assert.Contains(t, f.activator.refused, "docker-a")
	assert.Contains(t, f.activator.refused, "docker-b")
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestHost_MultipleConfiguredOnlyOneActiveHasNoInstanceRoute: with more than
// one instance configured, the listener never routes to the surviving
// instance, and readiness stays false.
func TestHost_MultipleConfiguredOnlyOneActiveHasNoInstanceRoute(t *testing.T) {
	f := newFixture(t)
	f.activator.refuse = map[string]error{"docker-b": errors.New("refused")}
	h, err := New(f.config(t, dockerInstance("docker-a", "a"), dockerInstance("docker-b", "b")))
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))
	require.Len(t, h.Active(), 1)

	handler := h.Handler()
	assert.Equal(t, http.StatusNotFound, get(t, handler, "/api/v1/info").Code, "no instance route")
	assert.Equal(t, http.StatusNotFound, get(t, handler, "/api/v1/agents").Code)
	assert.Equal(t, http.StatusOK, get(t, handler, "/healthz").Code, "liveness: the host serves")
	ready := get(t, handler, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, ready.Code, "readiness is false while a configured instance is refused")
	assert.Contains(t, ready.Body.String(), `"docker-b"`)
	assert.Contains(t, ready.Body.String(), `"refused"`)
}

func TestHost_SingleConfiguredKeepsInstanceRoute(t *testing.T) {
	f := newFixture(t)
	h, err := New(f.config(t, dockerInstance("docker-a", "a")))
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))

	rec := get(t, h.Handler(), "/api/v1/info")
	assert.Equal(t, http.StatusOK, rec.Code, "the P1 route serves the single configured instance")
	assert.Contains(t, rec.Body.String(), h.Status()[0].RuntimeBrokerID)
}

func TestHost_InactiveSingletonServesHealthOnly(t *testing.T) {
	f := newFixture(t)
	f.activator.refuse = map[string]error{"docker-a": errors.New("refused")}
	h, err := New(f.config(t, dockerInstance("docker-a", "a")))
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))

	handler := h.Handler()
	assert.Equal(t, http.StatusNotFound, get(t, handler, "/api/v1/info").Code, "an inactive singleton's routes do not execute")
	assert.Equal(t, http.StatusServiceUnavailable, get(t, handler, "/readyz").Code)
}

func TestHost_PrepareOnce(t *testing.T) {
	f := newFixture(t)
	h, err := New(f.config(t, dockerInstance("docker-a", "a")))
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))
	assert.Error(t, h.Prepare(context.Background()))
	assert.Error(t, (&Host{cfg: h.cfg}).Run(context.Background()), "Run requires Prepare")
}

// TestHost_OwnershipKeysConflictAndReadFailure: a key claimed by two
// instances is reported to both (and only that key); an instance whose keys
// cannot be read is refused in pass 1 with ownership_unresolved.
func TestHost_OwnershipKeysConflictAndReadFailure(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t, dockerInstance("docker-a", "a"), dockerInstance("docker-b", "b"), dockerInstance("docker-c", "c"), dockerInstance("docker-d", "d"))
	cfg.OwnershipKeys = func(_ context.Context, c Candidate) ([]string, error) {
		switch c.Instance.Key {
		case "docker-a":
			return []string{"agent:p/x", "slug:p/x", "agent:p/a"}, nil
		case "docker-b":
			return []string{"agent:p/x", "slug:p/x"}, nil
		case "docker-c":
			return []string{"agent:p/c"}, nil
		}
		return nil, errors.New("record unreadable")
	}
	got := map[string]map[string]bool{}
	build := cfg.BuildServer
	cfg.BuildServer = func(ic InstanceContext) (*runtimebroker.Server, error) {
		got[ic.Instance.Key] = ic.ConflictingKeys
		return build(ic)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"agent:p/x": true, "slug:p/x": true}
	for _, k := range []string{"docker-a", "docker-b"} {
		if len(got[k]) != len(want) || !got[k]["agent:p/x"] || !got[k]["slug:p/x"] {
			t.Errorf("%s conflicting keys = %v, want %v", k, got[k], want)
		}
	}
	if len(got["docker-c"]) != 0 {
		t.Errorf("docker-c has conflicting keys %v", got["docker-c"])
	}
	st := statusByKey(h)["docker-d"]
	if st.State != StateRefused || st.Reason != "ownership_unresolved" {
		t.Fatalf("docker-d = %s/%s, want refused/ownership_unresolved", st.State, st.Reason)
	}
	if _, built := got["docker-d"]; built {
		t.Fatal("an instance whose ownership keys cannot be read was built")
	}
}
