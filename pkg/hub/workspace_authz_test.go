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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Regression tests for the project workspace, shared-directory and WebDAV file
// routes.
//
// These are NOT a #591 idiom conversion. There was no guard here to convert:
// measured on an unmodified tree at 9a85f085, project_workspace_handlers.go
// (976 lines) and project_webdav.go both contained zero authorization calls of
// any kind. Every caller the middleware would authenticate at all — a
// cross-project agent, a user with no relationship to the project, a runtime
// broker — could list, read, write and delete files in an arbitrary project's
// workspace and shared directories, and download a zip of either.
//
// The gate is authorizeProjectWorkspaceAccess (project_workspace_handlers.go),
// called from the five entry handlers in that file and from
// handleProjectWebDAV. The two files must agree, because /workspace/files and
// /dav/ serve the same bytes out of the same directory returned by the same
// resolveProjectWebDAVPath: a gate on one and not the other is not a partial
// fix, it is no fix.
//
// The four caller classes the brief requires, plus a broker:
//
//	cross-project agent  -> 404 (isolation runs before authorization, so the
//	                        response does not confirm the project exists)
//	unrelated user       -> 403
//	broker               -> 403 (CheckAccess has no broker arm)
//	project owner        -> succeeds — the positive control, without which
//	                        every row above is satisfiable by a handler that
//	                        refuses everyone
//	unauthenticated      -> 401
//
// and one more that is not in the brief but is the load-bearing half of the
// read/write split: an agent INSIDE the project reads (the agent project read
// baseline, authz.go:239) and does not write.
//
// Where a route mutates, the test asserts on the filesystem as well as on the
// status code. A 403 returned after the file was already deleted is still a
// vulnerability, and a status-code-only assertion cannot tell the two apart.
//
// On the case count: `go test -v` reports 115 lines here (9 tests, 106
// subtests), and that is not 115 cases of gate coverage. Neutering
// authorizeProjectWorkspaceAccess to `return true` turns 68 of them red; the
// 47 survivors decompose, measured rather than estimated, as 17 reported lines
// of positive-control allows that a disabled gate necessarily still passes,
// 16 unauthenticated lines whose 401 comes from the auth middleware upstream
// of the gate, the 9 read cases of TestWSGate_InProjectAgentMayReadNotWrite
// (its write half does go red), and 5 from
// TestWSGate_WebDAVAndWorkspaceAgreePerCaller for three reasons rather than
// two: owner_read and owner_write are positive controls, unauthenticated_read
// and unauthenticated_write are middleware 401s, and in-project_agent_read
// survives for the read-baseline reason — the same reason as the 9 read cases
// named just above. Every figure in this paragraph counts reported lines, not
// leaves: a top-level test and each of its subtests each contribute a line, so
// e.g. the 17 are 15 subtests plus a parent line plus a whole-test line. Before
// that test was given per-caller expected verdicts, all 12 of its cases
// survived the neuter and the figure was 60. The number that means something
// is the mutation result, not the total.
//
// Test naming: everything file-local is prefixed wsGate.

type wsGateFixture struct {
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

	// workspacePath is what resolveProjectWebDAVPath returns for projA, and
	// sharedDirPath is what resolveSharedDirPath returns for projA's "team"
	// shared dir. Both hold a canary file named wsGateCanaryName.
	workspacePath string
	sharedDirPath string
}

const (
	wsGateCanaryName    = "canary.txt"
	wsGateCanaryContent = "canary-contents-must-not-leak"

	// wsGateIntruderName is the file an unauthorized write would create. No
	// test creates it deliberately; its existence after a denied request is
	// the failure.
	wsGateIntruderName = "pwned.txt"
)

// wsGateSetup builds a git-anchored project whose workspace is served from a
// co-located (embedded) broker's LocalPath. That shape is used rather than a
// hub-managed project because it puts the served directory in a t.TempDir()
// this test owns, instead of under the real ~/.scion.
func wsGateSetup(t *testing.T) *wsGateFixture {
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
	f := &wsGateFixture{srv: srv, store: s}

	f.owner = &store.User{
		ID: tid("wsgate-owner"), Email: "wsgate-owner@example.com",
		DisplayName: "Owner", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.owner))

	// A member with no relationship to either project.
	f.outsdr = &store.User{
		ID: tid("wsgate-outsider"), Email: "wsgate-outsider@example.com",
		DisplayName: "Outsider", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.outsdr))

	f.projA = &store.Project{
		ID: tid("wsgate-pa"), Name: "WS Gate A", Slug: "wsgate-pa",
		OwnerID: f.owner.ID, GitRemote: "https://example.invalid/a.git",
		SharedDirs: []api.SharedDir{{Name: "team"}},
	}
	require.NoError(t, s.CreateProject(ctx, f.projA))

	f.projB = &store.Project{
		ID: tid("wsgate-pb"), Name: "WS Gate B", Slug: "wsgate-pb",
		OwnerID: f.owner.ID, GitRemote: "https://example.invalid/b.git",
	}
	require.NoError(t, s.CreateProject(ctx, f.projB))

	// The embedded broker's LocalPath is <root>/workspace, so
	// config.GetSharedDirsBasePath resolves shared dirs to <root>/shared-dirs.
	// Both stay inside the temp root.
	root := t.TempDir()
	f.workspacePath = filepath.Join(root, "workspace")
	f.sharedDirPath = filepath.Join(root, "shared-dirs", "team")
	require.NoError(t, os.MkdirAll(f.workspacePath, 0o755))
	require.NoError(t, os.MkdirAll(f.sharedDirPath, 0o755))

	embeddedBrokerID := tid("wsgate-embedded-broker")
	srv.SetEmbeddedBrokerID(embeddedBrokerID)
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: embeddedBrokerID, Name: "wsgate-embedded", Slug: "wsgate-embedded",
	}))
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: f.projA.ID, BrokerID: embeddedBrokerID,
		BrokerName: "wsgate-embedded", LocalPath: f.workspacePath,
	}))

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
	f.insider = mk("wsgate-insider", f.projA.ID)
	f.stranger = mk("wsgate-stranger", f.projB.ID)

	// A broker is neither a UserIdentity nor an AgentIdentity, and CheckAccess
	// has no broker arm — so it is denied by the gate rather than skipped by
	// it. Before the gate existed it was served.
	f.brokerSecret = []byte("wsgate-secret-key-32-bytes!!!!!!")
	f.broker = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "wsgate-broker", Slug: "wsgate-broker",
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

	f.seedCanaries(t)
	return f
}

// seedCanaries (re)writes the canary file into both served directories. Called
// at setup and again before each subtest that might have deleted it.
func (f *wsGateFixture) seedCanaries(t *testing.T) {
	t.Helper()
	for _, dir := range []string{f.workspacePath, f.sharedDirPath} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, wsGateCanaryName),
			[]byte(wsGateCanaryContent), 0o644))
		_ = os.Remove(filepath.Join(dir, wsGateIntruderName))
	}
}

// requireCanariesIntact asserts that nothing the request did reached the disk:
// the canary still exists with its original contents, and no intruder file was
// created. This is the assertion a handler that answers 403 *after* doing the
// work cannot satisfy.
func (f *wsGateFixture) requireCanariesIntact(t *testing.T) {
	t.Helper()
	for _, dir := range []string{f.workspacePath, f.sharedDirPath} {
		got, err := os.ReadFile(filepath.Join(dir, wsGateCanaryName))
		require.NoError(t, err, "canary in %s was deleted by a request the gate refused", dir)
		require.Equal(t, wsGateCanaryContent, string(got),
			"canary in %s was overwritten by a request the gate refused", dir)
		_, err = os.Stat(filepath.Join(dir, wsGateIntruderName))
		require.True(t, os.IsNotExist(err),
			"a request the gate refused created %s in %s", wsGateIntruderName, dir)
	}
}

// ---------------------------------------------------------------------------
// Request helpers.
//
// Rule 4: none of these inject an identity into the request context. These
// requests go through srv.Handler(), where the auth middleware establishes its
// own identity and discards anything the test put there. Each helper supplies
// the credential the middleware would genuinely have produced for that caller.
// ---------------------------------------------------------------------------

func (f *wsGateFixture) newRequest(method, path string, body []byte) *http.Request {
	var rdr io.Reader = bytes.NewReader(body)
	req := httptest.NewRequest(method, path, rdr)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (f *wsGateFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *wsGateFixture) asUser(t *testing.T, u *store.User, method, path string, body []byte) *httptest.ResponseRecorder {
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

func (f *wsGateFixture) asAgent(t *testing.T, a *store.Agent, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(a.ID, a.ProjectID, nil, nil)
	require.NoError(t, err)

	req := f.newRequest(method, path, body)
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

func (f *wsGateFixture) asBroker(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := f.newRequest(method, path, body)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "wsgate-nonce-" + uuid.New().String()
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

func (f *wsGateFixture) anonymous(_ *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	return f.serve(f.newRequest(method, path, body))
}

// ---------------------------------------------------------------------------
// Route tables.
// ---------------------------------------------------------------------------

type wsGateRoute struct {
	name   string
	method string
	path   string
	body   []byte

	// okCode is what the project owner gets: the positive control. It is a
	// specific code, not "anything but 403", so that a handler which starts
	// refusing the owner for some unrelated reason fails the test instead of
	// quietly passing it.
	okCode int
}

func wsGateJSONWrite(content string) []byte {
	return []byte(`{"content":"` + content + `"}`)
}

// readRoutes are the read-class routes: workspaceActionForMethod maps their
// verbs to ActionRead, so an agent inside the project passes them on the
// authz.go:239 read baseline.
func (f *wsGateFixture) readRoutes() []wsGateRoute {
	base := "/api/v1/projects/" + f.projA.ID
	return []wsGateRoute{
		{name: "workspace file list", method: http.MethodGet, path: base + "/workspace/files", okCode: http.StatusOK},
		{name: "workspace file download", method: http.MethodGet, path: base + "/workspace/files/" + wsGateCanaryName, okCode: http.StatusOK},
		{name: "workspace archive", method: http.MethodGet, path: base + "/workspace/archive", okCode: http.StatusOK},
		{name: "shared dir file list", method: http.MethodGet, path: base + "/shared-dirs/team/files", okCode: http.StatusOK},
		{name: "shared dir file download", method: http.MethodGet, path: base + "/shared-dirs/team/files/" + wsGateCanaryName, okCode: http.StatusOK},
		{name: "shared dir archive", method: http.MethodGet, path: base + "/shared-dirs/team/archive", okCode: http.StatusOK},
		{name: "webdav download", method: http.MethodGet, path: base + "/dav/" + wsGateCanaryName, okCode: http.StatusOK},
		{name: "webdav propfind", method: "PROPFIND", path: base + "/dav/", okCode: http.StatusMultiStatus},
	}
}

// writeRoutes are the write-class routes. workspaceActionForMethod maps their
// verbs to ActionUpdate, which is not read-class, so the read baseline does not
// reach them and an in-project agent is denied along with everyone else who is
// not authorized to update the project.
func (f *wsGateFixture) writeRoutes() []wsGateRoute {
	base := "/api/v1/projects/" + f.projA.ID
	return []wsGateRoute{
		{name: "workspace file write", method: http.MethodPut, path: base + "/workspace/files/" + wsGateIntruderName,
			body: wsGateJSONWrite("owned"), okCode: http.StatusOK},
		{name: "workspace file delete", method: http.MethodDelete, path: base + "/workspace/files/" + wsGateCanaryName,
			okCode: http.StatusNoContent},
		{name: "shared dir file write", method: http.MethodPut, path: base + "/shared-dirs/team/files/" + wsGateIntruderName,
			body: wsGateJSONWrite("owned"), okCode: http.StatusOK},
		{name: "shared dir file delete", method: http.MethodDelete, path: base + "/shared-dirs/team/files/" + wsGateCanaryName,
			okCode: http.StatusNoContent},
		{name: "webdav put", method: http.MethodPut, path: base + "/dav/" + wsGateIntruderName,
			body: []byte("owned"), okCode: http.StatusCreated},
		{name: "webdav delete", method: http.MethodDelete, path: base + "/dav/" + wsGateCanaryName,
			okCode: http.StatusNoContent},
		// projA is not a shared-workspace project, so an authorized caller gets
		// 409 from the handler's own check. That is still a positive control:
		// 409 is a code the gate cannot produce, so reaching it proves the
		// request was authorized.
		{name: "workspace pull", method: http.MethodPost, path: base + "/workspace/pull",
			okCode: http.StatusConflict},
	}
}

func (f *wsGateFixture) allRoutes() []wsGateRoute {
	return append(f.readRoutes(), f.writeRoutes()...)
}

// ---------------------------------------------------------------------------
// The four required caller classes, plus broker and in-project agent.
// ---------------------------------------------------------------------------

// TestWSGate_OwnerAllowed is the positive control. Without it, every denial
// test below is also satisfied by a handler that refuses everybody, which is a
// different bug rather than a fix.
func TestWSGate_OwnerAllowed(t *testing.T) {
	f := wsGateSetup(t)

	for _, rt := range f.allRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			f.seedCanaries(t)
			rec := f.asUser(t, f.owner, rt.method, rt.path, rt.body)
			require.Equal(t, rt.okCode, rec.Code,
				"project owner must still be served; body=%s", rec.Body.String())
		})
	}
}

// TestWSGate_OwnerReadsRealContent pins that the read routes actually serve the
// bytes, so the denial tests are denying access to something real rather than
// to an empty or missing workspace.
func TestWSGate_OwnerReadsRealContent(t *testing.T) {
	f := wsGateSetup(t)

	rec := f.asUser(t, f.owner, http.MethodGet,
		"/api/v1/projects/"+f.projA.ID+"/workspace/files/"+wsGateCanaryName, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), wsGateCanaryContent)
}

// TestWSGate_UnrelatedUserDenied covers the caller kind that makes this not a
// #591 idiom-1 conversion. The retiring idiom gates only agents; an unrelated
// USER is a plain authenticated user and would sail through it. Here they are
// denied on every route, read and write alike.
func TestWSGate_UnrelatedUserDenied(t *testing.T) {
	f := wsGateSetup(t)

	for _, rt := range f.allRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			f.seedCanaries(t)
			rec := f.asUser(t, f.outsdr, rt.method, rt.path, rt.body)
			require.Equal(t, http.StatusForbidden, rec.Code,
				"a user with no relationship to the project must be denied; body=%s",
				rec.Body.String())
			f.requireCanariesIntact(t)
			require.NotContains(t, rec.Body.String(), wsGateCanaryContent)
		})
	}
}

// TestWSGate_CrossProjectAgentDenied pins 404 rather than 403.
// requireProjectVisibleToAgent runs BEFORE s.authorize precisely so that the
// response does not confirm to an agent outside the project that the project
// exists. Swap the two calls and this test fails with 403.
func TestWSGate_CrossProjectAgentDenied(t *testing.T) {
	f := wsGateSetup(t)

	for _, rt := range f.allRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			f.seedCanaries(t)
			rec := f.asAgent(t, f.stranger, rt.method, rt.path, rt.body)
			require.Equal(t, http.StatusNotFound, rec.Code,
				"an agent in another project must get 404, not a 403 that confirms "+
					"the project exists; body=%s", rec.Body.String())
			f.requireCanariesIntact(t)
			require.NotContains(t, rec.Body.String(), wsGateCanaryContent)
		})
	}
}

// TestWSGate_BrokerDenied. A broker authenticates successfully — the middleware
// establishes a broker identity for any path once the HMAC verifies — and then
// fails authorization, because CheckAccess has no broker arm and its default
// arm denies. Before the gate there was nothing to fail: the broker was served.
func TestWSGate_BrokerDenied(t *testing.T) {
	f := wsGateSetup(t)

	for _, rt := range f.allRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			f.seedCanaries(t)
			rec := f.asBroker(t, rt.method, rt.path, rt.body)
			require.Equal(t, http.StatusForbidden, rec.Code,
				"a broker must be denied; body=%s", rec.Body.String())
			f.requireCanariesIntact(t)
			require.NotContains(t, rec.Body.String(), wsGateCanaryContent)
		})
	}
}

// TestWSGate_UnauthenticatedDenied. The nil-identity case: s.authorize answers
// 401 rather than 403 for a caller that presented no credential at all.
func TestWSGate_UnauthenticatedDenied(t *testing.T) {
	f := wsGateSetup(t)

	for _, rt := range f.allRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			f.seedCanaries(t)
			rec := f.anonymous(t, rt.method, rt.path, rt.body)
			require.Equal(t, http.StatusUnauthorized, rec.Code,
				"an unauthenticated caller must be denied; body=%s", rec.Body.String())
			f.requireCanariesIntact(t)
			require.NotContains(t, rec.Body.String(), wsGateCanaryContent)
		})
	}
}

// TestWSGate_InProjectAgentMayReadNotWrite is the read/write split.
//
// The read half is the agent project read baseline (authz.go:239): an agent may
// perform read-class actions on resources in its own project. Gating reads with
// ActionRead therefore grants an in-project agent nothing it did not already
// have — its workspace is mounted into its own filesystem regardless — while
// still denying every other caller kind.
//
// The write half is the point of choosing ActionUpdate rather than ActionRead
// for mutating verbs: ActionUpdate is not read-class, so the baseline does not
// reach it and the agent is denied. If someone widens isReadClassAction to
// include ActionUpdate, or gates these routes with ActionRead for uniformity,
// the write half of this test fails.
func TestWSGate_InProjectAgentMayReadNotWrite(t *testing.T) {
	f := wsGateSetup(t)

	t.Run("reads", func(t *testing.T) {
		for _, rt := range f.readRoutes() {
			t.Run(rt.name, func(t *testing.T) {
				f.seedCanaries(t)
				rec := f.asAgent(t, f.insider, rt.method, rt.path, rt.body)
				require.Equal(t, rt.okCode, rec.Code,
					"an agent inside the project reads on the authz.go:239 baseline; body=%s",
					rec.Body.String())
			})
		}
	})

	t.Run("writes", func(t *testing.T) {
		for _, rt := range f.writeRoutes() {
			t.Run(rt.name, func(t *testing.T) {
				f.seedCanaries(t)
				rec := f.asAgent(t, f.insider, rt.method, rt.path, rt.body)
				require.Equal(t, http.StatusForbidden, rec.Code,
					"ActionUpdate is not read-class, so the in-project read baseline "+
						"must not carry an agent into a write; body=%s", rec.Body.String())
				f.requireCanariesIntact(t)
			})
		}
	})
}

// TestWSGate_SharedDirExistenceNotDisclosed. The declared-dir check in
// handleSharedDirFiles and handleProjectSharedDirArchive answers "Shared
// directory not found" for an undeclared name and something else for a declared
// one, which is an existence oracle over a project's shared dirs. The gate runs
// before it, so an unauthorized caller gets the same answer for both and learns
// nothing.
func TestWSGate_SharedDirExistenceNotDisclosed(t *testing.T) {
	f := wsGateSetup(t)
	base := "/api/v1/projects/" + f.projA.ID

	for _, dir := range []string{"team", "no-such-dir"} {
		t.Run(dir, func(t *testing.T) {
			rec := f.asUser(t, f.outsdr, http.MethodGet, base+"/shared-dirs/"+dir+"/files", nil)
			require.Equal(t, http.StatusForbidden, rec.Code)
			require.NotContains(t, strings.ToLower(rec.Body.String()), "shared directory",
				"the response distinguishes a declared shared dir from an undeclared "+
					"one, which is the oracle the gate ordering exists to close")
		})
	}
}

// TestWSGate_WebDAVAndWorkspaceAgreePerCaller. The two endpoints serve the same
// bytes from the same resolveProjectWebDAVPath. If they ever disagree about a
// caller, the stricter one is decorative — the caller just uses the other.
//
// Agreement alone is not enough to assert, and this test used to assert only
// that. Two endpoints that both serve everyone agree perfectly: neutering the
// gate left all twelve cases green, because both arms went permissive
// together. So each caller now also declares the verdict it is supposed to
// receive, and the equality check is what remains of the original point. The
// expected verdicts are written as classify() strings rather than raw codes so
// that the WebDAV and JSON success codes (201 vs 200) can still differ.
func TestWSGate_WebDAVAndWorkspaceAgreePerCaller(t *testing.T) {
	f := wsGateSetup(t)
	base := "/api/v1/projects/" + f.projA.ID

	type caller struct {
		name string
		do   func(method, path string, body []byte) *httptest.ResponseRecorder

		// wantRead and wantWrite are classify() verdicts. The in-project agent
		// is the row that differs between them: the authz.go:239 read baseline
		// lets it read its own project and ActionUpdate is not read-class.
		wantRead  string
		wantWrite string
	}
	callers := []caller{
		{"owner", func(m, p string, b []byte) *httptest.ResponseRecorder { return f.asUser(t, f.owner, m, p, b) },
			"allowed", "allowed"},
		{"unrelated user", func(m, p string, b []byte) *httptest.ResponseRecorder { return f.asUser(t, f.outsdr, m, p, b) },
			"refused:403", "refused:403"},
		{"cross-project agent", func(m, p string, b []byte) *httptest.ResponseRecorder { return f.asAgent(t, f.stranger, m, p, b) },
			"refused:404", "refused:404"},
		{"in-project agent", func(m, p string, b []byte) *httptest.ResponseRecorder { return f.asAgent(t, f.insider, m, p, b) },
			"allowed", "refused:403"},
		{"broker", func(m, p string, b []byte) *httptest.ResponseRecorder { return f.asBroker(t, m, p, b) },
			"refused:403", "refused:403"},
		{"unauthenticated", func(m, p string, b []byte) *httptest.ResponseRecorder { return f.anonymous(t, m, p, b) },
			"refused:401", "refused:401"},
	}

	for _, c := range callers {
		t.Run(c.name+" read", func(t *testing.T) {
			f.seedCanaries(t)
			viaFiles := f.classify(c.do(http.MethodGet, base+"/workspace/files/"+wsGateCanaryName, nil))
			viaDAV := f.classify(c.do(http.MethodGet, base+"/dav/"+wsGateCanaryName, nil))
			require.Equal(t, viaFiles, viaDAV,
				"/workspace/files and /dav/ serve the same bytes and must reach the "+
					"same verdict for %s", c.name)
			require.Equal(t, c.wantRead, viaFiles,
				"both endpoints agreed, but on the wrong verdict for %s", c.name)
		})
		t.Run(c.name+" write", func(t *testing.T) {
			f.seedCanaries(t)
			viaFiles := f.classify(c.do(http.MethodPut, base+"/workspace/files/"+wsGateIntruderName, wsGateJSONWrite("x")))
			f.seedCanaries(t)
			viaDAV := f.classify(c.do(http.MethodPut, base+"/dav/"+wsGateIntruderName, []byte("x")))
			require.Equal(t, viaFiles, viaDAV,
				"/workspace/files and /dav/ write the same bytes and must reach the "+
					"same verdict for %s", c.name)
			require.Equal(t, c.wantWrite, viaFiles,
				"both endpoints agreed, but on the wrong verdict for %s", c.name)
		})
	}
}

// classify reduces a response to "allowed" or the refusal status, so the two
// endpoints can be compared without requiring their success codes to match
// (a WebDAV PUT answers 201, the JSON write route answers 200).
func (f *wsGateFixture) classify(rec *httptest.ResponseRecorder) string {
	switch rec.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return "refused:" + strconv.Itoa(rec.Code)
	default:
		return "allowed"
	}
}
