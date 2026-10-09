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
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/brokercredentials"
	"github.com/GoogleCloudPlatform/scion/pkg/brokerhost"
	"github.com/GoogleCloudPlatform/scion/pkg/brokeridentity"
	"github.com/GoogleCloudPlatform/scion/pkg/brokerregistration"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker"
)

func TestFlatHostMode_SimulatedRemoteTakesRemotePath(t *testing.T) {
	origHub, origSim := enableHub, simulateRemoteBroker
	t.Cleanup(func() { enableHub, simulateRemoteBroker = origHub, origSim })
	s := newTestStore(t)
	cfg := &config.GlobalConfig{RuntimeBroker: config.RuntimeBrokerConfig{Enabled: true}}

	enableHub, simulateRemoteBroker = true, false
	assert.Equal(t, brokerhost.ModeColocated, flatHostMode(cfg, s))
	enableHub, simulateRemoteBroker = true, true
	assert.Equal(t, brokerhost.ModeRemote, flatHostMode(cfg, s), "--simulate-remote-broker takes the remote path even with a Hub in the process")
	enableHub, simulateRemoteBroker = false, false
	assert.Equal(t, brokerhost.ModeRemote, flatHostMode(cfg, s))
}

// selfReadHub answers GET /api/v1/runtime-brokers/{id} with the row it is
// told to return and records the requests.
type selfReadHub struct {
	*httptest.Server
	mu       sync.Mutex
	target   *api.RuntimeTargetDescriptor
	requests int
}

func newSelfReadHub(t *testing.T, target *api.RuntimeTargetDescriptor) *selfReadHub {
	t.Helper()
	h := &selfReadHub{target: target}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/runtime-brokers/")
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		h.mu.Lock()
		h.requests++
		row := map[string]any{"id": id, "name": "example-remote"}
		if h.target != nil {
			row["runtimeTarget"] = h.target
		}
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(row)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *selfReadHub) setTarget(target *api.RuntimeTargetDescriptor) {
	h.mu.Lock()
	h.target = target
	h.mu.Unlock()
}

func fakeScopeProber(daemonID string) brokerhost.ScopeProber {
	return func(context.Context, config.V1RuntimeBrokerInstanceConfig, runtime.Runtime) (brokeridentity.ExecutionScope, error) {
		return brokeridentity.ExecutionScope{Type: "docker", Docker: &brokeridentity.DockerScope{DaemonID: daemonID}}, nil
	}
}

// newRemoteTestHost builds a remote-mode host for one instance with the
// production remote activator.
func newRemoteTestHost(t *testing.T, globalDir string, inst config.V1RuntimeBrokerInstanceConfig) (*brokerhost.Host, *[]string) {
	t.Helper()
	var built []string
	h, err := brokerhost.New(brokerhost.Config{
		GlobalDir:  globalDir,
		Instances:  []config.V1RuntimeBrokerInstanceConfig{inst},
		Mode:       brokerhost.ModeRemote,
		NewRuntime: newFlatInstanceRuntime,
		ProbeScope: fakeScopeProber("daemon-1"),
		Activator:  &remoteFlatActivator{globalDir: globalDir},
		BuildServer: func(ic brokerhost.InstanceContext) (*runtimebroker.Server, error) {
			built = append(built, ic.Instance.Key)
			cfg := runtimebroker.DefaultServerConfig()
			cfg.BrokerID = ic.Identity.RuntimeBrokerID
			cfg.StateDir = filepath.Join(globalDir, "state", ic.Identity.RuntimeBrokerID)
			cfg.FlatInstance = &runtimebroker.FlatInstanceConfig{Identity: ic.Identity, Instance: ic.Instance,
				RemoteCredentials: ic.Activation.RemoteCredentials}
			return runtimebroker.New(cfg, ic.Manager, ic.Runtime), nil
		},
	})
	require.NoError(t, err)
	return h, &built
}

// TestFlatRemoteActivation_BindingCheckedOnEveryStart: a remote (or
// simulated-remote) instance activates only with its instance-scoped
// credentials after the self-read binding check, on every start; saved
// credentials never skip it, and legacy credentials are never used.
func TestFlatRemoteActivation_BindingCheckedOnEveryStart(t *testing.T) {
	ctx := context.Background()
	globalDir := t.TempDir()
	inst := config.V1RuntimeBrokerInstanceConfig{Key: "remote-docker", Name: "example-remote",
		RuntimeTarget: &config.V1RuntimeTargetConfig{Type: "docker"}}
	id, err := brokeridentity.LoadOrCreate(brokeridentity.InstanceDir(globalDir, inst.Key), inst.Key, "docker",
		brokeridentity.ExecutionScope{Type: "docker", Docker: &brokeridentity.DockerScope{DaemonID: "daemon-1"}}, nil)
	require.NoError(t, err)
	hub := newSelfReadHub(t, &id.RuntimeTarget)
	secret := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

	// Legacy credentials for this Runtime Broker ID are present in both
	// legacy locations; with no instance credentials the instance is not
	// registered.
	legacy := &brokercredentials.BrokerCredentials{Name: "hub", BrokerID: id.RuntimeBrokerID, SecretKey: secret, HubEndpoint: hub.URL}
	require.NoError(t, brokercredentials.NewMultiStore(filepath.Join(globalDir, "hub-credentials")).Save(legacy))
	require.NoError(t, brokercredentials.NewStore(filepath.Join(globalDir, brokercredentials.DefaultFileName)).Save(legacy))
	h, built := newRemoteTestHost(t, globalDir, inst)
	require.NoError(t, h.Prepare(ctx))
	st := h.Status()[0]
	assert.Equal(t, brokerhost.StateRefused, st.State)
	assert.Contains(t, st.Error, api.ErrCodeFlatRuntimeBrokerNotRegistered)
	assert.Empty(t, *built)
	assert.Zero(t, hub.requests, "no Hub request without instance credentials")

	// Instance credentials saved by the registration: the binding is read
	// and checked at each start.
	require.NoError(t, brokercredentials.NewMultiStore(brokerregistration.InstanceCredentialsDir(globalDir, inst.Key)).Save(
		&brokercredentials.BrokerCredentials{Name: "hub", BrokerID: id.RuntimeBrokerID, SecretKey: secret, HubEndpoint: hub.URL, AuthMode: brokercredentials.AuthModeHMAC}))
	h, built = newRemoteTestHost(t, globalDir, inst)
	require.NoError(t, h.Prepare(ctx))
	require.Equal(t, brokerhost.StateActive, h.Status()[0].State)
	assert.Equal(t, []string{inst.Key}, *built)
	active := h.Active()
	require.Len(t, active, 1)
	require.Len(t, active[0].Context.Activation.RemoteCredentials, 1)
	assert.Equal(t, 1, hub.requests)

	// The next start against a Hub that no longer acknowledges the binding
	// is refused, although the credentials are saved.
	hub.setTarget(nil)
	h, built = newRemoteTestHost(t, globalDir, inst)
	require.NoError(t, h.Prepare(ctx))
	st = h.Status()[0]
	assert.Equal(t, brokerhost.StateRefused, st.State)
	assert.Contains(t, st.Error, api.ErrCodeRuntimeTargetAckMissing)
	assert.Empty(t, *built, "not activated: no Runtime Broker server")
	assert.Equal(t, 2, hub.requests, "every start reads the binding")

	// A different binding is refused the same way.
	hub.setTarget(&api.RuntimeTargetDescriptor{ID: "another-target", Type: "docker"})
	h, _ = newRemoteTestHost(t, globalDir, inst)
	require.NoError(t, h.Prepare(ctx))
	assert.Contains(t, h.Status()[0].Error, api.ErrCodeRuntimeTargetBindingConflict)
}

// registrationHub is a capable fake Hub for 'broker register --instance'.
type registrationHub struct {
	*httptest.Server
	mu     sync.Mutex
	bodies map[string]map[string]any
	auth   map[string]string
}

func newRegistrationHub(t *testing.T) *registrationHub {
	t.Helper()
	h := &registrationHub{bodies: map[string]map[string]any{}, auth: map[string]string{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.bodies[r.URL.Path] = body
		h.auth[r.URL.Path] = r.Header.Get("Authorization")
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/brokers":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"brokerId": body["brokerId"], "joinToken": "scion_join_t",
				"expiresAt": "2026-10-09T05:00:00Z", "runtimeTarget": body["runtimeTarget"]})
		case "/api/v1/brokers/join":
			_ = json.NewEncoder(w).Encode(map[string]any{"brokerId": body["brokerId"],
				"secretKey": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
				"hubEndpoint": "http://" + r.Host, "runtimeTarget": body["runtimeTarget"]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.Close)
	return h
}

func setupRegisterInstanceTest(t *testing.T, hubURL string) string {
	t.Helper()
	isolateJoinEnv(t)
	_, globalDir := brokerTestHome(t)
	t.Setenv("SCION_HUB_ENDPOINT", hubURL)
	t.Setenv("SCION_DEV_TOKEN", joinTestDevToken)
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "settings.yaml"), []byte(
		"schema_version: \"1\"\nserver:\n  broker:\n    enabled: true\n    instances:\n      - key: remote-docker\n        name: example-remote\n        runtime_target:\n          type: docker\n          display_name: Remote Docker\n"), 0o644))
	origProber, origInstance := flatScopeProber, brokerRegisterInstance
	t.Cleanup(func() { flatScopeProber, brokerRegisterInstance = origProber, origInstance })
	flatScopeProber = fakeScopeProber("daemon-1")
	brokerRegisterCmd.SetContext(context.Background())
	return globalDir
}

func TestBrokerRegisterInstance_RegistersAndSavesInstanceCredentials(t *testing.T) {
	hub := newRegistrationHub(t)
	globalDir := setupRegisterInstanceTest(t, hub.URL)
	brokerRegisterInstance = "remote-docker"

	require.NoError(t, runBrokerRegister(brokerRegisterCmd, nil))

	id, err := brokeridentity.LoadOrCreate(brokeridentity.InstanceDir(globalDir, "remote-docker"), "remote-docker", "docker",
		brokeridentity.ExecutionScope{Type: "docker", Docker: &brokeridentity.DockerScope{DaemonID: "daemon-1"}}, nil)
	require.NoError(t, err)
	list, err := brokerregistration.LoadInstanceCredentials(globalDir, id)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, id.RuntimeBrokerID, list[0].BrokerID)
	_, err = os.Stat(filepath.Join(globalDir, "hub-credentials"))
	assert.True(t, errors.Is(err, os.ErrNotExist), "legacy credentials are not written")

	create := hub.bodies["/api/v1/brokers"]
	assert.Equal(t, id.RuntimeBrokerID, create["brokerId"])
	assert.Equal(t, "example-remote", create["name"])
	assert.Equal(t, map[string]any{"id": id.RuntimeTarget.ID, "type": "docker", "displayName": "Remote Docker"}, create["runtimeTarget"])
	join := hub.bodies["/api/v1/brokers/join"]
	assert.Contains(t, join["capabilities"], "asyncLaunch", "static capabilities are sent at registration")
	assert.NotContains(t, join, "profiles")
	assert.Equal(t, "Bearer "+joinTestDevToken, hub.auth["/api/v1/brokers"], "the registration uses the user credential")
}

func TestBrokerRegisterInstance_UnknownKeyRefused(t *testing.T) {
	hub := newRegistrationHub(t)
	setupRegisterInstanceTest(t, hub.URL)
	brokerRegisterInstance = "not-configured"

	err := runBrokerRegister(brokerRegisterCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no Runtime Broker instance with key "not-configured"`)
	assert.Empty(t, hub.bodies, "nothing is sent to the Hub")
}
