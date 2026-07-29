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
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Regression tests for the three project cache entry handlers in
// project_cache.go: cache/status, cache/refresh and cache/notify.
//
// These are NOT a #591 idiom conversion. There was no guard here to convert:
// measured on an unmodified tree at 9a85f085, project_cache.go (508 lines)
// contained zero authorization calls of any kind. Any caller the middleware
// would authenticate at all could refresh one project's cache, overwrite it
// from GCS via cache/notify, or read back the ID of the broker serving it.
//
// What each route is, and therefore what it is gated as:
//
//	cache/status   read  — but not a harmless one. The response carries the ID
//	                       of the broker that last served this project, plus
//	                       its file and byte counts.
//	cache/refresh  write — tells a broker to upload the workspace to GCS and
//	                       overwrites the hub's local cache with the result.
//	cache/notify   write — runs SyncFromGCS over the hub's cache directory for
//	                       the project, so an unauthorized caller could
//	                       overwrite one project's cached workspace on demand.
//
// The caller classes the brief requires, plus a broker:
//
//	cross-project agent  -> 404 on status and refresh (isolation runs before
//	                        authorization, so the response does not confirm the
//	                        project exists); 403 on notify — see the note on
//	                        pcGateExpect below, this asymmetry is deliberate
//	unrelated user       -> 403
//	broker               -> 403, INCLUDING on cache/notify, whose body reads a
//	                        broker identity out of the context. Being the
//	                        endpoint's intended caller is not authorization.
//	project owner        -> succeeds — the positive control, without which
//	                        every row above is satisfiable by a handler that
//	                        refuses everyone
//	unauthenticated      -> 401
//
// and the read/write split: an agent INSIDE the project reads status (the
// agent project read baseline, authz.go:239) and cannot refresh or notify.
//
// Test naming: everything file-local is prefixed pcGate.

type pcGateFixture struct {
	srv   *Server
	store store.Store

	owner  *store.User
	outsdr *store.User

	projA *store.Project
	projB *store.Project

	// insider belongs to projA, stranger to projB.
	insider  *store.Agent
	stranger *store.Agent

	broker       *store.RuntimeBroker
	brokerSecret []byte
}

// pcGateSecretBrokerID is seeded into projA's sync state. cache/status echoes
// it back in the response body, so it doubles as the disclosure canary: it must
// not appear in the body of any response the gate refuses.
const pcGateSecretBrokerID = "broker-id-must-not-leak"

const (
	pcGateSeededFileCount  = 7
	pcGateSeededTotalBytes = 4242
)

func pcGateSetup(t *testing.T) *pcGateFixture {
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
	f := &pcGateFixture{srv: srv, store: s}

	f.owner = &store.User{
		ID: tid("pcgate-owner"), Email: "pcgate-owner@example.com",
		DisplayName: "Owner", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.owner))

	// A member with no relationship to either project.
	f.outsdr = &store.User{
		ID: tid("pcgate-outsider"), Email: "pcgate-outsider@example.com",
		DisplayName: "Outsider", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.outsdr))

	// projA has a git remote and no provider brokers, so an authorized
	// cache/refresh reaches findConnectedProvider and stops at 409. The slug is
	// unique per run because handleProjectCacheStatus stats
	// hubManagedProjectPath(slug); the test asserts nothing about whether a
	// cache exists, only about who is allowed to ask.
	f.projA = &store.Project{
		ID: tid("pcgate-pa"), Name: "PC Gate A", Slug: tid("pcgate-pa"),
		OwnerID: f.owner.ID, GitRemote: "https://example.invalid/a.git",
	}
	require.NoError(t, s.CreateProject(ctx, f.projA))

	f.projB = &store.Project{
		ID: tid("pcgate-pb"), Name: "PC Gate B", Slug: tid("pcgate-pb"),
		OwnerID: f.owner.ID, GitRemote: "https://example.invalid/b.git",
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
	f.insider = mk("pcgate-insider", f.projA.ID)
	f.stranger = mk("pcgate-stranger", f.projB.ID)

	// A broker is neither a UserIdentity nor an AgentIdentity, and CheckAccess
	// has no broker arm — so it is denied by the gate rather than skipped by
	// it. Before the gate existed it was served.
	f.brokerSecret = []byte("pcgate-secret-key-32-bytes!!!!!!")
	f.broker = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "pcgate-broker", Slug: "pcgate-broker",
		Status: store.BrokerStatusOnline, AutoProvide: true,
		Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, f.broker))
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  f.broker.ID,
		SecretKey: f.brokerSecret,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))

	f.seedSyncState(t)
	return f
}

// seedSyncState writes the sync state that cache/status discloses and that a
// successful cache/notify would overwrite.
func (f *pcGateFixture) seedSyncState(t *testing.T) {
	t.Helper()
	when := time.Unix(1700000000, 0).UTC()
	require.NoError(t, f.store.UpsertProjectSyncState(context.Background(),
		&store.ProjectSyncState{
			ProjectID:    f.projA.ID,
			BrokerID:     pcGateSecretBrokerID,
			LastSyncTime: &when,
			FileCount:    pcGateSeededFileCount,
			TotalBytes:   pcGateSeededTotalBytes,
		}))
}

// requireSyncStateUnchanged asserts that a refused request did not reach the
// write. cache/notify's last act is to upsert this record; a 403 returned after
// the record was rewritten is still the vulnerability, and a status-code-only
// assertion cannot tell the two apart.
func (f *pcGateFixture) requireSyncStateUnchanged(t *testing.T) {
	t.Helper()
	states, err := f.store.ListProjectSyncStates(context.Background(), f.projA.ID)
	require.NoError(t, err)
	require.Len(t, states, 1, "a request the gate refused added or removed a sync state")
	require.Equal(t, pcGateSecretBrokerID, states[0].BrokerID,
		"a request the gate refused rewrote the sync state's broker")
	require.Equal(t, pcGateSeededFileCount, states[0].FileCount,
		"a request the gate refused rewrote the sync state's file count")
	require.Equal(t, int64(pcGateSeededTotalBytes), states[0].TotalBytes,
		"a request the gate refused rewrote the sync state's byte count")
}

// ---------------------------------------------------------------------------
// Request helpers.
//
// Rule 4: none of these inject an identity into the request context. These
// requests go through srv.Handler(), where the auth middleware establishes its
// own identity and discards anything the test put there. Each helper supplies
// the credential the middleware would genuinely have produced for that caller.
// ---------------------------------------------------------------------------

func (f *pcGateFixture) newRequest(method, path string) *http.Request {
	var rdr io.Reader = bytes.NewReader(nil)
	return httptest.NewRequest(method, path, rdr)
}

func (f *pcGateFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *pcGateFixture) asUser(t *testing.T, u *store.User, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName,
		string(u.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := f.newRequest(method, path)
	req.Header.Set("Authorization", "Bearer "+tok)
	return f.serve(req)
}

func (f *pcGateFixture) asAgent(t *testing.T, a *store.Agent, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(a.ID, a.ProjectID, nil, nil)
	require.NoError(t, err)

	req := f.newRequest(method, path)
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

func (f *pcGateFixture) asBroker(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := f.newRequest(method, path)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "pcgate-nonce-" + uuid.New().String()
	req.Header.Set(HeaderBrokerID, f.broker.ID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)

	svc := f.srv.brokerAuthService
	require.NotNil(t, svc, "broker auth service must be configured")
	mac := hmac.New(sha256.New, f.brokerSecret)
	mac.Write(svc.buildCanonicalString(req, timestamp, nonce))
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	return f.serve(req)
}

func (f *pcGateFixture) anonymous(_ *testing.T, method, path string) *httptest.ResponseRecorder {
	return f.serve(f.newRequest(method, path))
}

// ---------------------------------------------------------------------------
// Route table.
// ---------------------------------------------------------------------------

type pcGateRoute struct {
	name   string
	method string
	path   string

	// okCode is what the project owner gets: the positive control. Two of the
	// three are error codes the GATE CANNOT PRODUCE, which is the point —
	// reaching a 409 or a 502 proves the request was authorized and then
	// stopped by configuration, not refused.
	//
	//	409 refresh — projA has no provider brokers, so findConnectedProvider
	//	              fails and the handler answers Conflict.
	//	502 notify  — the test server has no storage configured, so the handler
	//	              answers RuntimeError before it touches the cache
	//	              directory. Nothing under ~/.scion is written by this test.
	okCode int

	// write marks the two routes that mutate, so the sync-state assertion is
	// applied to them and not to the read.
	write bool
}

func (f *pcGateFixture) routes() []pcGateRoute {
	base := "/api/v1/projects/" + f.projA.ID + "/workspace/cache/"
	return []pcGateRoute{
		{name: "status", method: http.MethodGet, path: base + "status", okCode: http.StatusOK},
		{name: "refresh", method: http.MethodPost, path: base + "refresh", okCode: http.StatusConflict, write: true},
		{name: "notify", method: http.MethodPost, path: base + "notify", okCode: http.StatusBadGateway, write: true},
	}
}

// pcGateExpect is the expected refusal code per route for a given caller.
//
// It is a per-route map rather than one code because cache/notify genuinely
// differs from the other two for one caller: status and refresh run
// requireProjectVisibleToAgent first, which collapses a cross-project agent to
// 404 so the response does not confirm projA exists, whereas notify is a plain
// s.authorize and answers 403. That is the ruled shape for notify, and the
// asymmetry is recorded here rather than smoothed over, so that a later change
// to either handler's ordering shows up as a test diff.
type pcGateExpect map[string]int

func (f *pcGateFixture) requireRefused(t *testing.T, want pcGateExpect,
	call func(*testing.T, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	for _, rt := range f.routes() {
		t.Run(rt.name, func(t *testing.T) {
			code, ok := want[rt.name]
			require.True(t, ok, "no expectation declared for route %s", rt.name)

			rec := call(t, rt.method, rt.path)
			require.Equal(t, code, rec.Code, "body: %s", rec.Body.String())
			require.NotContains(t, rec.Body.String(), pcGateSecretBrokerID,
				"a refused response disclosed the serving broker's ID")
			if rt.write {
				f.requireSyncStateUnchanged(t)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The positive control.
// ---------------------------------------------------------------------------

func TestPCGate_OwnerAllowed(t *testing.T) {
	f := pcGateSetup(t)
	for _, rt := range f.routes() {
		t.Run(rt.name, func(t *testing.T) {
			rec := f.asUser(t, f.owner, rt.method, rt.path)
			require.Equal(t, rt.okCode, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// TestPCGate_OwnerReadsRealStatus is the other half of the positive control:
// the owner does not merely get a 200, the 200 carries the sync state that the
// refused callers above must not see. Without this, "did not disclose the
// broker ID" would be satisfiable by a handler that discloses it to nobody.
func TestPCGate_OwnerReadsRealStatus(t *testing.T) {
	f := pcGateSetup(t)
	rec := f.asUser(t, f.owner, http.MethodGet,
		"/api/v1/projects/"+f.projA.ID+"/workspace/cache/status")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), pcGateSecretBrokerID)
}

// ---------------------------------------------------------------------------
// The refusals.
// ---------------------------------------------------------------------------

func TestPCGate_UnrelatedUserDenied(t *testing.T) {
	f := pcGateSetup(t)
	f.requireRefused(t, pcGateExpect{
		"status":  http.StatusForbidden,
		"refresh": http.StatusForbidden,
		"notify":  http.StatusForbidden,
	}, func(t *testing.T, method, path string) *httptest.ResponseRecorder {
		return f.asUser(t, f.outsdr, method, path)
	})
}

func TestPCGate_CrossProjectAgentDenied(t *testing.T) {
	f := pcGateSetup(t)
	f.requireRefused(t, pcGateExpect{
		// 404, not 403: requireProjectVisibleToAgent runs first on these two,
		// so an agent outside the project cannot use the response to learn
		// that the project exists.
		"status":  http.StatusNotFound,
		"refresh": http.StatusNotFound,
		// 403: notify is a plain s.authorize by ruling. Refused either way;
		// only the amount it admits differs.
		"notify": http.StatusForbidden,
	}, func(t *testing.T, method, path string) *httptest.ResponseRecorder {
		return f.asAgent(t, f.stranger, method, path)
	})
}

// TestPCGate_BrokerDenied covers the caller cache/notify was written for. Its
// body reads a broker identity out of the context to record which broker
// pushed, but AuthzService.CheckAccess has no broker arm, so a broker falls to
// its default deny like any other unrecognized identity. This test pins that:
// "the endpoint was designed for brokers" is not an authorization rule, and if
// broker access is ever wanted it has to be granted somewhere visible, not
// inherited from the absence of a check.
func TestPCGate_BrokerDenied(t *testing.T) {
	f := pcGateSetup(t)
	f.requireRefused(t, pcGateExpect{
		"status":  http.StatusForbidden,
		"refresh": http.StatusForbidden,
		"notify":  http.StatusForbidden,
	}, func(t *testing.T, method, path string) *httptest.ResponseRecorder {
		return f.asBroker(t, method, path)
	})
}

func TestPCGate_UnauthenticatedDenied(t *testing.T) {
	f := pcGateSetup(t)
	f.requireRefused(t, pcGateExpect{
		"status":  http.StatusUnauthorized,
		"refresh": http.StatusUnauthorized,
		"notify":  http.StatusUnauthorized,
	}, func(t *testing.T, method, path string) *httptest.ResponseRecorder {
		return f.anonymous(t, method, path)
	})
}

// TestPCGate_InProjectAgentMayReadNotWrite pins the read/write split. An agent
// in projA passes cache/status on the agent project read baseline
// (authz.go:239, read-class actions on its own project) and is refused
// cache/refresh and cache/notify, because ActionUpdate is not read-class. If
// refresh or notify were ever gated with ActionRead instead, the codes below
// would flip and this test would say so.
func TestPCGate_InProjectAgentMayReadNotWrite(t *testing.T) {
	f := pcGateSetup(t)
	base := "/api/v1/projects/" + f.projA.ID + "/workspace/cache/"

	t.Run("status/allowed", func(t *testing.T) {
		rec := f.asAgent(t, f.insider, http.MethodGet, base+"status")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})

	for _, rt := range []struct{ name, path string }{
		{"refresh", base + "refresh"},
		{"notify", base + "notify"},
	} {
		t.Run(rt.name+"/refused", func(t *testing.T) {
			rec := f.asAgent(t, f.insider, http.MethodPost, rt.path)
			require.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
			f.requireSyncStateUnchanged(t)
		})
	}
}

// TestPCGate_ProjectShapeNotDisclosed checks the placement of the gates rather
// than their verdict. Each handler's first post-gate act narrates the project
// or the hub: refresh says whether this project's workspace lives on a broker
// and whether one is connected (409), notify reports on hub storage
// configuration (502), status stats the cache directory. A refused caller must
// receive none of those codes — receiving one would mean the gate was placed
// after the disclosure it is supposed to precede.
func TestPCGate_ProjectShapeNotDisclosed(t *testing.T) {
	f := pcGateSetup(t)
	disclosing := map[int]bool{
		http.StatusConflict:   true,
		http.StatusBadGateway: true,
		http.StatusOK:         true,
	}

	callers := []struct {
		name string
		call func(*testing.T, string, string) *httptest.ResponseRecorder
	}{
		{"unrelated-user", func(t *testing.T, m, p string) *httptest.ResponseRecorder {
			return f.asUser(t, f.outsdr, m, p)
		}},
		{"cross-project-agent", func(t *testing.T, m, p string) *httptest.ResponseRecorder {
			return f.asAgent(t, f.stranger, m, p)
		}},
		{"broker", func(t *testing.T, m, p string) *httptest.ResponseRecorder {
			return f.asBroker(t, m, p)
		}},
		{"unauthenticated", func(t *testing.T, m, p string) *httptest.ResponseRecorder {
			return f.anonymous(t, m, p)
		}},
	}

	for _, c := range callers {
		for _, rt := range f.routes() {
			t.Run(c.name+"/"+rt.name, func(t *testing.T) {
				rec := c.call(t, rt.method, rt.path)
				require.False(t, disclosing[rec.Code],
					"refused caller received %d, a code only reachable past the gate: %s",
					rec.Code, rec.Body.String())
			})
		}
	}
}

// TestPCGate_MethodDispatchStillFirst records that the gates were inserted
// after each handler's method check, not before it. This is not an
// authorization property; it is here because the three handlers answer 405 to
// the wrong verb, existing tests depend on that, and moving a gate above the
// method check would change 405 into 401 for unauthenticated callers. Pinning
// it keeps a future reorder honest.
func TestPCGate_MethodDispatchStillFirst(t *testing.T) {
	f := pcGateSetup(t)
	base := "/api/v1/projects/" + f.projA.ID + "/workspace/cache/"
	cases := []struct{ name, method, path string }{
		{"status/post", http.MethodPost, base + "status"},
		{"refresh/get", http.MethodGet, base + "refresh"},
		{"notify/get", http.MethodGet, base + "notify"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := f.asUser(t, f.owner, c.method, c.path)
			require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "body: %s", rec.Body.String())
		})
	}
}
