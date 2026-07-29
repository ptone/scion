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
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Regression tests for the READ gate on the matched branch of
// handleProjectRegister — the branch reached when the request named a project
// that already exists.
//
// WHAT THESE MEASURE. That branch answers with the matched project's stored
// record and backfills that project's groups, so it is a read: a caller who may
// not read the project must be refused there, must be refused in the shape of a
// project that is not there, and must leave no backfill write behind. The three
// caller classes who MAY read it must still get through, or the gate has broken
// the flow it is guarding. Ownership is separately constrained (#591) and is
// asserted here as a standing invariant, not as this gate's job.
//
// WHAT THE GATE IS. Not a new policy — the project READ baseline this branch
// already ships on the same object, via the same helper and in the same position
// getProject uses: authorizeProjectReadNoOracle first, then the identical two
// backfill calls. Measured before landing, the three matched arms of register and
// create came out cell-for-cell identical to GET /projects/{id} across all eight
// caller classes on the same tree.
//
// THE 404s BELOW ARE THE POINT, not an accident of the helper. Every denial arm
// renders the body the missing-project path renders — 404 / not_found /
// "Resource not found" — so a caller cannot tell a project they may not read from
// one that does not exist. Asserting only the status would miss a regression that
// kept 404 but restored a distinguishable message, so the code and message are
// asserted too.
//
// A PLAIN MEMBER GETS 404 HERE, and that is deliberate rather than overlooked. A
// member of the project's members group cannot read the project at this sha
// either — the members-group policy grants agent creation, not project read — so
// the gate propagates a denial that already existed on GET rather than inventing
// one. Read access is the prerequisite for announcing or linking a project. The
// policy-grant question is tracked separately and is NOT settled here; this test
// pins today's behaviour so that a later change to it is a visible decision.
//
// Test naming: everything file-local is prefixed rrGate.

type rrGateFixture struct {
	srv   *Server
	store store.Store

	owner    *store.User
	member   *store.User
	outsider *store.User
	admin    *store.User

	// proj is created through the API, so its groups, policy and owner record are
	// whatever production emits. legacy is written straight to the store with NO
	// groups at all, which is what makes the backfill WRITE observable: if the
	// refused call had reached the backfill, the members group would exist.
	proj   *store.Project
	other  *store.Project
	legacy *store.Project

	inAgent *store.Agent // belongs to proj
	xAgent  *store.Agent // belongs to other

	broker       *store.RuntimeBroker
	brokerSecret []byte
}

const rrGateRegisterPath = "/api/v1/projects/register"

func rrGateSetup(t *testing.T) *rrGateFixture {
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
	f := &rrGateFixture{srv: srv, store: s}

	mk := func(name, role string) *store.User {
		u := &store.User{
			ID: tid("rrgate-" + name), Email: "rrgate-" + name + "@example.com",
			DisplayName: name, Role: role, Status: "active", Created: time.Now(),
		}
		require.NoError(t, s.CreateUser(ctx, u))
		return u
	}
	f.owner = mk("owner", store.UserRoleMember)
	f.member = mk("member", store.UserRoleMember)
	f.outsider = mk("outsider", store.UserRoleMember)
	f.admin = mk("admin", store.UserRoleAdmin)

	// Rule 4: the subjects are built by production, not seeded. Both projects the
	// matrix runs against come out of POST /api/v1/projects as the owner.
	f.proj = f.createProjectAsOwner(t, "RR Gate Subject")
	f.other = f.createProjectAsOwner(t, "RR Gate Other")

	// legacy predates group support: no agents group, no members group, no policy.
	// This is the shape the backfill exists for.
	f.legacy = &store.Project{
		ID: tid("rrgate-legacy"), Name: "RR Gate Legacy", Slug: tid("rrgate-legacy"),
		OwnerID: f.owner.ID, CreatedBy: f.owner.ID,
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateProject(ctx, f.legacy))

	// The plain member: role=member in the project's own members group. The four
	// fields set here are the four production's add-member handler sets
	// (handlers_groups.go), minus AddedBy, which has no occurrence in authz.go or
	// authorize.go and so cannot participate in the decision measured.
	membersGroup, err := s.GetGroupBySlug(ctx, "project:"+f.proj.Slug+":members")
	require.NoError(t, err, "the API-created project has no members group, so the "+
		"member row below would not be measuring membership of anything")
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{
		GroupID:    membersGroup.ID,
		MemberType: store.GroupMemberTypeUser,
		MemberID:   f.member.ID,
		Role:       store.GroupMemberRoleMember,
	}))

	f.inAgent = &store.Agent{
		ID: tid("rrgate-in-agent"), Slug: tid("rrgate-in-agent"), Name: "rrgate-in-agent",
		ProjectID: f.proj.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.owner.ID, OwnerID: f.owner.ID, Ancestry: []string{f.owner.ID},
	}
	require.NoError(t, s.CreateAgent(ctx, f.inAgent))

	f.xAgent = &store.Agent{
		ID: tid("rrgate-x-agent"), Slug: tid("rrgate-x-agent"), Name: "rrgate-x-agent",
		ProjectID: f.other.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.owner.ID, OwnerID: f.owner.ID, Ancestry: []string{f.owner.ID},
	}
	require.NoError(t, s.CreateAgent(ctx, f.xAgent))

	f.brokerSecret = []byte("rrgate-secret-key-32-bytes!!!!!!")
	f.broker = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "rrgate-broker", Slug: "rrgate-broker",
		Status: store.BrokerStatusOnline, Created: time.Now(), Updated: time.Now(),
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

func (f *rrGateFixture) createProjectAsOwner(t *testing.T, name string) *store.Project {
	t.Helper()
	rec := f.asUser(t, f.owner, http.MethodPost, "/api/v1/projects",
		CreateProjectRequest{Name: name})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	var p store.Project
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.NotEmpty(t, p.ID)
	return &p
}

// ---------------------------------------------------------------------------
// Requests. Rule 4: no identity is injected into a context. Every request goes
// through srv.Handler() with the credential the middleware really produces.
// ---------------------------------------------------------------------------

func (f *rrGateFixture) newRequest(method, path string, body any) *http.Request {
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

func (f *rrGateFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *rrGateFixture) asUser(t *testing.T, u *store.User, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName, u.Role, ClientTypeAPI)
	require.NoError(t, err)

	req := f.newRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	return f.serve(req)
}

func (f *rrGateFixture) asAgent(t *testing.T, a *store.Agent, method, path string,
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

func (f *rrGateFixture) asBroker(t *testing.T, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	req := f.newRequest(method, path, body)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "rrgate-nonce-" + uuid.New().String()
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

func (f *rrGateFixture) anonymous(method, path string, body any) *httptest.ResponseRecorder {
	return f.serve(f.newRequest(method, path, body))
}

// registerExisting is the request every case sends: the project's own id and
// name, and nothing else. No broker payload, so the provider-write gate further
// down the handler is not involved — what is measured is the matched branch
// itself.
func rrGateRegisterExisting(p *store.Project) RegisterProjectRequest {
	return RegisterProjectRequest{ID: p.ID, Name: p.Name}
}

// ---------------------------------------------------------------------------
// Observations.
// ---------------------------------------------------------------------------

// rrGateRequireMissingShaped asserts the refusal is byte-shaped like the
// missing-project answer. Status alone is not enough: 404 with a different code
// or message still tells the caller the project is real.
func rrGateRequireMissingShaped(t *testing.T, rec *httptest.ResponseRecorder, because string) {
	t.Helper()
	require.Equal(t, http.StatusNotFound, rec.Code, "%s: body=%s", because, rec.Body.String())

	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), "%s", because)
	require.Equal(t, "not_found", got.Error.Code,
		"%s: the refusal is distinguishable from a missing project by error code", because)
	require.Equal(t, "Resource not found", got.Error.Message,
		"%s: the refusal is distinguishable from a missing project by message", because)
}

// rrGateRequireSubjectIntact proves the subject is still there and still owned by
// the same person. Without it, every denial assertion above would also be
// satisfied by a request that destroyed the project it was refused on.
func (f *rrGateFixture) rrGateRequireSubjectIntact(t *testing.T, p *store.Project, because string) {
	t.Helper()
	got, err := f.store.GetProject(context.Background(), p.ID)
	require.NoError(t, err,
		"%s: the subject project no longer exists, so the assertions above pass by "+
			"absence rather than by refusal", because)
	require.Equal(t, p.OwnerID, got.OwnerID, "%s: the subject changed owner", because)
}

// rrGateOwners returns the user ids recorded with role=owner in the project's
// members group — the set isProjectOwnerOrAdmin consults, i.e. the set that
// decides who has everything.
func (f *rrGateFixture) rrGateOwners(t *testing.T, p *store.Project) []string {
	t.Helper()
	g, err := f.store.GetGroupBySlug(context.Background(), "project:"+p.Slug+":members")
	if err != nil {
		return nil
	}
	members, err := f.store.GetGroupMembers(context.Background(), g.ID)
	require.NoError(t, err)
	var owners []string
	for _, m := range members {
		if m.Role == store.GroupMemberRoleOwner {
			owners = append(owners, m.MemberID)
		}
	}
	return owners
}

func (f *rrGateFixture) rrGateMembersGroupExists(t *testing.T, p *store.Project) bool {
	t.Helper()
	_, err := f.store.GetGroupBySlug(context.Background(), "project:"+p.Slug+":members")
	return err == nil
}

// ---------------------------------------------------------------------------
// The caller matrix on a matched register.
// ---------------------------------------------------------------------------

type rrGateCaller struct {
	name string
	want int
	do   func(*testing.T, *rrGateFixture, any) *httptest.ResponseRecorder
}

func rrGateCallers() []rrGateCaller {
	return []rrGateCaller{
		{"owner", http.StatusOK,
			func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
				return f.asUser(t, f.owner, http.MethodPost, rrGateRegisterPath, b)
			}},
		{"hub admin", http.StatusOK,
			func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
				return f.asUser(t, f.admin, http.MethodPost, rrGateRegisterPath, b)
			}},
		{"in-project agent", http.StatusOK,
			func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
				return f.asAgent(t, f.inAgent, http.MethodPost, rrGateRegisterPath, b)
			}},
		{"plain member", http.StatusNotFound,
			func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
				return f.asUser(t, f.member, http.MethodPost, rrGateRegisterPath, b)
			}},
		{"outsider user", http.StatusNotFound,
			func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
				return f.asUser(t, f.outsider, http.MethodPost, rrGateRegisterPath, b)
			}},
		{"cross-project agent", http.StatusNotFound,
			func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
				return f.asAgent(t, f.xAgent, http.MethodPost, rrGateRegisterPath, b)
			}},
		{"broker", http.StatusNotFound,
			func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
				return f.asBroker(t, http.MethodPost, rrGateRegisterPath, b)
			}},
	}
}

// TestRRGate_MatchedRegisterCallerMatrix is the whole finding in one table. The
// four refusals are what changes; the three 200s are the control that says the
// gate refuses the right people rather than everybody, because "no outsider gets
// the record" is trivially satisfied by breaking re-registration for its owner.
func TestRRGate_MatchedRegisterCallerMatrix(t *testing.T) {
	for _, c := range rrGateCallers() {
		t.Run(c.name, func(t *testing.T) {
			f := rrGateSetup(t)
			rec := c.do(t, f, rrGateRegisterExisting(f.proj))

			if c.want == http.StatusNotFound {
				rrGateRequireMissingShaped(t, rec, c.name)
				require.NotContains(t, rec.Body.String(), f.proj.Slug,
					"%s: the refusal handed back the project's stored slug, which the "+
						"request never supplied", c.name)
			} else {
				require.Equal(t, http.StatusOK, rec.Code, "%s: body=%s", c.name, rec.Body.String())
			}

			// The subject survives, and ownership is unmoved in every arm —
			// allowed and refused alike. This is the #591 grant staying closed.
			f.rrGateRequireSubjectIntact(t, f.proj, c.name)
			require.Equal(t, []string{f.owner.ID}, f.rrGateOwners(t, f.proj),
				"%s: the owner set of the project changed", c.name)
		})
	}
}

// TestRRGate_UnauthenticatedDenied records that anonymous callers are stopped by
// the authentication middleware, upstream of this gate. It was already true and
// is the one refusal here that is not evidence about the gate.
func TestRRGate_UnauthenticatedDenied(t *testing.T) {
	f := rrGateSetup(t)
	rec := f.anonymous(http.MethodPost, rrGateRegisterPath, rrGateRegisterExisting(f.proj))
	require.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
	f.rrGateRequireSubjectIntact(t, f.proj, "anonymous")
}

// ---------------------------------------------------------------------------
// The write. A status code is the least interesting thing this branch produces:
// it also performed the group/policy backfill on the named project. legacy has
// no groups, so whether that write happened is directly observable.
// ---------------------------------------------------------------------------

func TestRRGate_RefusedRegisterDoesNotBackfillNamedProject(t *testing.T) {
	for _, c := range rrGateCallers() {
		if c.want != http.StatusNotFound {
			continue
		}
		if c.name == "plain member" {
			// A plain member of legacy cannot be constructed: membership lives in
			// the members group, and legacy having no members group is the whole
			// point of this subject. Covered on proj in the matrix above.
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			f := rrGateSetup(t)
			require.False(t, f.rrGateMembersGroupExists(t, f.legacy),
				"legacy must start with no members group or this case measures nothing")

			rec := c.do(t, f, rrGateRegisterExisting(f.legacy))
			rrGateRequireMissingShaped(t, rec, c.name)

			require.False(t, f.rrGateMembersGroupExists(t, f.legacy),
				"%s: a refused register still ran the group/policy backfill on a "+
					"project this caller may not read", c.name)
			f.rrGateRequireSubjectIntact(t, f.legacy, c.name)
		})
	}
}

// TestRRGate_AdminRegisterStillBackfills is the positive control for the write.
// The backfill is why the matched branch calls those functions at all, and a gate
// that stopped it for everyone would satisfy every assertion above while leaving
// pre-group projects permanently unrepaired.
func TestRRGate_AdminRegisterStillBackfills(t *testing.T) {
	f := rrGateSetup(t)
	require.False(t, f.rrGateMembersGroupExists(t, f.legacy))

	rec := f.asUser(t, f.admin, http.MethodPost, rrGateRegisterPath,
		rrGateRegisterExisting(f.legacy))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	require.True(t, f.rrGateMembersGroupExists(t, f.legacy),
		"the gate stopped the backfill for a caller it allowed through")
	require.Equal(t, []string{f.owner.ID}, f.rrGateOwners(t, f.legacy),
		"the backfill made someone other than the project's recorded creator an owner")
}

// TestRRGate_NewProjectRegistrationUnaffected pins the boundary of the gate's
// condition. Registering a project that does not exist yet is a creation, it
// reaches a different branch, and a caller with no standing on anything must
// still be able to do it — that is how a project gets registered in the first
// place.
func TestRRGate_NewProjectRegistrationUnaffected(t *testing.T) {
	f := rrGateSetup(t)
	newID := tid("rrgate-brand-new")

	rec := f.asUser(t, f.outsider, http.MethodPost, rrGateRegisterPath,
		RegisterProjectRequest{ID: newID, Name: "RR Gate Brand New"})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, err := f.store.GetProject(context.Background(), newID)
	require.NoError(t, err, "registering a brand-new project no longer creates it")
	require.Equal(t, f.outsider.ID, got.CreatedBy)
}
