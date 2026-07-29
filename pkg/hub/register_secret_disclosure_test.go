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
	"encoding/base64"
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

// Regression tests for the broker HMAC secret that the deprecated by-name
// branch of handleProjectRegister used to put in its response body (#591).
//
// WHAT THIS FILE HOLDS CLOSED. A project register response is not a
// credential-issuing channel: the body goes to whoever made the request, and
// the request does not authenticate the broker being named. The branch used to
// return that broker's live HMAC secret to the caller, so the tests here are
// about the RESPONSE, not about any single caller being refused. The
// authorization gate in register_provider_authz_test.go is a different piece of
// work and covers a different hole; a caller the gate ALLOWS must still not be
// handed a broker credential, which is why every arm below is an allowed one.
//
// WHY THE MATRIX HAS THE SHAPE IT DOES. The emission sat below the branch's
// broker resolution, so it did not depend on which broker was named, on who
// asked, or on whether the project already existed. The arms therefore cross
// {user, agent} x {embedded broker, ordinary named broker} x {new project,
// pre-existing project} to record that none of those dimensions is what makes
// the response safe: the emission is gone, not narrowed. Arms that the
// authorization gate refuses before the branch runs are marked as such and are
// not counted as evidence about this removal.
//
// WHAT AN ARM ACTUALLY MEASURES. Absence of the "secretKey" field is the weak
// half. The decisive assertions are that the base64 of the SEEDED secret does
// not appear anywhere in the response body, and that the stored secret is
// afterwards byte-identical to what was seeded with exactly one active record —
// so neither disclosing the live key nor quietly minting a replacement passes.
//
// Test naming: everything file-local is prefixed rsd.

const (
	// Distinguishable so that a leak is identifiable in a body by inspection,
	// and so the read-back compares against a known value rather than against
	// whatever the code most recently wrote. Both are 32 bytes.
	rsdEmbeddedSecret   = "rsd-embedded-live-secret-32bytes"
	rsdStandaloneSecret = "rsd-standalone-live-secret-32byt"

	rsdRegisterPath = "/api/v1/projects/register"
)

type rsdFixture struct {
	srv   *Server
	store store.Store

	user *store.User

	// robot is an agent of agentHome; agentHome doubles as the agent's
	// pre-existing project.
	robot     *store.Agent
	agentHome *store.Project

	// userHome is the caller-owned pre-existing project.
	userHome *store.Project

	// Two brokers with different seeded secrets: the hub's embedded broker and
	// an ordinary one, because the branch resolves both by the same name lookup.
	embeddedID   string
	embeddedName string
	plainID      string
	plainName    string

	workspace string
}

func rsdSetup(t *testing.T) *rsdFixture {
	t.Helper()

	s, err := newTestStore(":memory:")
	if err != nil {
		t.Skipf("skipping: test store unavailable (%v)", err)
	}
	require.NoError(t, s.Migrate(context.Background()))

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	// Without a broker auth service the removed call could not have run at all,
	// so a fixture that omitted this would make every arm vacuous.
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-id")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	ctx := context.Background()
	f := &rsdFixture{srv: srv, store: s, workspace: t.TempDir()}

	f.user = &store.User{
		ID: tid("rsd-user"), Email: "rsd-user@example.com",
		DisplayName: "RSD User", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.user))

	f.userHome = &store.Project{
		ID: tid("rsd-user-home"), Name: "RSD User Home", Slug: tid("rsd-user-home"),
		OwnerID: f.user.ID, CreatedBy: f.user.ID,
		GitRemote: "https://example.invalid/rsd-user-home.git",
	}
	require.NoError(t, s.CreateProject(ctx, f.userHome))

	f.agentHome = &store.Project{
		ID: tid("rsd-agent-home"), Name: "RSD Agent Home", Slug: tid("rsd-agent-home"),
		OwnerID: f.user.ID, CreatedBy: f.user.ID,
		GitRemote: "https://example.invalid/rsd-agent-home.git",
	}
	require.NoError(t, s.CreateProject(ctx, f.agentHome))

	f.robot = &store.Agent{
		ID: tid("rsd-robot"), Slug: tid("rsd-robot"), Name: "rsd-robot",
		ProjectID: f.agentHome.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.user.ID, OwnerID: f.user.ID,
		Ancestry: []string{f.user.ID},
	}
	require.NoError(t, s.CreateAgent(ctx, f.robot))

	f.embeddedID, f.embeddedName = tid("rsd-embedded-broker"), "rsd-embedded"
	srv.SetEmbeddedBrokerID(f.embeddedID)
	f.plainID, f.plainName = tid("rsd-plain-broker"), "rsd-plain"

	for _, b := range []struct {
		id, name, secret string
	}{
		{f.embeddedID, f.embeddedName, rsdEmbeddedSecret},
		{f.plainID, f.plainName, rsdStandaloneSecret},
	} {
		require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
			ID: b.id, Name: b.name, Slug: b.name,
			Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
		}))
		// A broker that has already joined: it holds a live secret, which is
		// precisely what a register caller must not be able to read back out.
		require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
			BrokerID:  b.id,
			SecretKey: []byte(b.secret),
			Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
			Status:    store.BrokerSecretStatusActive,
		}))
	}

	return f
}

// ---------------------------------------------------------------------------
// Requests. As elsewhere, no identity is injected into a context: every request
// carries the credential the middleware would really have produced.
// ---------------------------------------------------------------------------

func rsdRequest(method, path string, body any) *http.Request {
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

func (f *rsdFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *rsdFixture) asUser(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(f.user.ID, f.user.Email, f.user.DisplayName,
		string(f.user.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := rsdRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	return f.serve(req)
}

func (f *rsdFixture) asAgent(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(f.robot.ID, f.robot.ProjectID, nil, nil)
	require.NoError(t, err)

	req := rsdRequest(method, path, body)
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

// byName drives the deprecated branch, which is where the emission lived. The
// brokerId branch never returned a secret and is covered by the existing
// two-phase test.
func rsdByName(projectID, projectName, brokerName, path string) RegisterProjectRequest {
	return RegisterProjectRequest{
		ID: projectID, Name: projectName,
		GitRemote: "https://example.invalid/" + projectID + ".git",
		Broker:    &RegisterProjectBrokerInfo{Name: brokerName, Version: "1.0.0"},
		Path:      path,
	}
}

// ---------------------------------------------------------------------------
// Observations.
// ---------------------------------------------------------------------------

// requireSecretIntact reads the stored secret back and compares it against the
// seed. Two distinct failures are separated deliberately: a changed byte string
// means the register path rotated a live broker's credential as a side effect,
// and a second active record means it minted an additional one. Neither is
// something registering a project may do.
func (f *rsdFixture) requireSecretIntact(t *testing.T, brokerID, want, because string) {
	t.Helper()
	ctx := context.Background()

	got, err := f.store.GetBrokerSecret(ctx, brokerID)
	require.NoError(t, err, "the seeded broker secret is gone after register (%s)", because)
	require.Equal(t, []byte(want), got.SecretKey,
		"registering a project changed a live broker's secret (%s)", because)

	active, err := f.store.GetActiveSecrets(ctx, brokerID)
	require.NoError(t, err)
	require.Len(t, active, 1,
		"registering a project minted an extra broker secret (%s)", because)
}

// requireNoSecretInBody is the assertion this file exists for. The field check
// is the weak half and is listed first only because it is the cheapest; the
// value checks are what actually close the disclosure, since a body that
// carried the key under any other name, or inside the broker object, would
// still be a handout.
func (f *rsdFixture) requireNoSecretInBody(t *testing.T, rec *httptest.ResponseRecorder, because string) {
	t.Helper()
	raw := rec.Body.String()

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		v, present := decoded["secretKey"]
		require.False(t, present && string(v) != `""` && string(v) != "null",
			"the register response carried a secretKey field (%s): %s", because, raw)
	}

	for _, secret := range []string{rsdEmbeddedSecret, rsdStandaloneSecret} {
		require.NotContains(t, raw, base64.StdEncoding.EncodeToString([]byte(secret)),
			"the register response disclosed a live broker HMAC secret (%s)", because)
		require.NotContains(t, raw, secret,
			"the register response disclosed a live broker HMAC secret in the "+
				"clear (%s)", because)
	}
}

// ---------------------------------------------------------------------------
// The matrix.
// ---------------------------------------------------------------------------

// TestRSD_RegisterNeverReturnsBrokerSecret crosses identity, broker and project
// existence. Every arm here is a caller the authorization gate ALLOWS through
// to the deprecated branch, which is the point: this is not a gate test, and a
// refusal would make an arm prove nothing about the response. Each allowed arm
// asserts that the branch really ran, by requiring the provider row it writes —
// without that, "no secret in the body" would also be satisfied by a request
// that never got there.
func TestRSD_RegisterNeverReturnsBrokerSecret(t *testing.T) {
	type arm struct {
		name       string
		agent      bool
		brokerName func(*rsdFixture) string
		brokerID   func(*rsdFixture) string
		// project returns the id and name to register. A project already in the
		// store registers with created=false, a fresh id with created=true.
		project func(*rsdFixture) (string, string)
		// want is the measured status, and reached says whether the deprecated
		// branch ran. They are separate because one combination is refused
		// upstream: see the note on the agent arms below.
		want    int
		reached bool
	}

	brokers := []struct {
		name string
		bn   func(*rsdFixture) string
		bid  func(*rsdFixture) string
	}{
		{"embedded broker",
			func(f *rsdFixture) string { return f.embeddedName },
			func(f *rsdFixture) string { return f.embeddedID }},
		{"ordinary named broker",
			func(f *rsdFixture) string { return f.plainName },
			func(f *rsdFixture) string { return f.plainID }},
	}

	var arms []arm
	for _, b := range brokers {
		b := b
		arms = append(arms,
			arm{"user/new project/" + b.name, false, b.bn, b.bid,
				func(f *rsdFixture) (string, string) {
					return tid("rsd-user-new"), "RSD User New"
				}, http.StatusOK, true},
			arm{"user/pre-existing project/" + b.name, false, b.bn, b.bid,
				func(f *rsdFixture) (string, string) {
					return f.userHome.ID, f.userHome.Name
				}, http.StatusOK, true},
			arm{"agent/new project/" + b.name, true, b.bn, b.bid,
				func(f *rsdFixture) (string, string) {
					return tid("rsd-agent-new"), "RSD Agent New"
				}, http.StatusOK, true},
			// MEASURED 403, and it is the register provider gate refusing it,
			// not this removal: an agent has read-class access to its own
			// project but not update, so it cannot rewrite that project's
			// providers. The arm is kept because a refused caller must not be
			// handed a secret either, but it is NOT evidence about the response
			// the branch produces — the agent-identity reach that was measured
			// against the disclosure is the created=true arm above, where the
			// gate does not fire because the caller is creating their own
			// project. If this ever starts returning 200, the gate changed and
			// this arm silently becomes a second real one; the reached flag is
			// what keeps that from passing unnoticed.
			arm{"agent/pre-existing project/" + b.name, true, b.bn, b.bid,
				func(f *rsdFixture) (string, string) {
					return f.agentHome.ID, f.agentHome.Name
				}, http.StatusForbidden, false},
		)
	}

	for _, a := range arms {
		a := a
		t.Run(a.name, func(t *testing.T) {
			f := rsdSetup(t)
			id, name := a.project(f)
			body := rsdByName(id, name, a.brokerName(f), f.workspace)

			var rec *httptest.ResponseRecorder
			if a.agent {
				rec = f.asAgent(t, http.MethodPost, rsdRegisterPath, body)
			} else {
				rec = f.asUser(t, http.MethodPost, rsdRegisterPath, body)
			}

			// Body first: it is the thing that leaves the process.
			f.requireNoSecretInBody(t, rec, a.name)
			f.requireSecretIntact(t, f.embeddedID, rsdEmbeddedSecret, a.name)
			f.requireSecretIntact(t, f.plainID, rsdStandaloneSecret, a.name)

			require.Equal(t, a.want, rec.Code, "body=%s", rec.Body.String())

			_, err := f.store.GetProjectProvider(context.Background(), id, a.brokerID(f))
			if a.reached {
				require.NoError(t, err,
					"the deprecated branch did not run to completion, so this arm "+
						"proves nothing about the secret it used to return")
			} else {
				require.Error(t, err,
					"an arm recorded as refused before the branch wrote a provider "+
						"row, so it is no longer measuring what its comment says")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Controls. Removing an emission is trivially achievable by breaking the route,
// so what registration is FOR has to stay working, and the supported way for a
// broker to get a secret has to keep working too.
// ---------------------------------------------------------------------------

// TestRSD_RegistrationStillLinksTheBroker records that the deprecated branch
// still does its job: the project exists afterwards, the response names the
// broker, and the provider row is written. Only the credential is gone.
func TestRSD_RegistrationStillLinksTheBroker(t *testing.T) {
	f := rsdSetup(t)
	id := tid("rsd-control-new")

	rec := f.asUser(t, http.MethodPost, rsdRegisterPath,
		rsdByName(id, "RSD Control New", f.plainName, f.workspace))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp RegisterProjectResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Created, "the register did not create the project")
	require.NotNil(t, resp.Broker, "the register no longer reports the linked broker")
	require.Equal(t, f.plainID, resp.Broker.ID, "the register linked the wrong broker")
	require.Empty(t, resp.SecretKey, "the deprecated response field was populated again")

	got, err := f.store.GetProjectProvider(context.Background(), id, f.plainID)
	require.NoError(t, err, "the register no longer attaches the provider")
	require.Equal(t, f.workspace, got.LocalPath)
}

// TestRSD_BrokerJoinStillIssuesASecret is the other half of the control, and
// the reason the fix is a call-site removal rather than a change to the
// secret-issuing function: that function is untouched, and the supported,
// broker-authenticated path to a secret still works. If this ever fails, the
// fix went too far.
func TestRSD_BrokerJoinStillIssuesASecret(t *testing.T) {
	f := rsdSetup(t)

	rec := f.asUser(t, http.MethodPost, "/api/v1/brokers",
		map[string]any{"name": "rsd-joining-broker"})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	var created CreateBrokerRegistrationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.JoinToken)

	// The join token is the credential here, so this phase is unauthenticated.
	joined := f.serve(rsdRequest(http.MethodPost, "/api/v1/brokers/join", map[string]any{
		"brokerId":  created.BrokerID,
		"joinToken": created.JoinToken,
		"hostname":  "rsd-machine",
		"version":   "1.0.0",
	}))
	require.Equal(t, http.StatusOK, joined.Code, "body=%s", joined.Body.String())

	var joinResp BrokerJoinResponse
	require.NoError(t, json.Unmarshal(joined.Body.Bytes(), &joinResp))
	require.NotEmpty(t, joinResp.SecretKey,
		"the supported broker-authenticated secret path stopped issuing secrets")
	decoded, err := base64.StdEncoding.DecodeString(joinResp.SecretKey)
	require.NoError(t, err)
	require.Len(t, decoded, 32)
}
