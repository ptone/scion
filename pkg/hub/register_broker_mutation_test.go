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

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// Regression tests for the broker RECORD overwrite in the deprecated by-name
// branch of handleProjectRegister (#107).
//
// THE PRIMITIVE. The branch resolves a broker from caller-supplied lookup keys
// and then used to assign Name, Slug, Version, Status, ConnectionState,
// Capabilities and Profiles from the request and persist them. Resolution is
// ID-first, name-second, so the record on the receiving end of that write is
// whichever broker the caller managed to name — including one registered by
// somebody else. Matching a record is not consent to rewrite it.
//
// WHY THE ARMS REGISTER A NEW PROJECT. The register provider gate (3c728f93)
// refuses a caller who does not own a PRE-EXISTING project, so an attack routed
// through someone else's project would be stopped before the branch and would
// measure that gate instead of this one. Creating your own project is a caller's
// own business and the gate does not fire — which is exactly why the broker
// record needed its own fix and could not be left to the project gate: they
// guard different objects.
//
// WHAT IS AND IS NOT CLOSED HERE. This is the broker record, and the read of it
// that skipping the write would otherwise expose. Two other persists on the
// same branch are out of scope and untouched: AddProjectProvider is an upsert
// on (project_id, broker_id) whose authorization is the register provider gate
// and whose LocalPath validation is separate work, and the DefaultRuntimeBrokerID
// assignment mutates the project rather than the broker.
//
// Test naming: everything file-local is prefixed rbm.

const rbmRegisterPath = "/api/v1/projects/register"

type rbmFixture struct {
	srv   *Server
	store store.Store

	intruder *store.User
	robot    *store.Agent
	home     *store.Project

	// victim is a broker registered by someone else, holding metadata that is
	// distinguishable from anything an attack request supplies.
	victimID   string
	victimName string

	workspace string
}

// rbmVictimSeed is the stored state every attack arm must leave untouched. The
// values are deliberately the opposite of what the attack sends: offline vs
// online, a low version vs a high one, a profile naming infrastructure that a
// stranger should not be able to read back either.
func rbmVictimSeed(id, name string) *store.RuntimeBroker {
	return &store.RuntimeBroker{
		ID: id, Name: name, Slug: name,
		Version:         "1.2.3",
		Status:          store.BrokerStatusOffline,
		ConnectionState: "disconnected",
		Capabilities:    &store.BrokerCapabilities{WebPTY: true, Sync: true, Attach: false},
		Profiles: []store.BrokerProfile{{
			Name: "prod-k8s", Type: "kubernetes", Available: true,
			Context: "prod-cluster", Namespace: "payments",
		}},
		Created: time.Now().Add(-time.Hour), Updated: time.Now().Add(-time.Hour),
	}
}

func rbmSetup(t *testing.T) *rbmFixture {
	t.Helper()

	s, err := newTestStore(":memory:")
	if err != nil {
		t.Skipf("skipping: test store unavailable (%v)", err)
	}
	require.NoError(t, s.Migrate(context.Background()))

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-id")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	ctx := context.Background()
	f := &rbmFixture{srv: srv, store: s, workspace: t.TempDir()}

	f.intruder = &store.User{
		ID: tid("rbm-intruder"), Email: "rbm-intruder@example.com",
		DisplayName: "RBM Intruder", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.intruder))

	f.home = &store.Project{
		ID: tid("rbm-home"), Name: "RBM Home", Slug: tid("rbm-home"),
		OwnerID: f.intruder.ID, CreatedBy: f.intruder.ID,
		GitRemote: "https://example.invalid/rbm-home.git",
	}
	require.NoError(t, s.CreateProject(ctx, f.home))

	f.robot = &store.Agent{
		ID: tid("rbm-robot"), Slug: tid("rbm-robot"), Name: "rbm-robot",
		ProjectID: f.home.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.intruder.ID, OwnerID: f.intruder.ID,
		Ancestry: []string{f.intruder.ID},
	}
	require.NoError(t, s.CreateAgent(ctx, f.robot))

	f.victimID, f.victimName = tid("rbm-victim-broker"), "rbm-victim"
	require.NoError(t, s.CreateRuntimeBroker(ctx, rbmVictimSeed(f.victimID, f.victimName)))

	return f
}

// ---------------------------------------------------------------------------
// Requests.
// ---------------------------------------------------------------------------

func rbmRequest(method, path string, body any) *http.Request {
	var rdr io.Reader = bytes.NewReader(nil)
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (f *rbmFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *rbmFixture) asUser(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(f.intruder.ID, f.intruder.Email,
		f.intruder.DisplayName, string(f.intruder.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := rbmRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	return f.serve(req)
}

func (f *rbmFixture) asAgent(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(f.robot.ID, f.robot.ProjectID, nil, nil)
	require.NoError(t, err)

	req := rbmRequest(method, path, body)
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

// rbmHostileBroker is what an attack sends: every writable field set to
// something the victim's record does not say.
func rbmHostileBroker(id, name string) *RegisterProjectBrokerInfo {
	return &RegisterProjectBrokerInfo{
		ID: id, Name: name, Version: "666.6.6",
		Capabilities: &store.BrokerCapabilities{WebPTY: false, Sync: false, Attach: true},
		Profiles: []store.BrokerProfile{{
			Name: "attacker-profile", Type: "docker", Available: true,
		}},
	}
}

// ---------------------------------------------------------------------------
// Observations.
// ---------------------------------------------------------------------------

func (f *rbmFixture) victim(t *testing.T) *store.RuntimeBroker {
	t.Helper()
	got, err := f.store.GetRuntimeBroker(context.Background(), f.victimID)
	require.NoError(t, err, "the victim broker record is gone")
	return got
}

// requireVictimUnchanged compares field by field rather than comparing whole
// structs, so a failure names the field that moved instead of printing two
// records and leaving the reader to diff them. Updated is checked last and for
// a different reason: the seven fields say the values survived, Updated says no
// write was attempted at all.
func (f *rbmFixture) requireVictimUnchanged(t *testing.T, before *store.RuntimeBroker, because string) {
	t.Helper()
	after := f.victim(t)

	require.Equal(t, before.Name, after.Name,
		"register renamed a broker record the caller does not own (%s)", because)
	require.Equal(t, before.Slug, after.Slug,
		"register rewrote a non-owned broker's slug (%s)", because)
	require.Equal(t, before.Version, after.Version,
		"register rewrote a non-owned broker's version (%s)", because)
	require.Equal(t, before.Status, after.Status,
		"register asserted a status for a broker it did not hear from (%s)", because)
	require.Equal(t, before.ConnectionState, after.ConnectionState,
		"register asserted a connection state for a broker it did not hear from (%s)", because)
	require.Equal(t, before.Capabilities, after.Capabilities,
		"register rewrote a non-owned broker's capabilities (%s)", because)
	require.Equal(t, before.Profiles, after.Profiles,
		"register rewrote a non-owned broker's profiles (%s)", because)
	require.Equal(t, before.Updated, after.Updated,
		"the broker record was persisted at all, so something on this branch "+
			"still writes to it (%s)", because)
}

// requireBrokerEchoIsProjection pins the read half. Skipping the overwrite means
// the echoed record would otherwise come back as STORED, so what a stranger can
// learn by naming a broker has to be checked, not assumed.
func (f *rbmFixture) requireBrokerEchoIsProjection(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	var resp RegisterProjectResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Broker, "the response no longer identifies the linked broker")

	require.Equal(t, f.victimID, resp.Broker.ID, "the response identified the wrong broker")
	require.Equal(t, f.victimName, resp.Broker.Name,
		"the response must still carry the broker name, which hubsync prints")

	require.Empty(t, resp.Broker.Version, "the response disclosed the stored version")
	require.Empty(t, resp.Broker.Status, "the response disclosed the stored status")
	require.Empty(t, resp.Broker.ConnectionState,
		"the response disclosed the stored connection state")
	require.Empty(t, resp.Broker.Slug, "the response disclosed the stored slug")
	require.Nil(t, resp.Broker.Capabilities, "the response disclosed the stored capabilities")
	require.Empty(t, resp.Broker.Profiles, "the response disclosed the stored profiles")

	// The profile fields are the ones worth naming: they describe someone
	// else's infrastructure, and a name lookup is not a reason to learn them.
	body := rec.Body.String()
	require.NotContains(t, body, "prod-cluster", "the response disclosed a stored k8s context")
	require.NotContains(t, body, "payments", "the response disclosed a stored k8s namespace")
}

// ---------------------------------------------------------------------------
// The attack.
// ---------------------------------------------------------------------------

// TestRBM_NonOwnedBrokerRecordNotOverwritten drives both lookup keys with both
// identity kinds. The by-ID arms exist because the skip is unconditional on
// there being an existing record: a skip written only for the name lookup would
// leave this whole primitive reachable by supplying the target's broker ID, and
// these arms are what would notice that.
func TestRBM_NonOwnedBrokerRecordNotOverwritten(t *testing.T) {
	for _, lookup := range []struct {
		name  string
		build func(*rbmFixture) *RegisterProjectBrokerInfo
	}{
		{"by name", func(f *rbmFixture) *RegisterProjectBrokerInfo {
			return rbmHostileBroker("", f.victimName)
		}},
		{"by id", func(f *rbmFixture) *RegisterProjectBrokerInfo {
			return rbmHostileBroker(f.victimID, "rbm-attacker-renamed")
		}},
	} {
		for _, caller := range []struct {
			name string
			do   func(*testing.T, *rbmFixture, any) *httptest.ResponseRecorder
		}{
			{"unrelated user", func(t *testing.T, f *rbmFixture, body any) *httptest.ResponseRecorder {
				return f.asUser(t, http.MethodPost, rbmRegisterPath, body)
			}},
			{"agent", func(t *testing.T, f *rbmFixture, body any) *httptest.ResponseRecorder {
				return f.asAgent(t, http.MethodPost, rbmRegisterPath, body)
			}},
		} {
			t.Run(lookup.name+"/"+caller.name, func(t *testing.T) {
				f := rbmSetup(t)
				before := f.victim(t)
				newProject := tid("rbm-own-project")

				rec := caller.do(t, f, RegisterProjectRequest{
					ID: newProject, Name: "RBM Own Project",
					GitRemote: "https://example.invalid/" + newProject + ".git",
					Broker:    lookup.build(f), Path: f.workspace,
				})

				// Store first: the record is the thing being protected, and a
				// status change reads very differently from a mutation.
				f.requireVictimUnchanged(t, before, lookup.name+"/"+caller.name)
				f.requireBrokerEchoIsProjection(t, rec)

				require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
				_, err := f.store.GetProjectProvider(context.Background(), newProject, f.victimID)
				require.NoError(t, err,
					"the branch did not run to completion, so this arm proves "+
						"nothing about the overwrite it used to perform")
			})
		}
	}
}

// TestRBM_LinkingIsStillRecorded records what the caller legitimately gets out
// of the arms above: they may link the broker to their own project, which is
// what registering is for. Only the record and its contents are off limits.
func TestRBM_LinkingIsStillRecorded(t *testing.T) {
	f := rbmSetup(t)
	newProject := tid("rbm-link-project")

	rec := f.asUser(t, http.MethodPost, rbmRegisterPath, RegisterProjectRequest{
		ID: newProject, Name: "RBM Link Project",
		GitRemote: "https://example.invalid/rbm-link.git",
		Broker:    &RegisterProjectBrokerInfo{Name: f.victimName}, Path: f.workspace,
	})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, err := f.store.GetProjectProvider(context.Background(), newProject, f.victimID)
	require.NoError(t, err, "registering no longer links the named broker")
	require.Equal(t, f.workspace, got.LocalPath)

	project, err := f.store.GetProject(context.Background(), newProject)
	require.NoError(t, err)
	require.Equal(t, f.victimID, project.DefaultRuntimeBrokerID,
		"the first linked broker is no longer recorded as the project default")
}

// ---------------------------------------------------------------------------
// Controls.
// ---------------------------------------------------------------------------

// TestRBM_NewBrokerStillGetsItsMetadata is the boundary of the skip. A record
// this request creates from its own input is not somebody else's record, and
// registering must still be able to describe it — otherwise the fix would have
// broken first-time broker registration rather than closed an overwrite.
func TestRBM_NewBrokerStillGetsItsMetadata(t *testing.T) {
	f := rbmSetup(t)
	newProject := tid("rbm-fresh-project")

	rec := f.asUser(t, http.MethodPost, rbmRegisterPath, RegisterProjectRequest{
		ID: newProject, Name: "RBM Fresh Project",
		GitRemote: "https://example.invalid/rbm-fresh.git",
		Broker:    rbmHostileBroker("", "rbm-brand-new-broker"),
		Path:      f.workspace,
	})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp RegisterProjectResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Broker)

	created, err := f.store.GetRuntimeBroker(context.Background(), resp.Broker.ID)
	require.NoError(t, err, "registering no longer creates the named broker")
	require.Equal(t, "rbm-brand-new-broker", created.Name)
	require.Equal(t, "666.6.6", created.Version, "a new broker's version was not recorded")
	require.Equal(t, store.BrokerStatusOnline, created.Status)
	require.Equal(t, "connected", created.ConnectionState)
	require.Equal(t, &store.BrokerCapabilities{Attach: true}, created.Capabilities)
	require.Len(t, created.Profiles, 1)

	// A record built entirely from this request may be echoed in full: it tells
	// the caller nothing they did not just supply.
	require.Equal(t, "666.6.6", resp.Broker.Version,
		"a newly created broker is no longer echoed in full")
}

// TestRBM_JoinStillRefreshesLiveness backs the claim that dropping the
// Status/ConnectionState/Version assignment strands nobody offline. The
// supported path is the one that authenticates the broker, and it still writes
// all three — which is the reason register did not need to.
func TestRBM_JoinStillRefreshesLiveness(t *testing.T) {
	f := rbmSetup(t)

	rec := f.asUser(t, http.MethodPost, "/api/v1/brokers",
		map[string]any{"name": "rbm-joining-broker"})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	var created CreateBrokerRegistrationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	joined := f.serve(rbmRequest(http.MethodPost, "/api/v1/brokers/join", map[string]any{
		"brokerId":  created.BrokerID,
		"joinToken": created.JoinToken,
		"hostname":  "rbm-machine",
		"version":   "9.9.9",
	}))
	require.Equal(t, http.StatusOK, joined.Code, "body=%s", joined.Body.String())

	got, err := f.store.GetRuntimeBroker(context.Background(), created.BrokerID)
	require.NoError(t, err)
	require.Equal(t, "9.9.9", got.Version, "the supported path no longer refreshes version")
	require.Equal(t, store.BrokerStatusOnline, got.Status,
		"the supported path no longer brings a broker online")
	require.Equal(t, "connected", got.ConnectionState,
		"the supported path no longer records the connection state")
	require.False(t, got.LastHeartbeat.IsZero(), "the supported path no longer heartbeats")
}
