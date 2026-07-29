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

// Regression tests for the four project-scoped READ guards that use the second
// #591 idiom:
//
//	identity := GetIdentityFromContext(ctx)
//	if userIdent, ok := identity.(UserIdentity); ok { ...CheckAccess... }
//
// with no else. The bypass here is the type ASSERTION rather than a nil check:
// identity is non-nil for an agent or a broker — both are rejected by the
// explicit nil guard above these sites — and it is the assertion to UserIdentity
// that fails, so the body is skipped in silence and the handler serves the
// resource.
//
// The four sites:
//
//	project_pre_start_hook_handlers.go:77   handleProjectPreStartHooks     GET
//	project_pre_start_hook_handlers.go:245  handleProjectPreStartHookByID  GET
//	project_settings_handlers.go:74         handleProjectSettings          GET
//	handlers_shared_dirs.go:47              handleProjectSharedDirs        GET
//
// What makes this set unusually clear-cut: in all three files every MUTATING
// method in the same switch statement already carries the else { Forbidden(w) }
// clause, and only the read paths lack it. The intended shape is present in the
// same function, a few lines away, in every case. These are omissions, not a
// deliberate policy that non-user callers may read project configuration.
//
// Reachability: UnifiedAuthMiddleware validates agent tokens with no path
// allowlist and BrokerAuthMiddleware establishes a broker identity for any path
// once the HMAC verifies, so both kinds reach these handlers with credentials
// the hub itself issues. This is a live bypass, not a latent one.
//
// Test naming: everything file-local is prefixed projRead.

type projReadFixture struct {
	srv   *Server
	store store.Store

	owner  *store.User
	outsdr *store.User

	projA *store.Project
	projB *store.Project

	hook *store.ProjectPreStartHook

	// insider belongs to projA, stranger to projB.
	insider  *store.Agent
	stranger *store.Agent

	broker       *store.RuntimeBroker
	brokerSecret []byte
}

func projReadSetup(t *testing.T) *projReadFixture {
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
	f := &projReadFixture{srv: srv, store: s}

	f.owner = &store.User{
		ID: tid("projread-owner"), Email: "projread-owner@example.com",
		DisplayName: "Owner", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.owner))

	// A member with no relationship to either project.
	f.outsdr = &store.User{
		ID: tid("projread-outsider"), Email: "projread-outsider@example.com",
		DisplayName: "Outsider", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.outsdr))

	f.projA = &store.Project{
		ID: tid("projread-pa"), Name: "Proj Read A", Slug: "projread-pa",
		OwnerID: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projA))

	f.projB = &store.Project{
		ID: tid("projread-pb"), Name: "Proj Read B", Slug: "projread-pb",
		OwnerID: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projB))

	f.hook, err = s.CreateProjectPreStartHook(ctx, &store.ProjectPreStartHook{
		ID: tid("projread-hook"), ProjectID: f.projA.ID,
		Name: "projread-hook", Slug: "projread-hook",
		Script: "#!/bin/sh\necho hello\n", Status: "active",
		CreatedBy: f.owner.Email,
	})
	require.NoError(t, err)

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
	f.insider = mk("projread-insider", f.projA.ID)
	f.stranger = mk("projread-stranger", f.projB.ID)

	f.brokerSecret = []byte("projread-secret-key-32-bytes!!!!")
	f.broker = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "projread-broker", Slug: "projread-broker",
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

	return f
}

// paths returns the four converted read endpoints, all scoped to projA.
func (f *projReadFixture) paths() []struct{ name, path string } {
	base := "/api/v1/projects/" + f.projA.ID
	return []struct{ name, path string }{
		{"pre-start-hooks list", base + "/pre-start-hooks"},
		{"pre-start-hook by id", base + "/pre-start-hooks/" + f.hook.ID},
		{"project settings", base + "/settings"},
		{"shared dirs", base + "/shared-dirs"},
	}
}

// asUser issues a GET bearing a real access token.
//
// Rule 4: the identity cannot be injected into the request context here,
// because these requests go through srv.Handler() and UnifiedAuthMiddleware
// establishes its own identity, discarding any the test put there. The test
// supplies the credential the middleware would genuinely have produced.
func (f *projReadFixture) asUser(t *testing.T, u *store.User, path string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName,
		string(u.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// asAgent issues a GET bearing a real agent token for the given agent.
func (f *projReadFixture) asAgent(t *testing.T, a *store.Agent, path string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(a.ID, a.ProjectID, nil, nil)
	require.NoError(t, err)
	return doRequestWithAgentToken(t, f.srv, http.MethodGet, path, nil, tok)
}

// asBroker issues an HMAC-signed broker GET. brokerIdentityImpl implements
// neither UserIdentity nor AgentIdentity, so the assertion at each of these
// sites fails and, before the conversion, the guard was skipped entirely.
func (f *projReadFixture) asBroker(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, bytes.NewReader(nil))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "projread-nonce-" + uuid.New().String()
	req.Header.Set(HeaderBrokerID, f.broker.ID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)

	svc := f.srv.brokerAuthService
	require.NotNil(t, svc, "broker auth service must be configured")
	mac := hmac.New(sha256.New, f.brokerSecret)
	mac.Write(svc.buildCanonicalString(req, timestamp, nonce))
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestProjRead_BrokerDenied is the core #591 regression for this set. A broker
// is neither a UserIdentity nor an AgentIdentity, so it fails the assertion at
// every one of these four sites and, before the conversion, read the project's
// configuration unchecked.
//
// Reverting the conversion turns all four of these into 200s.
func TestProjRead_BrokerDenied(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.asBroker(t, tc.path)
			require.Equal(t, http.StatusForbidden, rec.Code,
				"a broker must be denied; before the conversion the assertion to "+
					"UserIdentity failed and the guard was skipped in silence")
		})
	}
}

// TestProjRead_CrossProjectAgentDenied covers the agent half of the same
// bypass, and pins 404 rather than 403.
//
// Isolation runs before authorization: an agent outside the project must not be
// able to distinguish "this project exists but you may not read it" from "no
// such project". Answering 403 here would confirm the project's existence to a
// caller that cannot otherwise establish it.
func TestProjRead_CrossProjectAgentDenied(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.asAgent(t, f.stranger, tc.path)
			require.Equal(t, http.StatusNotFound, rec.Code,
				"a cross-project agent must get 404, not 403: 403 discloses that "+
					"the project exists")
		})
	}
}

// TestProjRead_UnauthenticatedDenied pins that none of the four is reachable
// without an identity, and that the conversion did not collapse 401 into 403.
func TestProjRead_UnauthenticatedDenied(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

// TestProjRead_UnrelatedUserDenied is the user-path control. It passed before
// the conversion as well — that is the point. The user path was the one path
// these guards did check, and its continuing to pass is exactly what allowed the
// bypass to survive, so it is pinned to show the conversion did not weaken it.
func TestProjRead_UnrelatedUserDenied(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.asUser(t, f.outsdr, tc.path)
			require.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

// TestProjRead_SameProjectAgentStillReads is the positive control that matters
// most here, because the conversion's real risk is over-tightening rather than
// under-tightening.
//
// An agent inside the project must keep its read access. That access is granted
// by the project-scoped read baseline in checkAccessForAgent, not by anything in
// these handlers, so this test is also what pins that the conversion routes
// agent callers through the baseline instead of denying them wholesale.
func TestProjRead_SameProjectAgentStillReads(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.asAgent(t, f.insider, tc.path)
			require.Equal(t, http.StatusOK, rec.Code,
				"an agent in the project must still read: 403 would mean the "+
					"conversion over-tightened, 404 would mean isolation denies on "+
					"identity kind rather than on project")
		})
	}
}

// TestProjRead_ProjectOwnerStillReads is the user-side positive control.
func TestProjRead_ProjectOwnerStillReads(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.asUser(t, f.owner, tc.path)
			require.Equal(t, http.StatusOK, rec.Code,
				"the project owner must still be able to read")
		})
	}
}

// ---------------------------------------------------------------------------
// Write paths.
//
// These are NOT #591 sites. Every one already carries else { Forbidden(w) } and
// already denies a cross-project agent. What changed is WHICH denial: a 403
// confirms the project exists to a caller who cannot otherwise establish that,
// so isolation now runs first and answers 404, matching the read paths.
//
// This is a deliberate, observable behaviour change on paths that were already
// fail-closed, and it is pinned here rather than left implicit. It narrows a
// disclosure; it does not close an authorization hole, and nothing below should
// be read as claiming otherwise.
//
// The seven write guards, all in the same three files:
//
//	project_pre_start_hook_handlers.go  POST create, POST activate, PUT, DELETE
//	project_settings_handlers.go        PUT
//	handlers_shared_dirs.go             POST, and DELETE via handleProjectSharedDirByName
// ---------------------------------------------------------------------------

// writePaths returns the seven mutating endpoints, scoped to projA.
func (f *projReadFixture) writePaths() []struct{ name, method, path, body string } {
	base := "/api/v1/projects/" + f.projA.ID
	hook := base + "/pre-start-hooks/" + f.hook.ID
	return []struct{ name, method, path, body string }{
		{"create hook", http.MethodPost, base + "/pre-start-hooks",
			`{"name":"x","script":"#!/bin/sh\necho x\n"}`},
		{"activate hook", http.MethodPost, hook + "/activate", ``},
		{"update hook", http.MethodPut, hook, `{"name":"y"}`},
		{"delete hook", http.MethodDelete, hook, ``},
		{"update settings", http.MethodPut, base + "/settings", `{}`},
		{"create shared dir", http.MethodPost, base + "/shared-dirs",
			`{"name":"sd","mountPath":"/mnt/sd"}`},
		{"delete shared dir", http.MethodDelete, base + "/shared-dirs/sd", ``},
	}
}

func (f *projReadFixture) agentWrite(t *testing.T, a *store.Agent, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(a.ID, a.ProjectID, nil, nil)
	require.NoError(t, err)
	return doRequestWithAgentToken(t, f.srv, method, path, bytes.NewBufferString(body), tok)
}

func (f *projReadFixture) userWrite(t *testing.T, u *store.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName,
		string(u.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestProjWrite_CrossProjectAgentGetsNotFound pins the changed answer. Before
// this change each of these returned 403, which told an agent in an unrelated
// project that the project exists. The caller is denied either way; only the
// disclosure differs.
func TestProjWrite_CrossProjectAgentGetsNotFound(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.writePaths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.agentWrite(t, f.stranger, tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusNotFound, rec.Code,
				"deliberate change: this path answered 403 before, which confirmed "+
					"the project's existence. It was already fail-closed — this is a "+
					"disclosure narrowing, not an authorization fix")
			// Pin WHO produced the 404. Several of these endpoints emit a second,
			// resource-level 404 (e.g. delete shared dir answers "Shared directory
			// not found" to a legitimate owner). Asserting the status alone would
			// stay green if a future reorder moved that resource lookup ahead of the
			// isolation gate, returning the resource-level 404 to a cross-project
			// agent while the gate is dead. Requiring the isolation message keeps the
			// assertion tied to the gate.
			require.Contains(t, rec.Body.String(), "Project not found",
				"the 404 must come from the project-isolation gate, not from a "+
					"resource lookup that ran ahead of it")
		})
	}
}

// TestProjWrite_SameProjectAgentStillForbidden pins that the change did not
// widen anything. An agent inside the project passes isolation and is then
// denied by the write guard exactly as before: agents may not mutate project
// configuration, and the project read baseline is read-class only.
func TestProjWrite_SameProjectAgentStillForbidden(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.writePaths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.agentWrite(t, f.insider, tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusForbidden, rec.Code,
				"an in-project agent must still be denied writes; 404 here would mean "+
					"isolation is denying on identity kind, 2xx would mean the change "+
					"widened write access")
		})
	}
}

// TestProjWrite_UnrelatedUserStillForbidden is the constraint that this must not
// become a user-visible change. A non-member user received 403 before and must
// receive 403 after — users are not subject to the agent isolation check at all.
func TestProjWrite_UnrelatedUserStillForbidden(t *testing.T) {
	f := projReadSetup(t)

	for _, tc := range f.writePaths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.userWrite(t, f.outsdr, tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusForbidden, rec.Code,
				"users keep 403: the isolation check is a no-op for non-agent callers, "+
					"and turning a user's 403 into a 404 would be an unrelated "+
					"user-visible change")
		})
	}
}

// TestProjWrite_ProjectOwnerStillWrites is the positive control. The isolation
// call is a no-op for users, but that is a claim about GetAgentIdentityFromContext
// returning nil for a user identity, and it is measured here rather than assumed.
func TestProjWrite_ProjectOwnerStillWrites(t *testing.T) {
	f := projReadSetup(t)
	base := "/api/v1/projects/" + f.projA.ID

	tests := []struct{ name, method, path, body string }{
		{"update settings", http.MethodPut, base + "/settings", `{}`},
		{"create shared dir", http.MethodPost, base + "/shared-dirs",
			`{"name":"owner-sd","mountPath":"/mnt/owner-sd"}`},
		{"create hook", http.MethodPost, base + "/pre-start-hooks",
			`{"name":"owner-hook","script":"#!/bin/sh\necho hi\n"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.userWrite(t, f.owner, tc.method, tc.path, tc.body)
			require.Less(t, rec.Code, 300,
				"the project owner must still be able to write; got %d %s",
				rec.Code, rec.Body.String())
		})
	}
}
