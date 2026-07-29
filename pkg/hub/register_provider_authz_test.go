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
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Regression tests for the provider write inside handleProjectRegister.
//
// The /providers gate does not cover register: register reaches
// AddProjectProvider by a different route, with a caller-supplied LocalPath,
// and it had no authorization at all — it read the caller's identity only to
// attribute a newly created project.
//
// THE STATE THAT MAKES THIS BITE, which is why this file builds an unusual
// fixture rather than reusing an existing one. A victim owns a pre-existing
// git-backed project that DECLARES a shared directory but has no provider on
// the hub's embedded broker yet: a co-located broker that has not attached, or
// a project used remotely. The owner's own read of that shared directory
// answers 409, because nothing is serving it. That gap is the opening. An
// outsider registers the victim's project ID, names the embedded broker and a
// directory of their own, and a provider row is created FOR THE EMBEDDED
// BROKER pointing at attacker storage — after which the owner's read of their
// own shared directory answers 200 with the attacker's bytes.
//
// So the assertion that matters here is not the status code and not even the
// absence of the provider row, though both are checked. It is that the owner's
// shared-dir read stays 409 rather than flipping to 200-with-attacker-bytes:
// the whole point of the write is what the victim reads afterwards.
//
// WHY THE PRESERVATION BLOCKS DO NOT COVER THIS. register keeps the stored
// LocalPath when the project already existed and that broker is already a
// provider, which does stop an outsider overwriting a live workspace. It does
// nothing here, because this is an attach: there is no row to preserve. Both
// register branches — the current brokerId flow and the deprecated by-name
// broker flow — reach the same write, so both are driven below.
//
// SCOPE. These tests cover WHO may write a provider through register. Register
// still writes the supplied path without the absolute-path, existing-directory
// and system-directory validation that the /providers attach applies; that is
// the LocalPath question and is deliberately not answered here.
//
// Test naming: everything file-local is prefixed rpGate.

type rpGateFixture struct {
	srv   *Server
	store store.Store

	owner  *store.User
	outsdr *store.User
	admin  *store.User

	// victim is the project in the reachable state: shared dir declared, no
	// embedded-broker provider. live is the same owner's project that already
	// has one, for the preservation control.
	victim *store.Project
	live   *store.Project
	other  *store.Project

	// stranger belongs to other, so it is an agent outside victim.
	stranger *store.Agent

	embeddedBrokerID   string
	embeddedBrokerName string

	// caller is a broker used as an authenticating identity.
	caller       *store.RuntimeBroker
	brokerSecret []byte

	// attackerWorkspace is what an attacker names as the project's workspace;
	// attackerSharedDir is where the shared-dir read would land if the write
	// succeeded, and it holds rpGateEvilName. ownerWorkspace is the legitimate
	// equivalent, used by the positive controls.
	attackerWorkspace string
	attackerSharedDir string
	ownerWorkspace    string
	liveWorkspace     string
}

const (
	rpGateSharedDir = "team"
	rpGateEvilName  = "evil.txt"
	rpGateEvilBody  = "attacker-controlled-bytes-must-not-be-served"
)

func rpGateSetup(t *testing.T) *rpGateFixture {
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
	f := &rpGateFixture{srv: srv, store: s}

	f.owner = &store.User{
		ID: tid("rpgate-owner"), Email: "rpgate-owner@example.com",
		DisplayName: "Owner", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.owner))

	f.outsdr = &store.User{
		ID: tid("rpgate-outsider"), Email: "rpgate-outsider@example.com",
		DisplayName: "Outsider", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.outsdr))

	f.admin = &store.User{
		ID: tid("rpgate-admin"), Email: "rpgate-admin@example.com",
		DisplayName: "Admin", Role: store.UserRoleAdmin, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.admin))

	f.victim = &store.Project{
		ID: tid("rpgate-victim"), Name: "RP Gate Victim", Slug: tid("rpgate-victim"),
		OwnerID: f.owner.ID, CreatedBy: f.owner.ID,
		GitRemote:  "https://example.invalid/victim.git",
		SharedDirs: []api.SharedDir{{Name: rpGateSharedDir}},
	}
	require.NoError(t, s.CreateProject(ctx, f.victim))

	f.live = &store.Project{
		ID: tid("rpgate-live"), Name: "RP Gate Live", Slug: tid("rpgate-live"),
		OwnerID: f.owner.ID, CreatedBy: f.owner.ID,
		GitRemote:  "https://example.invalid/live.git",
		SharedDirs: []api.SharedDir{{Name: rpGateSharedDir}},
	}
	require.NoError(t, s.CreateProject(ctx, f.live))

	f.other = &store.Project{
		ID: tid("rpgate-other"), Name: "RP Gate Other", Slug: tid("rpgate-other"),
		OwnerID: f.owner.ID, CreatedBy: f.owner.ID,
		GitRemote: "https://example.invalid/other.git",
	}
	require.NoError(t, s.CreateProject(ctx, f.other))

	f.stranger = &store.Agent{
		ID: tid("rpgate-stranger"), Slug: tid("rpgate-stranger"), Name: "rpgate-stranger",
		ProjectID: f.other.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.owner.ID, OwnerID: f.owner.ID,
		Ancestry: []string{f.owner.ID},
	}
	require.NoError(t, s.CreateAgent(ctx, f.stranger))

	// A provider's LocalPath is the project workspace, and shared dirs resolve
	// to <parent of workspace>/shared-dirs/<name>. The attacker's tree is fully
	// built, canary included, because the attack is only interesting if the
	// bytes are really there to be served.
	root := t.TempDir()
	f.attackerWorkspace = filepath.Join(root, "attacker", "workspace")
	f.attackerSharedDir = filepath.Join(root, "attacker", "shared-dirs", rpGateSharedDir)
	f.ownerWorkspace = filepath.Join(root, "owner", "workspace")
	f.liveWorkspace = filepath.Join(root, "live", "workspace")
	for _, d := range []string{
		f.attackerWorkspace, f.attackerSharedDir, f.ownerWorkspace, f.liveWorkspace,
		filepath.Join(root, "owner", "shared-dirs", rpGateSharedDir),
		filepath.Join(root, "live", "shared-dirs", rpGateSharedDir),
	} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(f.attackerSharedDir, rpGateEvilName),
		[]byte(rpGateEvilBody), 0o644))

	f.embeddedBrokerID = tid("rpgate-embedded-broker")
	f.embeddedBrokerName = "rpgate-embedded"
	srv.SetEmbeddedBrokerID(f.embeddedBrokerID)
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: f.embeddedBrokerID, Name: f.embeddedBrokerName, Slug: f.embeddedBrokerName,
		Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
	}))

	// victim deliberately has NO provider. live has one, so the preservation
	// control has something to preserve.
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: f.live.ID, BrokerID: f.embeddedBrokerID,
		BrokerName: f.embeddedBrokerName, LocalPath: f.liveWorkspace,
		LinkedBy: f.owner.ID,
	}))

	f.brokerSecret = []byte("rpgate-secret-key-32-bytes!!!!!!")
	f.caller = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "rpgate-caller", Slug: "rpgate-caller",
		Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, f.caller))
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  f.caller.ID,
		SecretKey: f.brokerSecret,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))

	return f
}

// ---------------------------------------------------------------------------
// Requests. Rule 4: no identity is injected into a context here. Every request
// goes through srv.Handler() carrying the credential the middleware would
// really have produced for that caller.
// ---------------------------------------------------------------------------

func (f *rpGateFixture) newRequest(method, path string, body any) *http.Request {
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

func (f *rpGateFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *rpGateFixture) asUser(t *testing.T, u *store.User, method, path string,
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

func (f *rpGateFixture) asAgent(t *testing.T, a *store.Agent, method, path string,
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

func (f *rpGateFixture) asBroker(t *testing.T, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	req := f.newRequest(method, path, body)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "rpgate-nonce-" + uuid.New().String()
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

func (f *rpGateFixture) anonymous(method, path string, body any) *httptest.ResponseRecorder {
	return f.serve(f.newRequest(method, path, body))
}

// ---------------------------------------------------------------------------
// Payloads and observations.
// ---------------------------------------------------------------------------

const rpGateRegisterPath = "/api/v1/projects/register"

// byBrokerID is the current flow; byBrokerName is the deprecated one, which
// resolves the same embedded broker through GetRuntimeBrokerByName and reaches
// the same write. Both are driven everywhere below, because a gate on one of
// them is not a gate.
func (f *rpGateFixture) byBrokerID(p *store.Project, path string) RegisterProjectRequest {
	return RegisterProjectRequest{
		ID: p.ID, Name: p.Name, GitRemote: p.GitRemote,
		BrokerID: f.embeddedBrokerID, Path: path,
	}
}

func (f *rpGateFixture) byBrokerName(p *store.Project, path string) RegisterProjectRequest {
	return RegisterProjectRequest{
		ID: p.ID, Name: p.Name, GitRemote: p.GitRemote,
		Broker: &RegisterProjectBrokerInfo{Name: f.embeddedBrokerName},
		Path:   path,
	}
}

func (f *rpGateFixture) embeddedProvider(t *testing.T, p *store.Project) (*store.ProjectProvider, bool) {
	t.Helper()
	got, err := f.store.GetProjectProvider(context.Background(), p.ID, f.embeddedBrokerID)
	if err != nil {
		return nil, false
	}
	return got, true
}

// requireVictimUnserved is the assertion this file exists for, and every case
// below runs it BEFORE checking the status code. The order is deliberate: the
// status is the least interesting thing a refused register produces. A change
// that returned 403 while still writing the row would be caught here, and a
// change that altered only the status would fail on something that reads as a
// status change rather than as a breach. It checks the store first and then the
// sink, because those two also mean different things: a row means the gate did
// not stop the write, and a served body means the write reached the victim's
// reader even if the row looks innocuous.
func (f *rpGateFixture) requireVictimUnserved(t *testing.T, because string) {
	t.Helper()

	// The subject is proven alive before anything below counts as a refusal.
	// Every assertion in this helper is also satisfied by a victim that stopped
	// existing: GetProjectProvider finds no row for a deleted project, and the
	// shared-dir read answers 404. A request that destroyed the project it was
	// refused on would therefore have passed here, which was measured with a
	// throwaway probe rather than reasoned about. Checking the project first
	// means a destroyed subject fails as a destroyed subject instead of reading
	// as a clean refusal.
	_, err := f.store.GetProject(context.Background(), f.victim.ID)
	require.NoError(t, err,
		"the victim project no longer exists, so every assertion below would "+
			"pass by absence rather than by refusal (%s)", because)

	_, ok := f.embeddedProvider(t, f.victim)
	require.False(t, ok,
		"a refused register created a provider row on the victim's embedded "+
			"broker, which is the write the gate exists to stop (%s)", because)

	rec := f.asUser(t, f.owner, http.MethodGet,
		"/api/v1/projects/"+f.victim.ID+"/shared-dirs/"+rpGateSharedDir+
			"/files/"+rpGateEvilName, nil)
	// Equality against the measured baseline, not "anything but 200". A
	// not-200 assertion is satisfied by a 404 from a subject that no longer
	// exists and by a route that broke for everyone, so it can only ever
	// convict the exact success code. 409 is what rpGateVictimBaseline reads
	// off this same request before the attack runs, so this is that
	// observation repeating, not a constant picked to make the test pass.
	require.Equal(t, http.StatusConflict, rec.Code,
		"the victim's own shared-dir read no longer answers the 409 baseline "+
			"after a refused register: 200 means the attacker's workspace was "+
			"served, and any other code means the subject or the route moved "+
			"underneath this assertion (%s)", because)
	require.NotContains(t, rec.Body.String(), rpGateEvilBody,
		"the victim was served attacker-controlled bytes from their own shared "+
			"directory after a refused register (%s)", because)
}

// requireNoBrokerSecret is a TRIPWIRE, and it cannot fire as things stand. Be
// clear about that rather than let it read as coverage: the deprecated by-name
// branch used to return a broker HMAC secret in the response body alongside the
// provider link, that emission is gone (#591), and the response field is
// omitempty and never populated — so the substring it looks for cannot appear
// in any register response, refused or allowed. It convicts nothing today, and
// it would only start convicting if something repopulated the field AND an arm
// in this file reached the branch. Neither holds, and the second is the easier
// one to overlook: this helper's single call site is the refused-caller matrix
// below, whose arms are all stopped by the gate before the branch runs. So
// repopulating the field alone leaves this file green — measured by aid-rev1,
// who restored the emission on the limb these routes take and watched
// TestRPGate pass while a live secret went out in the body.
//
// It is kept for that case and not deleted, but the real disclosure coverage is
// register_secret_disclosure_test.go, which drives the callers the gate ALLOWS
// through to the branch — including the limb that creates a broker — and checks
// the stored secrets and the body by value. A refused caller never reaches the
// branch at all, so value assertions here would duplicate that file's work
// while implying this file measures something it does not.
func (f *rpGateFixture) requireNoBrokerSecret(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.NotContains(t, rec.Body.String(), "secretKey",
		"a refused register returned a broker secret to the caller")
}

// rpGateVictimBaseline records the state the attack starts from, so that
// "still 409" below cannot be confused with "409 for some other reason".
func rpGateVictimBaseline(t *testing.T, f *rpGateFixture) int {
	t.Helper()
	rec := f.asUser(t, f.owner, http.MethodGet,
		"/api/v1/projects/"+f.victim.ID+"/shared-dirs/"+rpGateSharedDir+
			"/files/"+rpGateEvilName, nil)
	return rec.Code
}

// ---------------------------------------------------------------------------
// The attack.
// ---------------------------------------------------------------------------

// TestRPGate_CrossTenantProviderWriteDenied drives the three caller classes
// that could reach the write, across both register branches. The victim's
// baseline is asserted inside each case rather than once, because each case
// gets a fresh fixture and a baseline that silently stopped being 409 would
// make every "unserved" assertion below it meaningless.
func TestRPGate_CrossTenantProviderWriteDenied(t *testing.T) {
	type caller struct {
		name string
		want int
		do   func(*testing.T, *rpGateFixture, any) *httptest.ResponseRecorder
	}
	callers := []caller{
		{"unrelated user", http.StatusForbidden,
			func(t *testing.T, f *rpGateFixture, body any) *httptest.ResponseRecorder {
				return f.asUser(t, f.outsdr, http.MethodPost, rpGateRegisterPath, body)
			}},
		{
			// 404, not 403: requireProjectVisibleToAgent runs before the
			// authorization decision, so an agent outside the project cannot
			// use the refusal to confirm the project exists.
			"cross-project agent", http.StatusNotFound,
			func(t *testing.T, f *rpGateFixture, body any) *httptest.ResponseRecorder {
				return f.asAgent(t, f.stranger, http.MethodPost, rpGateRegisterPath, body)
			}},
		{"broker", http.StatusForbidden,
			func(t *testing.T, f *rpGateFixture, body any) *httptest.ResponseRecorder {
				return f.asBroker(t, http.MethodPost, rpGateRegisterPath, body)
			}},
	}

	routes := []struct {
		name string
		body func(*rpGateFixture) any
	}{
		{"brokerId flow", func(f *rpGateFixture) any {
			return f.byBrokerID(f.victim, f.attackerWorkspace)
		}},
		{"deprecated broker-by-name flow", func(f *rpGateFixture) any {
			return f.byBrokerName(f.victim, f.attackerWorkspace)
		}},
	}

	for _, rt := range routes {
		for _, c := range callers {
			t.Run(rt.name+"/"+c.name, func(t *testing.T) {
				f := rpGateSetup(t)
				require.Equal(t, http.StatusConflict, rpGateVictimBaseline(t, f),
					"the victim must start with nothing serving its shared dir, "+
						"or this case is not measuring the attack")

				rec := c.do(t, f, rt.body(f))
				f.requireVictimUnserved(t, rt.name+"/"+c.name)
				f.requireNoBrokerSecret(t, rec)
				require.Equal(t, c.want, rec.Code, "body=%s", rec.Body.String())
			})
		}
	}
}

// TestRPGate_UnauthenticatedDenied records that anonymous callers are stopped
// by the authentication middleware, upstream of this gate. This was already
// true and is the one refusal here that is not evidence about the gate.
func TestRPGate_UnauthenticatedDenied(t *testing.T) {
	f := rpGateSetup(t)
	rec := f.anonymous(http.MethodPost, rpGateRegisterPath,
		f.byBrokerID(f.victim, f.attackerWorkspace))
	require.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
	f.requireVictimUnserved(t, "anonymous")
}

// ---------------------------------------------------------------------------
// Positive controls. A gate that refused everyone would satisfy every case
// above and break registration, which is how a project gets a workspace at all.
// ---------------------------------------------------------------------------

// TestRPGate_OwnerMayAttachOnRegister is the control the attack case inverts:
// the same request, from the project's owner, must attach and must make the
// shared dir readable. Without it, "the victim's read stays 409" would be
// satisfied by a gate that stopped registration working for anybody.
func TestRPGate_OwnerMayAttachOnRegister(t *testing.T) {
	for _, rt := range []struct {
		name string
		body func(*rpGateFixture) any
	}{
		{"brokerId flow", func(f *rpGateFixture) any {
			return f.byBrokerID(f.victim, f.ownerWorkspace)
		}},
		{"deprecated broker-by-name flow", func(f *rpGateFixture) any {
			return f.byBrokerName(f.victim, f.ownerWorkspace)
		}},
	} {
		t.Run(rt.name, func(t *testing.T) {
			f := rpGateSetup(t)
			rec := f.asUser(t, f.owner, http.MethodPost, rpGateRegisterPath, rt.body(f))
			require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

			got, ok := f.embeddedProvider(t, f.victim)
			require.True(t, ok, "the owner's own register did not attach the provider")
			require.Equal(t, f.ownerWorkspace, got.LocalPath,
				"the owner's own register attached the wrong workspace")
		})
	}
}

func TestRPGate_AdminMayAttachOnRegister(t *testing.T) {
	f := rpGateSetup(t)
	rec := f.asUser(t, f.admin, http.MethodPost, rpGateRegisterPath,
		f.byBrokerID(f.victim, f.ownerWorkspace))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, ok := f.embeddedProvider(t, f.victim)
	require.True(t, ok, "an admin register did not attach the provider")
	require.Equal(t, f.ownerWorkspace, got.LocalPath)
}

// TestRPGate_NewProjectRegistrationStillAttaches pins the boundary of the
// gate's condition. Registering a project that does not exist yet is a
// creation: the caller becomes its owner, and the provider write that follows
// is theirs to make. The gate must not fire there, and a caller with no
// standing on anything else must still be able to do it.
func TestRPGate_NewProjectRegistrationStillAttaches(t *testing.T) {
	f := rpGateSetup(t)
	newID := tid("rpgate-brand-new")

	rec := f.asUser(t, f.outsdr, http.MethodPost, rpGateRegisterPath,
		RegisterProjectRequest{
			ID: newID, Name: "RP Gate Brand New",
			GitRemote: "https://example.invalid/brand-new.git",
			BrokerID:  f.embeddedBrokerID, Path: f.ownerWorkspace,
		})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, err := f.store.GetProjectProvider(context.Background(), newID, f.embeddedBrokerID)
	require.NoError(t, err, "registering a new project no longer attaches its provider")
	require.Equal(t, f.ownerWorkspace, got.LocalPath)
}

// TestRPGate_OwnerReRegisterPreservesWorkspace is the preservation control. It
// still holds, and it is recorded here as behaviour that survived the gate
// rather than as the thing protecting anyone: it never stopped an attach, only
// an overwrite, which is why the attack above went around it.
func TestRPGate_OwnerReRegisterPreservesWorkspace(t *testing.T) {
	f := rpGateSetup(t)

	rec := f.asUser(t, f.owner, http.MethodPost, rpGateRegisterPath,
		f.byBrokerID(f.live, f.attackerWorkspace))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, ok := f.embeddedProvider(t, f.live)
	require.True(t, ok, "the owner's re-register removed their own provider")
	require.Equal(t, f.liveWorkspace, got.LocalPath,
		"re-register overwrote the project's existing workspace path")
}

// TestRPGate_CrossTenantCannotOverwriteLiveWorkspace drives the same three
// callers at the project that DOES have a provider. Preservation already kept
// the stored path here, so the status codes are the only thing that change —
// recorded so that a later edit to the preservation block, which is not an
// authorization boundary and could reasonably be removed, cannot quietly open
// an overwrite path.
func TestRPGate_CrossTenantCannotOverwriteLiveWorkspace(t *testing.T) {
	for _, c := range []struct {
		name string
		want int
		do   func(*testing.T, *rpGateFixture, any) *httptest.ResponseRecorder
	}{
		{"unrelated user", http.StatusForbidden,
			func(t *testing.T, f *rpGateFixture, body any) *httptest.ResponseRecorder {
				return f.asUser(t, f.outsdr, http.MethodPost, rpGateRegisterPath, body)
			}},
		{"cross-project agent", http.StatusNotFound,
			func(t *testing.T, f *rpGateFixture, body any) *httptest.ResponseRecorder {
				return f.asAgent(t, f.stranger, http.MethodPost, rpGateRegisterPath, body)
			}},
		{"broker", http.StatusForbidden,
			func(t *testing.T, f *rpGateFixture, body any) *httptest.ResponseRecorder {
				return f.asBroker(t, http.MethodPost, rpGateRegisterPath, body)
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := rpGateSetup(t)
			rec := c.do(t, f, f.byBrokerID(f.live, f.attackerWorkspace))

			// Store first, status second, for the reason given on
			// requireVictimUnserved.
			got, ok := f.embeddedProvider(t, f.live)
			require.True(t, ok, "a refused register removed the project's provider")
			require.Equal(t, f.liveWorkspace, got.LocalPath,
				"a refused register overwrote the project's workspace path")
			require.Equal(t, c.want, rec.Code, "body=%s", rec.Body.String())
		})
	}
}
