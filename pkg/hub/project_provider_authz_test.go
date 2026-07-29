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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Regression tests for the provider mutation gate on
// /api/v1/projects/{id}/providers.
//
// Until this gate landed, both mutating verbs were reachable by any
// authenticated caller. Independently measured: an unrelated user, an agent
// belonging to a different project, and a runtime broker each received 201
// attaching a provider to a project they had no relationship to. Only anonymous
// requests were stopped, and that was the authentication middleware rather than
// any authorization decision.
//
// A provider is not an inert label. It is the record that says which broker
// serves the project and, through LocalPath, which directory on that broker is
// the project's workspace — so the ungated endpoint let an outsider decide what
// a victim project's workspace IS. store.AddProjectProvider is an upsert keyed
// on (projectID, brokerID), which makes the second POST the dangerous one: it
// does not fail as a duplicate, it silently rewrites the existing provider. That
// is why the overwrite case below asserts the stored LocalPath rather than the
// status code, and it is the assertion this file exists for.
//
// SCOPE. These tests cover WHO may mutate providers. They say nothing about
// WHERE LocalPath may point: an authorized owner can still name any existing
// directory outside the restricted system prefixes. Constraining that is a
// separate question, deliberately not answered here.
//
// Test naming: everything file-local is prefixed pvGate.

type pvGateFixture struct {
	srv   *Server
	store store.Store

	owner  *store.User
	outsdr *store.User
	admin  *store.User

	projA *store.Project
	projB *store.Project

	// insider belongs to projA, stranger to projB.
	insider  *store.Agent
	stranger *store.Agent

	// attachable is the broker a caller tries to attach as a provider.
	attachable *store.RuntimeBroker

	// caller is a second broker, used as an authenticating identity rather than
	// as the subject of the request.
	caller       *store.RuntimeBroker
	brokerSecret []byte

	// ownedPath is the LocalPath of the provider projA legitimately has;
	// intruderPath is what an attacker tries to replace it with.
	ownedPath    string
	intruderPath string
}

func pvGateSetup(t *testing.T) *pvGateFixture {
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
	f := &pvGateFixture{srv: srv, store: s}

	f.owner = &store.User{
		ID: tid("pvgate-owner"), Email: "pvgate-owner@example.com",
		DisplayName: "Owner", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.owner))

	f.outsdr = &store.User{
		ID: tid("pvgate-outsider"), Email: "pvgate-outsider@example.com",
		DisplayName: "Outsider", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.outsdr))

	f.admin = &store.User{
		ID: tid("pvgate-admin"), Email: "pvgate-admin@example.com",
		DisplayName: "Admin", Role: store.UserRoleAdmin, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.admin))

	f.projA = &store.Project{
		ID: tid("pvgate-pa"), Name: "PV Gate A", Slug: tid("pvgate-pa"),
		OwnerID: f.owner.ID, CreatedBy: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projA))

	f.projB = &store.Project{
		ID: tid("pvgate-pb"), Name: "PV Gate B", Slug: tid("pvgate-pb"),
		OwnerID: f.owner.ID, CreatedBy: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projB))

	mk := func(name, projectID string) *store.Agent {
		a := &store.Agent{
			ID: tid(name), Slug: tid(name), Name: name,
			ProjectID: projectID, Phase: string(state.PhaseRunning),
			CreatedBy: f.owner.ID, OwnerID: f.owner.ID,
			Ancestry: []string{f.owner.ID},
		}
		require.NoError(t, s.CreateAgent(ctx, a))
		return a
	}
	f.insider = mk("pvgate-insider", f.projA.ID)
	f.stranger = mk("pvgate-stranger", f.projB.ID)

	f.attachable = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "pvgate-attachable", Slug: "pvgate-attachable",
		Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, f.attachable))

	f.brokerSecret = []byte("pvgate-secret-key-32-bytes!!!!!!")
	f.caller = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "pvgate-caller", Slug: "pvgate-caller",
		Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, f.caller))
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  f.caller.ID,
		SecretKey: f.brokerSecret,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))

	// LocalPath is validated as an existing absolute directory, so both must
	// really exist or the request would fail validation before reaching the
	// store and the test would prove nothing about the gate.
	root := t.TempDir()
	f.ownedPath = filepath.Join(root, "owned")
	f.intruderPath = filepath.Join(root, "intruder")
	require.NoError(t, os.MkdirAll(f.ownedPath, 0o755))
	require.NoError(t, os.MkdirAll(f.intruderPath, 0o755))

	return f
}

// ---------------------------------------------------------------------------
// Requests.
// ---------------------------------------------------------------------------

func (f *pvGateFixture) newRequest(method, path string, body any) *http.Request {
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

func (f *pvGateFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *pvGateFixture) asUser(t *testing.T, u *store.User, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName,
		string(u.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := f.newRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	return f.serve(req)
}

func (f *pvGateFixture) asAgent(t *testing.T, a *store.Agent, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(a.ID, a.ProjectID, nil, nil)
	require.NoError(t, err)

	req := f.newRequest(method, path, body)
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

// asBroker signs the request the way a real runtime broker would. Note that the
// broker HMAC covers the method, path, query, timestamp and nonce but NOT the
// body, so signing here is about establishing the identity, not the payload.
func (f *pvGateFixture) asBroker(t *testing.T, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	req := f.newRequest(method, path, body)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "pvgate-nonce-" + uuid.New().String()
	req.Header.Set(HeaderBrokerID, f.caller.ID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)

	svc := f.srv.brokerAuthService
	require.NotNil(t, svc, "broker auth service must be configured")
	mac := hmac.New(sha256.New, f.brokerSecret)
	mac.Write(svc.buildCanonicalString(req, timestamp, nonce))
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	return f.serve(req)
}

func (f *pvGateFixture) anonymous(method, path string, body any) *httptest.ResponseRecorder {
	return f.serve(f.newRequest(method, path, body))
}

// ---------------------------------------------------------------------------
// Paths and store observations.
// ---------------------------------------------------------------------------

func (f *pvGateFixture) collectionPath() string {
	return "/api/v1/projects/" + f.projA.ID + "/providers"
}

func (f *pvGateFixture) resourcePath() string {
	return f.collectionPath() + "/" + f.attachable.ID
}

func (f *pvGateFixture) attachBody(localPath string) AddProviderRequest {
	return AddProviderRequest{BrokerID: f.attachable.ID, LocalPath: localPath}
}

// seedProvider gives projA the provider a legitimate owner would have created,
// so that the overwrite and delete cases have something real to attack.
func (f *pvGateFixture) seedProvider(t *testing.T) {
	t.Helper()
	require.NoError(t, f.store.AddProjectProvider(context.Background(),
		&store.ProjectProvider{
			ProjectID: f.projA.ID, BrokerID: f.attachable.ID,
			BrokerName: f.attachable.Name, LocalPath: f.ownedPath,
			LinkedBy: f.owner.ID,
		}))
}

func (f *pvGateFixture) providers(t *testing.T) []store.ProjectProvider {
	t.Helper()
	got, err := f.store.GetProjectProviders(context.Background(), f.projA.ID)
	require.NoError(t, err)
	return got
}

// requireNoProvider is the real signal for the attach cases: the gate must have
// stopped the write, not merely returned an unhappy status alongside it.
func (f *pvGateFixture) requireNoProvider(t *testing.T, because string) {
	t.Helper()
	require.Empty(t, f.providers(t),
		"a refused request attached a provider anyway (%s)", because)
}

// requireOwnedPathIntact is the real signal for the overwrite and delete cases.
func (f *pvGateFixture) requireOwnedPathIntact(t *testing.T, because string) {
	t.Helper()
	got := f.providers(t)
	require.Len(t, got, 1, "the project's provider was removed or duplicated (%s)", because)
	require.Equal(t, f.attachable.ID, got[0].BrokerID)
	require.Equal(t, f.ownedPath, got[0].LocalPath,
		"a refused request overwrote the project's provider LocalPath, which is "+
			"what decides where the project's workspace lives (%s)", because)
}

// ---------------------------------------------------------------------------
// Attach: the three caller classes that used to receive 201.
// ---------------------------------------------------------------------------

func TestPVGate_AttachDenied(t *testing.T) {
	cases := []struct {
		name string
		want int
		call func(*testing.T, *pvGateFixture) *httptest.ResponseRecorder
	}{
		{"unrelated user", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asUser(t, f.outsdr, http.MethodPost, f.collectionPath(),
					f.attachBody(f.intruderPath))
			}},
		{
			// 404 rather than 403: requireProjectVisibleToAgent runs first, so
			// an agent outside the project cannot use the refusal to confirm
			// the project exists.
			"cross-project agent", http.StatusNotFound,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asAgent(t, f.stranger, http.MethodPost, f.collectionPath(),
					f.attachBody(f.intruderPath))
			}},
		{"broker", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asBroker(t, http.MethodPost, f.collectionPath(),
					f.attachBody(f.intruderPath))
			}},
		{
			// The agent inside projA is denied too. Attaching a provider is a
			// write, and the agent project baseline (authz.go:239) admits an
			// agent to read-class actions on its own project only. Pinned
			// because "the gate lets in-project agents through" is a plausible
			// misreading of what the cross-project case above establishes.
			"in-project agent", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asAgent(t, f.insider, http.MethodPost, f.collectionPath(),
					f.attachBody(f.intruderPath))
			}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := pvGateSetup(t)
			rec := c.call(t, f)
			require.Equal(t, c.want, rec.Code, "body=%s", rec.Body.String())
			f.requireNoProvider(t, c.name)
		})
	}
}

// TestPVGate_OverwriteDenied is the sharpest case in the file. Because
// AddProjectProvider upserts on (projectID, brokerID), an attacker did not need
// to attach a new broker — reposting the victim's own broker with a different
// LocalPath rewrote where that project's workspace resolves to. The status code
// is secondary here; the stored LocalPath is the finding.
func TestPVGate_OverwriteDenied(t *testing.T) {
	for _, c := range []struct {
		name string
		want int
		call func(*testing.T, *pvGateFixture) *httptest.ResponseRecorder
	}{
		{"unrelated user", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asUser(t, f.outsdr, http.MethodPost, f.collectionPath(),
					f.attachBody(f.intruderPath))
			}},
		{"cross-project agent", http.StatusNotFound,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asAgent(t, f.stranger, http.MethodPost, f.collectionPath(),
					f.attachBody(f.intruderPath))
			}},
		{"broker", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asBroker(t, http.MethodPost, f.collectionPath(),
					f.attachBody(f.intruderPath))
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := pvGateSetup(t)
			f.seedProvider(t)
			rec := c.call(t, f)
			require.Equal(t, c.want, rec.Code, "body=%s", rec.Body.String())
			f.requireOwnedPathIntact(t, c.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Detach.
// ---------------------------------------------------------------------------

func TestPVGate_DetachDenied(t *testing.T) {
	for _, c := range []struct {
		name string
		want int
		call func(*testing.T, *pvGateFixture) *httptest.ResponseRecorder
	}{
		{"unrelated user", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asUser(t, f.outsdr, http.MethodDelete, f.resourcePath(), nil)
			}},
		{"cross-project agent", http.StatusNotFound,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asAgent(t, f.stranger, http.MethodDelete, f.resourcePath(), nil)
			}},
		{"broker", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asBroker(t, http.MethodDelete, f.resourcePath(), nil)
			}},
		{"in-project agent", http.StatusForbidden,
			func(t *testing.T, f *pvGateFixture) *httptest.ResponseRecorder {
				return f.asAgent(t, f.insider, http.MethodDelete, f.resourcePath(), nil)
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := pvGateSetup(t)
			f.seedProvider(t)
			rec := c.call(t, f)
			require.Equal(t, c.want, rec.Code, "body=%s", rec.Body.String())
			f.requireOwnedPathIntact(t, c.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Positive controls. Without these the gate could be "correct" by refusing
// everyone, which would break every legitimate project-linking flow there is.
// ---------------------------------------------------------------------------

func TestPVGate_OwnerMayAttachAndDetach(t *testing.T) {
	f := pvGateSetup(t)

	rec := f.asUser(t, f.owner, http.MethodPost, f.collectionPath(),
		f.attachBody(f.ownedPath))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	f.requireOwnedPathIntact(t, "owner attach")

	rec = f.asUser(t, f.owner, http.MethodDelete, f.resourcePath(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, "body=%s", rec.Body.String())
	require.Empty(t, f.providers(t), "the owner's delete did not remove the provider")
}

func TestPVGate_AdminMayAttachAndDetach(t *testing.T) {
	f := pvGateSetup(t)

	rec := f.asUser(t, f.admin, http.MethodPost, f.collectionPath(),
		f.attachBody(f.ownedPath))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	f.requireOwnedPathIntact(t, "admin attach")

	rec = f.asUser(t, f.admin, http.MethodDelete, f.resourcePath(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, "body=%s", rec.Body.String())
	require.Empty(t, f.providers(t), "the admin's delete did not remove the provider")
}

// TestPVGate_OwnerMayReplaceOwnProvider pins that the upsert is still an upsert
// for the caller entitled to use it. The overwrite case above must be read as
// "not by an outsider", not as "never".
func TestPVGate_OwnerMayReplaceOwnProvider(t *testing.T) {
	f := pvGateSetup(t)
	f.seedProvider(t)

	rec := f.asUser(t, f.owner, http.MethodPost, f.collectionPath(),
		f.attachBody(f.intruderPath))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	got := f.providers(t)
	require.Len(t, got, 1)
	require.Equal(t, f.intruderPath, got[0].LocalPath,
		"the owner's own re-attach did not take effect, so the gate has turned "+
			"a legitimate update into a silent no-op")
}

// ---------------------------------------------------------------------------
// Authentication, and what the gate deliberately does not disclose.
// ---------------------------------------------------------------------------

// TestPVGate_UnauthenticatedDenied records that anonymous callers are stopped by
// the authentication middleware, before the gate is consulted. This was already
// true before the gate existed and is the one refusal that is not evidence about
// it — asserted so that a future change which moves authentication is visible.
func TestPVGate_UnauthenticatedDenied(t *testing.T) {
	f := pvGateSetup(t)
	f.seedProvider(t)

	attach := f.anonymous(http.MethodPost, f.collectionPath(), f.attachBody(f.intruderPath))
	require.Equal(t, http.StatusUnauthorized, attach.Code, "body=%s", attach.Body.String())

	detach := f.anonymous(http.MethodDelete, f.resourcePath(), nil)
	require.Equal(t, http.StatusUnauthorized, detach.Code, "body=%s", detach.Body.String())

	f.requireOwnedPathIntact(t, "anonymous")
}

// TestPVGate_ExistenceNotDisclosed pins the ordering. The dispatcher looks the
// project up before it can authorize — it needs the record to do so — but the
// answer is withheld until the gate has spoken, so a caller who may not mutate
// this project gets the same response whether or not it exists.
//
// This is stricter than the neighbouring gated project handlers, which answer
// 404 for a missing project and 403 for a forbidden one. It is deliberate here
// and is not a claim about those.
func TestPVGate_ExistenceNotDisclosed(t *testing.T) {
	f := pvGateSetup(t)
	missing := "/api/v1/projects/" + tid("pvgate-no-such-project") + "/providers"

	t.Run("unrelated user cannot tell missing from forbidden", func(t *testing.T) {
		real := f.asUser(t, f.outsdr, http.MethodPost, f.collectionPath(),
			f.attachBody(f.intruderPath))
		fake := f.asUser(t, f.outsdr, http.MethodPost, missing,
			f.attachBody(f.intruderPath))
		require.Equal(t, http.StatusForbidden, real.Code)
		require.Equal(t, real.Code, fake.Code,
			"the response reveals whether the project exists")
		require.Equal(t, real.Body.String(), fake.Body.String(),
			"the body reveals whether the project exists")
	})

	t.Run("cross-project agent cannot either", func(t *testing.T) {
		real := f.asAgent(t, f.stranger, http.MethodPost, f.collectionPath(),
			f.attachBody(f.intruderPath))
		fake := f.asAgent(t, f.stranger, http.MethodPost, missing,
			f.attachBody(f.intruderPath))
		require.Equal(t, http.StatusNotFound, real.Code)
		require.Equal(t, real.Code, fake.Code,
			"the response reveals whether the project exists")
		require.Equal(t, real.Body.String(), fake.Body.String(),
			"the body reveals whether the project exists")
	})

	t.Run("an authorized caller still gets the truth", func(t *testing.T) {
		// The admin passes the gate, so withholding the lookup result from them
		// would be a bug of its own: a 403 here would misdescribe the problem.
		rec := f.asUser(t, f.admin, http.MethodPost, missing,
			f.attachBody(f.intruderPath))
		require.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
	})
}

// TestPVGate_MethodDispatchUnchanged records that an unsupported verb still
// answers 405 rather than being caught by the gate, because isProviderMutation
// selects only POST-on-collection and DELETE-on-broker. Not an authorization
// property; here so that a future reorder is visible.
func TestPVGate_MethodDispatchUnchanged(t *testing.T) {
	f := pvGateSetup(t)
	rec := f.asUser(t, f.owner, http.MethodPut, f.collectionPath(), nil)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "body=%s", rec.Body.String())
}
