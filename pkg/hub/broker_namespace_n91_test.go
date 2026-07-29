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
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// Regression tests for N91: the DEPRECATED embedded broker-registration flow in
// handleProjectRegister (handlers_projects_core.go) mints a RuntimeBroker whose
// ID is taken from req.Broker.ID VERBATIM and passed to CreateRuntimeBroker with
// no format check and no cross-namespace check.
//
// This is the same class as F3b in brokerauth.go: a caller chooses the ID of the
// principal it creates, so it can mint a broker whose ID() collides with an
// existing user's or agent's UUID. Any authorization site that compares an ID to
// identity.ID() then resolves the broker to that other-type principal. The two
// mint sites (register's embedded flow and brokerauth's broker registration)
// must not disagree, so the fix is a SINGLE SHARED validation function both call
// — not two hand-rolled guards.
//
// SEQUENCING (aid-em/lead ruling 12:51Z): these pins are shape-independent and
// are built first. They assert store invariants (no broker row minted at a
// malformed or cross-namespace ID) and a legitimate registration still
// succeeding — not any particular error code — so they hold regardless of the
// helper's exact refusal shape. Until dev6 exposes the F3b guard as a reusable
// package-level function and it is wired at the two embedded-flow sites, the
// three refusal arms are RED (today the malformed/colliding broker is accepted
// and persisted). Do not push this file without the wiring that turns them green.
//
// Test naming: everything file-local is prefixed bn.

const bnRegisterPath = "/api/v1/projects/register"

type bnFixture struct {
	srv   *Server
	store store.Store

	caller *store.User // registers a NEW project, so it owns the result (no gate)

	victimUser  *store.User    // a user whose UUID a hostile brokerID can collide with
	victimAgent *store.Agent   // an agent whose UUID a hostile brokerID can collide with
	otherProj   *store.Project // home for victimAgent

	workspace string
}

func bnSetup(t *testing.T) *bnFixture {
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
	f := &bnFixture{srv: srv, store: s, workspace: t.TempDir()}

	f.caller = &store.User{
		ID: tid("bn-caller"), Email: "bn-caller@example.com",
		DisplayName: "BN Caller", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.caller))

	f.victimUser = &store.User{
		ID: tid("bn-victim-user"), Email: "bn-victim-user@example.com",
		DisplayName: "BN Victim User", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.victimUser))

	f.otherProj = &store.Project{
		ID: tid("bn-other-proj"), Name: "BN Other", Slug: tid("bn-other-proj"),
		OwnerID: f.victimUser.ID, CreatedBy: f.victimUser.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.otherProj))

	f.victimAgent = &store.Agent{
		ID: tid("bn-victim-agent"), Slug: tid("bn-victim-agent"), Name: "bn-victim-agent",
		ProjectID: f.otherProj.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.victimUser.ID, OwnerID: f.victimUser.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, f.victimAgent))

	return f
}

func bnRequest(method, path string, body any) *http.Request {
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

func (f *bnFixture) asCaller(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(f.caller.ID, f.caller.Email,
		f.caller.DisplayName, string(f.caller.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := bnRequest(http.MethodPost, bnRegisterPath, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// register registers a fresh project owned by the caller (so the register
// provider gate does not fire — it only fires on a pre-existing project the
// caller does not own) with an embedded broker carrying the given ID.
func (f *bnFixture) register(t *testing.T, brokerID, brokerName string) *httptest.ResponseRecorder {
	t.Helper()
	newProject := tid("bn-own-" + brokerName)
	return f.asCaller(t, RegisterProjectRequest{
		ID:        newProject,
		Name:      "BN Own " + brokerName,
		GitRemote: "https://example.invalid/bn-" + brokerName + ".git",
		Broker:    &RegisterProjectBrokerInfo{ID: brokerID, Name: brokerName},
		Path:      f.workspace,
	})
}

// requireNoBrokerRow is the load-bearing, shape-independent invariant: whatever
// the refusal status code, the embedded flow must not have minted a broker whose
// ID is the malformed or cross-namespace value the request supplied.
func (f *bnFixture) requireNoBrokerRow(t *testing.T, brokerID, because string) {
	t.Helper()
	_, err := f.store.GetRuntimeBroker(context.Background(), brokerID)
	require.ErrorIs(t, err, store.ErrNotFound,
		"a broker row was persisted at a %s id — the embedded mint took req.Broker.ID verbatim", because)
}

// requireRefused asserts a client-error refusal (4xx), not a 200 and not a 5xx
// crash. The exact code is the helper's business; the test pins that the request
// was rejected, cleanly, as a bad request rather than accepted or 500'd.
func requireRefused(t *testing.T, rec *httptest.ResponseRecorder, because string) {
	t.Helper()
	require.GreaterOrEqual(t, rec.Code, 400, "%s: expected a client-error refusal, got %d: %s", because, rec.Code, rec.Body.String())
	require.Less(t, rec.Code, 500, "%s: expected a clean 4xx refusal, not a 5xx, got %d: %s", because, rec.Code, rec.Body.String())
}

// TestBN_MalformedBrokerIDRejected: a plainly non-UUID brokerId must be refused
// and must not be persisted. This arm is a defense-in-depth control: the ent
// store already refuses a non-parseable id one layer down, so it holds both
// before and after the shared guard (it is not the attributable arm).
func TestBN_MalformedBrokerIDRejected(t *testing.T) {
	f := bnSetup(t)
	const malformed = "not-a-valid-uuid"

	rec := f.register(t, malformed, "bn-malformed")

	f.requireNoBrokerRow(t, malformed, "malformed")
	requireRefused(t, rec, "malformed brokerId")
}

// TestBN_NonCanonicalBrokerIDRejected is the attributable format arm. An
// uppercase UUID is parseable but not in canonical lowercase-hyphenated form:
// the ent store would silently canonicalise and ACCEPT it (persisting a row
// under the normalised spelling), so only the shared validateNewBrokerID guard
// refuses it. RED before the guard is wired, GREEN after. Absence is asserted
// under the NORMALISED (lowercase) spelling because the store canonicalises on
// write.
func TestBN_NonCanonicalBrokerIDRejected(t *testing.T) {
	f := bnSetup(t)
	canonical := tid("bn-noncanonical") // canonical lowercase uuid, collides with nothing
	upper := strings.ToUpper(canonical) // same uuid, non-canonical spelling

	rec := f.register(t, upper, "bn-noncanonical")

	f.requireNoBrokerRow(t, canonical, "non-canonical (normalised)")
	f.requireNoBrokerRow(t, upper, "non-canonical (as sent)")
	requireRefused(t, rec, "non-canonical brokerId")
}

// TestBN_BrokerIDCollidingWithUserRejected: a well-formed brokerId that already
// names a user is a cross-namespace collision and must be refused; no broker row
// may be minted at the user's UUID.
func TestBN_BrokerIDCollidingWithUserRejected(t *testing.T) {
	f := bnSetup(t)

	rec := f.register(t, f.victimUser.ID, "bn-collide-user")

	f.requireNoBrokerRow(t, f.victimUser.ID, "user-colliding")
	requireRefused(t, rec, "brokerId colliding with an existing user")

	// The user record itself must be intact.
	u, err := f.store.GetUser(context.Background(), f.victimUser.ID)
	require.NoError(t, err, "the colliding user record must still exist")
	require.Equal(t, f.victimUser.Email, u.Email)
}

// TestBN_BrokerIDCollidingWithAgentRejected: same, for an agent's UUID.
func TestBN_BrokerIDCollidingWithAgentRejected(t *testing.T) {
	f := bnSetup(t)

	rec := f.register(t, f.victimAgent.ID, "bn-collide-agent")

	f.requireNoBrokerRow(t, f.victimAgent.ID, "agent-colliding")
	requireRefused(t, rec, "brokerId colliding with an existing agent")

	a, err := f.store.GetAgent(context.Background(), f.victimAgent.ID)
	require.NoError(t, err, "the colliding agent record must still exist")
	require.Equal(t, f.victimAgent.Name, a.Name)
}

// TestBN_LegitimateEmbeddedRegistrationStillSucceeds is the boundary/positive
// control: a registration that supplies no brokerId (server generates a fresh
// UUID) must still create the broker and link it. This arm is GREEN today and
// must stay GREEN after the guard — a guard that broke it would be an outage.
func TestBN_LegitimateEmbeddedRegistrationStillSucceeds(t *testing.T) {
	f := bnSetup(t)

	rec := f.register(t, "", "bn-legit-broker")
	require.Equal(t, http.StatusOK, rec.Code, "a legitimate embedded registration must still succeed; body=%s", rec.Body.String())

	var resp RegisterProjectResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Broker, "the response must identify the created broker")
	require.NotEmpty(t, resp.Broker.ID, "the created broker must have a server-generated id")

	created, err := f.store.GetRuntimeBroker(context.Background(), resp.Broker.ID)
	require.NoError(t, err, "the legitimate embedded broker must be persisted")
	require.Equal(t, "bn-legit-broker", created.Name)
}
