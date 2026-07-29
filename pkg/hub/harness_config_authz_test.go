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

// Regression tests for the harness-config write endpoints.
//
// The hole: POST /api/v1/harness-configs performed no authorization at all. An
// authenticated caller of any kind could create a harness config at global
// scope with an attacker-chosen Config.Image, and broker nodes pull that image
// on the next agent start.
//
// PUT and PATCH on an existing config were ungated the same way. The two are
// not equivalent and the tests below do not treat them as such: PUT
// (updateHarnessConfig) unmarshals the whole record and so rewrites
// Config.Image on a config that already exists, which is the attacker-image
// effect again through a different verb — gating create alone would have moved
// the hole rather than closed it. PATCH (patchHarnessConfig) unmarshals a
// five-field struct that does not include Config, so it cannot reach the image;
// what it could do unauthorized was rename and re-describe another scope's
// config. Both are gated, for different reasons.
//
// A third shape, in the same file: handleHarnessConfigClone DID authorize, but
// its switch over the caller-supplied destination scope had a global arm, a
// project arm, and no others. A clone to "user" scope, or to any scope string
// the switch did not name, matched nothing and fell out of the bottom into the
// create with nothing checked. The switch was exhaustive over the values its
// author had in mind and silent about every other, and the silence read as
// permission.
//
// A fourth: PUT authorized the scope the record was IN and then wrote the
// record from the body, scope field included. An agent authorized on its own
// project's config PUT it back as "global" with an attacker image and got 200.
// The gate asked where the record was and never where the caller was moving it
// to. updateHarnessConfig now preserves Scope, ScopeID and OwnerID from the
// stored record, and TestHCAuthz_UpdateCannotPromoteScope is red without that.
//
// All four endpoints now call authorizeHarnessConfigScope, which is the switch
// deleteHarnessConfig always carried — three named arms (global, project, user)
// and a default that denies — lifted out so that the endpoints share one copy
// instead of diverging copies. deleteHarnessConfig was converted to the same
// call with its behaviour and its messages unchanged.
//
// One inherited wart, preserved rather than fixed and pinned below so that it
// is a decision on the record: the global and user arms answer 401
// "Authentication required" to an agent or a broker, which are authenticated
// callers. They require a UserIdentity and read its absence as missing
// authentication rather than as insufficient permission. 403 would be the
// honest code. It is deliberately not changed here, because it is
// deleteHarnessConfig's long-standing behaviour and this commit's claim is that
// the extraction changed nothing.
//
// This gate decides WHO may write. It does not inspect WHAT is written: the
// image reference is not validated here. The two are separate layers and these
// tests only pin the first.
//
// Test naming: everything file-local is prefixed hcAuthz.

type hcAuthzFixture struct {
	srv   *Server
	store store.Store

	admin  *store.User
	outsdr *store.User

	projA *store.Project
	projB *store.Project

	insider  *store.Agent
	stranger *store.Agent

	broker       *store.RuntimeBroker
	brokerSecret []byte
}

func hcAuthzSetup(t *testing.T) *hcAuthzFixture {
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
	f := &hcAuthzFixture{srv: srv, store: s}

	f.admin = &store.User{
		ID: tid("hcauthz-admin"), Email: "hcauthz-admin@example.com",
		DisplayName: "Admin", Role: store.UserRoleAdmin, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.admin))

	f.outsdr = &store.User{
		ID: tid("hcauthz-outsider"), Email: "hcauthz-outsider@example.com",
		DisplayName: "Outsider", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.outsdr))

	f.projA = &store.Project{
		ID: tid("hcauthz-pa"), Name: "HC Authz A", Slug: "hcauthz-pa",
		OwnerID: f.admin.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projA))
	f.projB = &store.Project{
		ID: tid("hcauthz-pb"), Name: "HC Authz B", Slug: "hcauthz-pb",
		OwnerID: f.admin.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.projB))

	mk := func(name, projectID string) *store.Agent {
		a := &store.Agent{
			ID: tid(name), Slug: tid(name), Name: name,
			ProjectID: projectID, Phase: string(state.PhaseRunning),
			CreatedBy: f.admin.ID, OwnerID: f.admin.ID,
			Ancestry: []string{f.admin.ID},
		}
		require.NoError(t, s.CreateAgent(ctx, a))
		return a
	}
	f.insider = mk("hcauthz-insider", f.projA.ID)
	f.stranger = mk("hcauthz-stranger", f.projB.ID)

	f.brokerSecret = []byte("hcauthz-secret-key-32-bytes!!!!!")
	f.broker = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "hcauthz-broker", Slug: "hcauthz-broker",
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

// hcAuthzAttackerImage is the payload the create/update hole delivered: a
// harness config whose image a broker node will pull on the next agent start.
const hcAuthzAttackerImage = "attacker.invalid/pwned:latest"

func (f *hcAuthzFixture) seedConfig(t *testing.T, id, scope, scopeID, ownerID string) *store.HarnessConfig {
	t.Helper()
	hc := &store.HarnessConfig{
		ID: tid(id), Slug: id, Name: id, Harness: "claude",
		Scope: scope, ScopeID: scopeID, OwnerID: ownerID,
		Visibility: store.VisibilityPrivate,
		Status:     store.HarnessConfigStatusActive,
		Config:     &store.HarnessConfigData{Image: "trusted.example.com/base:v1"},
		Created:    time.Now(), Updated: time.Now(),
	}
	require.NoError(t, f.store.CreateHarnessConfig(context.Background(), hc))
	return hc
}

// requireImageUnchanged reads the record back from the store. A 403 returned
// after the image was already rewritten is still the vulnerability, and the
// status code alone cannot tell the two apart.
func (f *hcAuthzFixture) requireImageUnchanged(t *testing.T, id string) {
	t.Helper()
	got, err := f.store.GetHarnessConfig(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, got.Config)
	require.NotEqual(t, hcAuthzAttackerImage, got.Config.Image,
		"the image was rewritten by a request the gate refused")
}

// requireNoConfigNamed asserts that a refused create did not in fact create.
func (f *hcAuthzFixture) requireNoConfigNamed(t *testing.T, name string) {
	t.Helper()
	res, err := f.store.ListHarnessConfigs(context.Background(),
		store.HarnessConfigFilter{Name: name}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	require.Empty(t, res.Items, "a request the gate refused created %q anyway", name)
}

// ---------------------------------------------------------------------------
// Request helpers. Rule 4: each supplies the credential the middleware would
// genuinely have produced; none injects an identity into the context.
// ---------------------------------------------------------------------------

func (f *hcAuthzFixture) newRequest(t *testing.T, method, path string, body interface{}) *http.Request {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (f *hcAuthzFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *hcAuthzFixture) asUser(t *testing.T, u *store.User, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName,
		string(u.Role), ClientTypeAPI)
	require.NoError(t, err)
	req := f.newRequest(t, method, path, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	return f.serve(req)
}

// asAgent issues the request with an agent token carrying agent:create, which
// is the scope the project arm requires. Using the default scope set would make
// every agent case fail on "Missing required scope" and prove nothing about the
// project check underneath it.
func (f *hcAuthzFixture) asAgent(t *testing.T, a *store.Agent, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(a.ID, a.ProjectID,
		[]AgentTokenScope{ScopeAgentCreate}, nil)
	require.NoError(t, err)
	req := f.newRequest(t, method, path, body)
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

func (f *hcAuthzFixture) asBroker(t *testing.T, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	req := f.newRequest(t, method, path, body)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "hcauthz-nonce-" + uuid.New().String()
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

func (f *hcAuthzFixture) anonymous(t *testing.T, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	return f.serve(f.newRequest(t, method, path, body))
}

func hcAuthzCreateBody(name, scope, scopeID string) CreateHarnessConfigRequest {
	return CreateHarnessConfigRequest{
		Name:    name,
		Harness: "claude",
		Scope:   scope,
		ScopeID: scopeID,
		Config:  &store.HarnessConfigData{Image: hcAuthzAttackerImage},
	}
}

// ---------------------------------------------------------------------------
// createHarnessConfig
// ---------------------------------------------------------------------------

// TestHCAuthz_CreateGlobalDenied is the core case. Before the gate, every one
// of these calls returned 201 and planted an attacker-chosen image at global
// scope, where it applies to every project on the hub.
func TestHCAuthz_CreateGlobalDenied(t *testing.T) {
	f := hcAuthzSetup(t)

	cases := []struct {
		name string
		do   func(body interface{}) *httptest.ResponseRecorder
		want int
	}{
		{"unrelated user", func(b interface{}) *httptest.ResponseRecorder {
			return f.asUser(t, f.outsdr, http.MethodPost, "/api/v1/harness-configs", b)
		}, http.StatusForbidden},
		// An agent is not a UserIdentity, and the global arm has no agent
		// branch — global harness configs are not an agent's to create. The
		// 401 rather than 403 is inherited verbatim from the delete arm this
		// helper was lifted from; it denies either way.
		{"cross-project agent", func(b interface{}) *httptest.ResponseRecorder {
			return f.asAgent(t, f.stranger, http.MethodPost, "/api/v1/harness-configs", b)
		}, http.StatusUnauthorized},
		{"in-project agent", func(b interface{}) *httptest.ResponseRecorder {
			return f.asAgent(t, f.insider, http.MethodPost, "/api/v1/harness-configs", b)
		}, http.StatusUnauthorized},
		{"broker", func(b interface{}) *httptest.ResponseRecorder {
			return f.asBroker(t, http.MethodPost, "/api/v1/harness-configs", b)
		}, http.StatusUnauthorized},
		{"unauthenticated", func(b interface{}) *httptest.ResponseRecorder {
			return f.anonymous(t, http.MethodPost, "/api/v1/harness-configs", b)
		}, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "hcauthz-global-" + tc.name
			rec := tc.do(hcAuthzCreateBody(name, store.HarnessConfigScopeGlobal, ""))
			require.Equal(t, tc.want, rec.Code, "body=%s", rec.Body.String())
			f.requireNoConfigNamed(t, name)
		})
	}
}

// TestHCAuthz_CreateGlobalDefaultsAreGatedToo. Scope is optional in the request
// body and an omitted scope resolves to global — the most privileged of the
// three. The gate runs after that defaulting, so an omitted scope is refused
// exactly as an explicit "global" is. Gate before the defaulting and this test
// fails, because the empty string would take the default arm.
func TestHCAuthz_CreateGlobalDefaultsAreGatedToo(t *testing.T) {
	f := hcAuthzSetup(t)

	rec := f.asUser(t, f.outsdr, http.MethodPost, "/api/v1/harness-configs",
		hcAuthzCreateBody("hcauthz-implicit-global", "", ""))
	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	f.requireNoConfigNamed(t, "hcauthz-implicit-global")
}

// TestHCAuthz_CreateUnknownScopeDenied. Scope is a free string from the request
// body. A value no arm names must be refused by the default arm, not admitted
// by falling past the switch.
func TestHCAuthz_CreateUnknownScopeDenied(t *testing.T) {
	f := hcAuthzSetup(t)

	for _, scope := range []string{"hub", "organization", "GLOBAL", "../global"} {
		t.Run(scope, func(t *testing.T) {
			name := "hcauthz-unknown-" + scope
			rec := f.asUser(t, f.admin, http.MethodPost, "/api/v1/harness-configs",
				hcAuthzCreateBody(name, scope, ""))
			require.Equal(t, http.StatusForbidden, rec.Code,
				"an unrecognised scope must be denied by the default arm, even for "+
					"an admin; body=%s", rec.Body.String())
			f.requireNoConfigNamed(t, name)
		})
	}
}

// TestHCAuthz_CreateProjectScoped covers the project arm across caller kinds.
func TestHCAuthz_CreateProjectScoped(t *testing.T) {
	f := hcAuthzSetup(t)

	t.Run("cross-project agent denied", func(t *testing.T) {
		rec := f.asAgent(t, f.stranger, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-xproj", store.HarnessConfigScopeProject, f.projA.ID))
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "within their own project")
		f.requireNoConfigNamed(t, "hcauthz-xproj")
	})

	t.Run("unrelated user denied", func(t *testing.T) {
		rec := f.asUser(t, f.outsdr, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-outsider-proj", store.HarnessConfigScopeProject, f.projA.ID))
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		f.requireNoConfigNamed(t, "hcauthz-outsider-proj")
	})

	t.Run("unauthenticated denied", func(t *testing.T) {
		rec := f.anonymous(t, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-anon-proj", store.HarnessConfigScopeProject, f.projA.ID))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
		f.requireNoConfigNamed(t, "hcauthz-anon-proj")
	})

	// Positive controls. Without these the denials above are also satisfied by
	// an endpoint that refuses everyone, which is a different bug.
	t.Run("in-project agent allowed", func(t *testing.T) {
		rec := f.asAgent(t, f.insider, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-insider-proj", store.HarnessConfigScopeProject, f.projA.ID))
		require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	})

	t.Run("project owner allowed", func(t *testing.T) {
		rec := f.asUser(t, f.admin, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-owner-proj", store.HarnessConfigScopeProject, f.projA.ID))
		require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	})
}

// TestHCAuthz_CreateGlobalAllowedForAdmin is the global arm's positive control.
func TestHCAuthz_CreateGlobalAllowedForAdmin(t *testing.T) {
	f := hcAuthzSetup(t)

	rec := f.asUser(t, f.admin, http.MethodPost, "/api/v1/harness-configs",
		hcAuthzCreateBody("hcauthz-admin-global", store.HarnessConfigScopeGlobal, ""))
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
}

// TestHCAuthz_CreateUserScoped. A user-scoped config with no target named is
// the caller's own and is allowed; one naming another user is not.
func TestHCAuthz_CreateUserScoped(t *testing.T) {
	f := hcAuthzSetup(t)

	t.Run("for self allowed", func(t *testing.T) {
		rec := f.asUser(t, f.outsdr, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-self-user", store.HarnessConfigScopeUser, ""))
		require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	})

	t.Run("for another user denied", func(t *testing.T) {
		rec := f.asUser(t, f.outsdr, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-other-user", store.HarnessConfigScopeUser, f.admin.ID))
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		f.requireNoConfigNamed(t, "hcauthz-other-user")
	})

	t.Run("agent denied", func(t *testing.T) {
		rec := f.asAgent(t, f.insider, http.MethodPost, "/api/v1/harness-configs",
			hcAuthzCreateBody("hcauthz-agent-user", store.HarnessConfigScopeUser, ""))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
		f.requireNoConfigNamed(t, "hcauthz-agent-user")
	})
}

// ---------------------------------------------------------------------------
// updateHarnessConfig / patchHarnessConfig
// ---------------------------------------------------------------------------

// TestHCAuthz_UpdateRewritesImageDenied. This is the reason update had to be
// gated in the same commit as create: PUT rewrites Config.Image on a config
// that already exists, so a gate on create alone would have relocated the
// attacker-image effect rather than removed it. The assertion is on the stored
// record, not the status code.
func TestHCAuthz_UpdateRewritesImageDenied(t *testing.T) {
	f := hcAuthzSetup(t)
	hc := f.seedConfig(t, "hcauthz-target", store.HarnessConfigScopeGlobal, "", "")
	path := "/api/v1/harness-configs/" + hc.ID

	poisoned := *hc
	poisoned.Config = &store.HarnessConfigData{Image: hcAuthzAttackerImage}

	cases := []struct {
		name string
		do   func() *httptest.ResponseRecorder
		want int
	}{
		{"unrelated user", func() *httptest.ResponseRecorder {
			return f.asUser(t, f.outsdr, http.MethodPut, path, poisoned)
		}, http.StatusForbidden},
		{"cross-project agent", func() *httptest.ResponseRecorder {
			return f.asAgent(t, f.stranger, http.MethodPut, path, poisoned)
		}, http.StatusUnauthorized},
		{"broker", func() *httptest.ResponseRecorder {
			return f.asBroker(t, http.MethodPut, path, poisoned)
		}, http.StatusUnauthorized},
		{"unauthenticated", func() *httptest.ResponseRecorder {
			return f.anonymous(t, http.MethodPut, path, poisoned)
		}, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.do()
			require.Equal(t, tc.want, rec.Code, "body=%s", rec.Body.String())
			f.requireImageUnchanged(t, hc.ID)
		})
	}
}

// TestHCAuthz_UpdateAuthorizesStoredScopeNotSubmittedScope. The body of a PUT
// carries a Scope field. Authorizing on the submitted scope would let a caller
// who may write project-scoped configs in their own project name that scope in
// the body and rewrite a GLOBAL record. The gate reads the scope off the stored
// record instead.
func TestHCAuthz_UpdateAuthorizesStoredScopeNotSubmittedScope(t *testing.T) {
	f := hcAuthzSetup(t)
	hc := f.seedConfig(t, "hcauthz-global-target", store.HarnessConfigScopeGlobal, "", "")

	// The stored record is global. The caller claims project scope on projA,
	// where their agent identity is legitimately allowed to write.
	spoof := *hc
	spoof.Scope = store.HarnessConfigScopeProject
	spoof.ScopeID = f.projA.ID
	spoof.Config = &store.HarnessConfigData{Image: hcAuthzAttackerImage}

	rec := f.asAgent(t, f.insider, http.MethodPut, "/api/v1/harness-configs/"+hc.ID, spoof)
	require.NotEqual(t, http.StatusOK, rec.Code,
		"the submitted scope must not be the one authorized; body=%s", rec.Body.String())
	f.requireImageUnchanged(t, hc.ID)
}

// TestHCAuthz_PatchDenied. PATCH cannot reach Config.Image today, but it
// addresses the same record through the same route, and which fields its
// anonymous struct happens to list is not an authorization boundary.
func TestHCAuthz_PatchDenied(t *testing.T) {
	f := hcAuthzSetup(t)
	hc := f.seedConfig(t, "hcauthz-patch-target", store.HarnessConfigScopeGlobal, "", "")
	path := "/api/v1/harness-configs/" + hc.ID
	body := map[string]string{"name": "renamed-by-attacker"}

	rec := f.asUser(t, f.outsdr, http.MethodPatch, path, body)
	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())

	got, err := f.store.GetHarnessConfig(context.Background(), hc.ID)
	require.NoError(t, err)
	require.Equal(t, hc.Name, got.Name, "a refused PATCH renamed the record anyway")
}

// TestHCAuthz_UpdateAndPatchAllowedForAdmin is the positive control for both
// verbs: the gate denies the callers above without denying everyone.
func TestHCAuthz_UpdateAndPatchAllowedForAdmin(t *testing.T) {
	f := hcAuthzSetup(t)
	hc := f.seedConfig(t, "hcauthz-admin-target", store.HarnessConfigScopeGlobal, "", "")
	path := "/api/v1/harness-configs/" + hc.ID

	updated := *hc
	updated.Description = "updated by admin"
	rec := f.asUser(t, f.admin, http.MethodPut, path, updated)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	rec = f.asUser(t, f.admin, http.MethodPatch, path, map[string]string{"displayName": "Renamed"})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
}

// ---------------------------------------------------------------------------
// handleHarnessConfigClone — the missing arms
// ---------------------------------------------------------------------------

// TestHCAuthz_CloneFallsThroughSwitch is the OR3 regression. The old switch had
// exactly two arms; a destination scope matching neither reached the create
// with nothing checked. Both cases below produced a 201 before the default arm
// existed.
func TestHCAuthz_CloneFallsThroughSwitch(t *testing.T) {
	f := hcAuthzSetup(t)
	source := f.seedConfig(t, "hcauthz-clone-src", store.HarnessConfigScopeGlobal, "", "")
	path := "/api/v1/harness-configs/" + source.ID + "/clone"

	t.Run("unknown destination scope", func(t *testing.T) {
		rec := f.asUser(t, f.outsdr, http.MethodPost, path, CloneTemplateRequest{
			Name: "hcauthz-clone-unknown", Scope: "hub",
		})
		require.Equal(t, http.StatusForbidden, rec.Code,
			"a scope no arm names must hit the default arm; body=%s", rec.Body.String())
		f.requireNoConfigNamed(t, "hcauthz-clone-unknown")
	})

	t.Run("user scope naming another user", func(t *testing.T) {
		rec := f.asUser(t, f.outsdr, http.MethodPost, path, CloneTemplateRequest{
			Name: "hcauthz-clone-otheruser", Scope: store.HarnessConfigScopeUser,
			ScopeID: f.admin.ID,
		})
		require.Equal(t, http.StatusForbidden, rec.Code,
			"the user arm the old switch lacked must deny a clone into another "+
				"user's scope; body=%s", rec.Body.String())
		f.requireNoConfigNamed(t, "hcauthz-clone-otheruser")
	})

	// Positive control for the arm that was added: cloning to one's own user
	// scope is ordinary and stays allowed.
	t.Run("user scope for self allowed", func(t *testing.T) {
		rec := f.asUser(t, f.outsdr, http.MethodPost, path, CloneTemplateRequest{
			Name: "hcauthz-clone-self", Scope: store.HarnessConfigScopeUser,
		})
		require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	})
}

// TestHCAuthz_CloneGlobalStillDenied pins that lifting the switch into the
// shared helper did not weaken the two arms clone already had.
func TestHCAuthz_CloneGlobalStillDenied(t *testing.T) {
	f := hcAuthzSetup(t)
	source := f.seedConfig(t, "hcauthz-clone-src2", store.HarnessConfigScopeGlobal, "", "")
	path := "/api/v1/harness-configs/" + source.ID + "/clone"

	rec := f.asUser(t, f.outsdr, http.MethodPost, path, CloneTemplateRequest{
		Name: "hcauthz-clone-global", Scope: store.HarnessConfigScopeGlobal,
	})
	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	f.requireNoConfigNamed(t, "hcauthz-clone-global")

	rec = f.asAgent(t, f.stranger, http.MethodPost, path, CloneTemplateRequest{
		Name: "hcauthz-clone-xproj", Scope: store.HarnessConfigScopeProject,
		ScopeID: f.projA.ID,
	})
	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	f.requireNoConfigNamed(t, "hcauthz-clone-xproj")
}

// TestHCAuthz_DeleteUnchangedByExtraction. deleteHarnessConfig was rewritten to
// call the extracted helper. It is the one endpoint here whose behaviour was
// already correct, so these assertions exist to prove the extraction changed
// nothing rather than to close a hole.
func TestHCAuthz_DeleteUnchangedByExtraction(t *testing.T) {
	f := hcAuthzSetup(t)

	t.Run("unrelated user denied", func(t *testing.T) {
		hc := f.seedConfig(t, "hcauthz-del-1", store.HarnessConfigScopeGlobal, "", "")
		rec := f.asUser(t, f.outsdr, http.MethodDelete, "/api/v1/harness-configs/"+hc.ID, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "delete global resources",
			"the extraction must preserve the message text as well as the decision")
		_, err := f.store.GetHarnessConfig(context.Background(), hc.ID)
		require.NoError(t, err, "a refused DELETE removed the record anyway")
	})

	t.Run("cross-project agent denied", func(t *testing.T) {
		hc := f.seedConfig(t, "hcauthz-del-2", store.HarnessConfigScopeProject, f.projA.ID, "")
		rec := f.asAgent(t, f.stranger, http.MethodDelete, "/api/v1/harness-configs/"+hc.ID, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "within their own project")
	})

	t.Run("admin allowed", func(t *testing.T) {
		hc := f.seedConfig(t, "hcauthz-del-3", store.HarnessConfigScopeGlobal, "", "")
		rec := f.asUser(t, f.admin, http.MethodDelete, "/api/v1/harness-configs/"+hc.ID, nil)
		require.Equal(t, http.StatusNoContent, rec.Code, "body=%s", rec.Body.String())
	})

	// The three subtests above exercise the global and project arms. They do
	// not touch the user arm or the default arm — which are the two arms the
	// extraction ADDS to what delete's caller reaches, and therefore the two
	// this test is least entitled to leave unmeasured. A test that pins half a
	// switch is evidence about half a switch.

	t.Run("user-scoped: another user's config denied", func(t *testing.T) {
		hc := f.seedConfig(t, "hcauthz-del-4", store.HarnessConfigScopeUser, "",
			f.admin.ID)
		rec := f.asUser(t, f.outsdr, http.MethodDelete, "/api/v1/harness-configs/"+hc.ID, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "another user's harness config")
		_, err := f.store.GetHarnessConfig(context.Background(), hc.ID)
		require.NoError(t, err, "a refused DELETE removed the record anyway")
	})

	// The next two are the dead-deny arm documented at authorizeHarnessConfigScope:
	// HarnessConfig.OwnerID is never populated, so a stored user-scoped record
	// names no owner and nobody can prove they are it. Admin is included
	// precisely because the answer is the same for admin — this arm compares
	// identity, it does not consult role — and that is the surprising half.
	t.Run("user-scoped: owner unrecorded denies an ordinary user", func(t *testing.T) {
		hc := f.seedConfig(t, "hcauthz-del-5", store.HarnessConfigScopeUser, "", "")
		rec := f.asUser(t, f.outsdr, http.MethodDelete, "/api/v1/harness-configs/"+hc.ID, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "another user's harness config")
	})

	t.Run("user-scoped: owner unrecorded denies an admin too", func(t *testing.T) {
		hc := f.seedConfig(t, "hcauthz-del-6", store.HarnessConfigScopeUser, "", "")
		rec := f.asUser(t, f.admin, http.MethodDelete, "/api/v1/harness-configs/"+hc.ID, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "another user's harness config")
		_, err := f.store.GetHarnessConfig(context.Background(), hc.ID)
		require.NoError(t, err, "a refused DELETE removed the record anyway")
	})

	t.Run("unknown scope denied by the default arm", func(t *testing.T) {
		hc := f.seedConfig(t, "hcauthz-del-7", "organization", "", "")
		rec := f.asUser(t, f.admin, http.MethodDelete, "/api/v1/harness-configs/"+hc.ID, nil)
		require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "Delete is not supported for this resource scope",
			"the default arm must name the refused verb, and admin must not bypass it")
		_, err := f.store.GetHarnessConfig(context.Background(), hc.ID)
		require.NoError(t, err, "a refused DELETE removed the record anyway")
	})
}

// TestHCAuthz_UpdateCannotPromoteScope is the red-without-fix control for the
// scope-promotion hole. Before updateHarnessConfig preserved Scope, ScopeID and
// OwnerID from the stored record, this exact request returned 200 and the store
// then held a GLOBAL config carrying the attacker's image — the end state that
// gating create was supposed to prevent, reached by PUT instead of POST.
//
// The status code is the weakest part of this test and is asserted last on
// purpose. What matters is the store read-back: a handler that answers 200 and
// silently declines the move is correct here, and a handler that answers 200
// and performs it is the vulnerability. Only the read-back separates them.
func TestHCAuthz_UpdateCannotPromoteScope(t *testing.T) {
	f := hcAuthzSetup(t)
	seeded := f.seedConfig(t, "hcauthz-promote", store.HarnessConfigScopeProject, f.projA.ID, "")

	promoted := *seeded
	promoted.Scope = store.HarnessConfigScopeGlobal
	promoted.ScopeID = ""
	promoted.OwnerID = "hcauthz-not-a-real-owner"
	promoted.Config = &store.HarnessConfigData{Image: hcAuthzAttackerImage}

	rec := f.asAgent(t, f.insider, http.MethodPut,
		"/api/v1/harness-configs/"+seeded.ID, promoted)

	after, err := f.store.GetHarnessConfig(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.Equal(t, store.HarnessConfigScopeProject, after.Scope,
		"a PUT promoted a project-scoped config to global (status was %d)", rec.Code)
	require.Equal(t, f.projA.ID, after.ScopeID,
		"a PUT moved the config out of its project (status was %d)", rec.Code)
	require.Equal(t, "", after.OwnerID,
		"a PUT set an owner the store never populates, which would flip the "+
			"dead-deny user arm into a live allow for whoever was named")

	// The caller IS authorized on this record's real scope, so the write of the
	// fields it may write is expected to succeed. This is what makes the
	// assertions above meaningful rather than incidental: the request was not
	// refused, it was performed within the scope it was authorized for.
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
}

// TestHCAuthz_UpdateStillWritesWithinScope is the positive control for the
// preservation above: pinning "the scope did not change" is satisfiable by an
// update that does nothing at all, so this asserts a legitimate in-scope edit
// still lands.
func TestHCAuthz_UpdateStillWritesWithinScope(t *testing.T) {
	f := hcAuthzSetup(t)
	seeded := f.seedConfig(t, "hcauthz-inscope", store.HarnessConfigScopeProject, f.projA.ID, "")

	edit := *seeded
	edit.DisplayName = "renamed by an authorized caller"

	rec := f.asAgent(t, f.insider, http.MethodPut,
		"/api/v1/harness-configs/"+seeded.ID, edit)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	after, err := f.store.GetHarnessConfig(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed by an authorized caller", after.DisplayName,
		"preserving scope must not have turned update into a no-op")
	require.Equal(t, store.HarnessConfigScopeProject, after.Scope)
	require.Equal(t, f.projA.ID, after.ScopeID)
}
