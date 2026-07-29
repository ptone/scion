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

// GATE-LIVENESS pin for the identity-kind gates on the group writes and on the
// PTY attach (ptone/scion#591).
//
// THE GAP THIS FILLS. Each of these handlers used to authorize inside
//
//	if userIdent := GetUserIdentityFromContext(ctx); userIdent != nil { ...CheckAccess... }
//
// An agent or broker token satisfies neither UserIdentity nor that nil check, so
// for those callers the whole authorization block was SKIPPED and the privileged
// write ran unauthorized. The gates are now fail-closed: nil identity is refused
// before CheckAccess.
//
// The pre-existing group-write authorization tests (TestGroupUpdateAuthz_NonOwnerDenied
// and siblings) drive a NON-OWNER USER. A user identity is non-nil, so it was denied
// by the buggy code too — those tests stay green with the fix removed. They witness
// user-vs-user policy, not the non-user skip, which is why no user row appears below:
// a user row cannot go red on revert and therefore cannot pin this gate.
//
// WHAT EACH ARM ASSERTS. For every site, with an AGENT token and with an HMAC-signed
// BROKER token — both unrelated to the target — the response must be 403 AND the
// store must be unchanged, re-read afterwards. The status alone is not enough: it is
// the write NOT HAPPENING that the gate exists to guarantee, and only a re-read
// witnesses that.
//
// WHY THERE IS AN ADMIN CONTROL. "The store is unchanged" is vacuous unless the same
// request WOULD have changed it. Each site therefore runs the identical method, path
// and body as a hub admin on a fresh fixture and asserts the mutation LANDS. The
// control establishes request validity only. It is authorized in both the fixed and
// the buggy code, so it stays green on revert and says nothing about the gate.
//
// ON ANONYMOUS. Unauthenticated callers are refused by the auth middleware before any
// handler runs, so a 401 arm would stay 401 with every gate deleted. Anonymous is a
// floor, not a liveness witness, and is deliberately absent here.
//
// WHAT WAS ACTUALLY UNPINNED. The four group sites: measured, the suite stays green
// with all four reverted. The PTY site is NOT in that state — since the attach gate
// became s.authorize (NB-8D7-1), the deny arms of pty_attach_ancestry_test.go already
// go red when it is reverted, and the arm here is a CORRELATED second witness, not the
// only one. It is kept for uniformity across the sites and because it states the bug
// shape (422, not merely "not 200") that the neighbouring file leaves implicit.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// glAttemptedName is the group name an unauthorized caller tries to write. It is a
// UUID rather than a legible string so that finding it in a store row is
// unambiguous evidence of THIS request's write and of nothing else.
var glAttemptedName = tid("gl-attempted-rename")

type gateLivenessFixture struct {
	*groupReadAuthzFixture

	// callerAgent is UNRELATED to every target: it lives in a different project
	// and is absent from ptyTarget's ancestry. Both facts are asserted in setup.
	// This matters most for the PTY site, where an ANCESTOR is legitimately
	// allowed to attach to its own descendant (NB-8D7-1) — a caller chosen
	// carelessly would make the expected 403 wrong rather than the gate broken.
	otherProject *store.Project
	callerAgent  *store.Agent
	callerToken  string

	// victim is a member (role "member") of the fixture group — the removal
	// target. Role "member" and not "owner" on purpose: removeGroupMember refuses
	// to remove a group's last owner with a 400, which would mask a bypass behind
	// an unrelated guard and leave the store untouched for the wrong reason.
	victim *store.User
	// newcomer exists in the store but is NOT a member — the addition target. It
	// must exist, otherwise a bypassing caller would be turned back by member
	// resolution ("user not found") instead of minting the membership.
	newcomer *store.User
	// ptyTarget carries no RuntimeBrokerID, so a caller who gets PAST the PTY gate
	// lands on 422 no_runtime_broker. That 422 is the bug shape for a non-user
	// caller, and it is what the agent and broker arms must NOT see.
	ptyTarget *store.Agent

	originalGroupName string
}

func setupGateLiveness(t *testing.T) *gateLivenessFixture {
	t.Helper()
	// setupGroupReadAuthz already builds the two authenticated non-user callers
	// this pin needs — an agent token and a broker with an active HMAC secret —
	// on a server with broker auth configured.
	base := setupGroupReadAuthz(t)
	ctx := context.Background()

	mkUser := func(name string) *store.User {
		u := &store.User{
			ID:          tid(name),
			Email:       name + "@example.com",
			DisplayName: name,
			Role:        store.UserRoleMember,
			Status:      "active",
			Created:     time.Now(),
		}
		require.NoError(t, base.store.CreateUser(ctx, u))
		ensureHubMembership(ctx, base.store, u.ID)
		return u
	}

	victim := mkUser("gl-victim")
	require.NoError(t, base.store.AddGroupMember(ctx, &store.GroupMember{
		GroupID:    base.group.ID,
		MemberType: store.GroupMemberTypeUser,
		MemberID:   victim.ID,
		Role:       store.GroupMemberRoleMember,
		AddedAt:    time.Now(),
	}))
	newcomer := mkUser("gl-newcomer")

	otherProject := &store.Project{
		ID:      tid("gl-other-project"),
		Name:    "GL Other Project",
		Slug:    "gl-other-project",
		OwnerID: base.owner.ID,
	}
	require.NoError(t, base.store.CreateProject(ctx, otherProject))

	// Scoped for agent management, to show the denial turns on identity KIND and
	// not on a missing scope: a well-scoped agent is refused all the same.
	callerAgent := &store.Agent{
		ID: tid("gl-caller-agent"), Slug: tid("gl-caller-agent"), Name: "GL Caller Agent",
		ProjectID: otherProject.ID, OwnerID: base.owner.ID, CreatedBy: base.owner.ID,
	}
	require.NoError(t, base.store.CreateAgent(ctx, callerAgent))
	callerToken, err := base.srv.GetAgentTokenService().GenerateAgentToken(
		callerAgent.ID, callerAgent.ProjectID,
		[]AgentTokenScope{ScopeAgentCreate, ScopeAgentLifecycle}, nil)
	require.NoError(t, err)

	ptyTarget := &store.Agent{
		ID: tid("gl-pty-target"), Slug: tid("gl-pty-target"), Name: "GL PTY Target",
		ProjectID: base.project.ID, OwnerID: base.owner.ID, CreatedBy: base.owner.ID,
		Ancestry: []string{base.owner.ID},
	}
	require.NoError(t, base.store.CreateAgent(ctx, ptyTarget))

	f := &gateLivenessFixture{
		groupReadAuthzFixture: base,
		otherProject:          otherProject,
		callerAgent:           callerAgent,
		callerToken:           callerToken,
		victim:                victim,
		newcomer:              newcomer,
		ptyTarget:             ptyTarget,
	}

	// Preconditions. Each one is a property some assertion below silently depends
	// on; asserting them here keeps a broken fixture from reading as a pass.
	require.NotContains(t, ptyTarget.Ancestry, callerAgent.ID,
		"the PTY caller must not be an ancestor of the target: an ancestor is ALLOWED to "+
			"attach to its own descendant, so an ancestral caller would make the expected "+
			"403 wrong rather than the gate broken")
	require.Empty(t, ptyTarget.RuntimeBrokerID,
		"the PTY target must have no runtime broker, so that a caller who clears the gate "+
			"lands on 422 no_runtime_broker — that 422 is how a skipped gate announces itself")

	g, err := base.store.GetGroup(ctx, base.group.ID)
	require.NoError(t, err)
	require.NotEmpty(t, g.Name)
	require.NotEqual(t, glAttemptedName, g.Name,
		"the group must not already carry the name an unauthorized caller writes, or the "+
			"unchanged-name assertion would pass without the gate")
	f.originalGroupName = g.Name

	return f
}

func (f *gateLivenessFixture) newRequest(t *testing.T, method, path string, body interface{}) *http.Request {
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

func (f *gateLivenessFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// nonUserCaller is an authenticated identity that is neither a UserIdentity nor
// anonymous — the caller kind the pre-fix idiom skipped.
type nonUserCaller struct {
	name string
	// authenticate adds credentials to an already-built request. It runs last, so
	// the broker signature covers the final method, path and headers.
	authenticate func(t *testing.T, f *gateLivenessFixture, req *http.Request)
}

func nonUserCallers() []nonUserCaller {
	return []nonUserCaller{
		{
			name: "agent",
			authenticate: func(t *testing.T, f *gateLivenessFixture, req *http.Request) {
				t.Helper()
				req.Header.Set("X-Scion-Agent-Token", f.callerToken)
			},
		},
		{
			// A real HMAC-signed broker request. A broker satisfies neither
			// UserIdentity nor AgentIdentity, so CheckAccess answers "unknown
			// identity type" for it — but only if the gate hands it to CheckAccess
			// at all, which is exactly what the pre-fix nil check prevented.
			name: "broker",
			authenticate: func(t *testing.T, f *gateLivenessFixture, req *http.Request) {
				t.Helper()
				timestamp := strconv.FormatInt(time.Now().Unix(), 10)
				nonce := "gl-nonce-" + uuid.New().String()
				req.Header.Set(HeaderBrokerID, f.broker.ID)
				req.Header.Set(HeaderTimestamp, timestamp)
				req.Header.Set(HeaderNonce, nonce)

				svc := f.srv.brokerAuthService
				require.NotNil(t, svc, "broker auth service must be configured")
				mac := hmac.New(sha256.New, f.brokerSecret)
				mac.Write(svc.buildCanonicalString(req, timestamp, nonce))
				req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			},
		},
	}
}

// gateSite is one guarded handler entry, described by the request that reaches it
// and by the store state that must survive an unauthorized attempt.
type gateSite struct {
	name   string
	method string
	path   func(f *gateLivenessFixture) string
	body   func(f *gateLivenessFixture) interface{}
	// websocket marks the PTY route, which the dispatcher only routes to
	// handleAgentPTY on an upgrade request.
	websocket bool

	// bypassCode is the status a caller sees when the gate is SKIPPED — the
	// pre-fix behaviour. It is not asserted (it cannot occur while the gate
	// stands); it is quoted in the failure message so a future failure is legible
	// as the regression it is.
	bypassCode int
	// successCode is what an authorized hub admin gets for the same request.
	successCode int

	// assertUnwritten re-reads the store and asserts the write did NOT happen. It
	// runs both BEFORE the unauthorized request (so a fixture that never held the
	// precondition cannot read as a pass) and AFTER it.
	assertUnwritten func(t *testing.T, f *gateLivenessFixture)
	// assertWritten re-reads the store and asserts the write DID happen. Only the
	// admin control runs it.
	assertWritten func(t *testing.T, f *gateLivenessFixture)
}

func gateLivenessSites() []gateSite {
	groupPath := func(f *gateLivenessFixture) string { return "/api/v1/groups/" + f.group.ID }

	getMembership := func(t *testing.T, f *gateLivenessFixture, memberID string) (*store.GroupMember, error) {
		t.Helper()
		return f.store.GetGroupMembership(context.Background(),
			f.group.ID, store.GroupMemberTypeUser, memberID)
	}

	return []gateSite{
		{
			// SITE 1 — handlers_groups.go updateGroup, ActionUpdate.
			name:   "updateGroup",
			method: http.MethodPatch,
			path:   groupPath,
			body: func(f *gateLivenessFixture) interface{} {
				return UpdateGroupRequest{Name: glAttemptedName}
			},
			bypassCode:  http.StatusOK,
			successCode: http.StatusOK,
			assertUnwritten: func(t *testing.T, f *gateLivenessFixture) {
				g, err := f.store.GetGroup(context.Background(), f.group.ID)
				require.NoError(t, err)
				require.Equal(t, f.originalGroupName, g.Name,
					"the group name was rewritten by a caller the gate must refuse")
			},
			assertWritten: func(t *testing.T, f *gateLivenessFixture) {
				g, err := f.store.GetGroup(context.Background(), f.group.ID)
				require.NoError(t, err)
				require.Equal(t, glAttemptedName, g.Name,
					"the admin control did not actually rename the group, so the "+
						"unchanged-name assertion on the deny arms proves nothing")
			},
		},
		{
			// SITE 2 — handlers_groups.go deleteGroup, ActionDelete. The fixture
			// group is GroupTypeExplicit, so the system-managed project_agents
			// refusal cannot stand in for the gate here.
			name:        "deleteGroup",
			method:      http.MethodDelete,
			path:        groupPath,
			bypassCode:  http.StatusNoContent,
			successCode: http.StatusNoContent,
			assertUnwritten: func(t *testing.T, f *gateLivenessFixture) {
				g, err := f.store.GetGroup(context.Background(), f.group.ID)
				require.NoError(t, err, "the group row is gone — a caller the gate must refuse deleted it")
				require.Equal(t, f.group.ID, g.ID)
			},
			assertWritten: func(t *testing.T, f *gateLivenessFixture) {
				_, err := f.store.GetGroup(context.Background(), f.group.ID)
				require.ErrorIs(t, err, store.ErrNotFound,
					"the admin control did not actually delete the group, so the "+
						"row-still-present assertion on the deny arms proves nothing")
			},
		},
		{
			// SITE 3 — handlers_groups.go addGroupMember, ActionAddMember.
			//
			// SITE 6 IS SUBSUMED HERE. The sixth converted site is the
			// role-hierarchy block further down this same handler, which now
			// dereferences userIdent unconditionally because the gate above
			// guarantees it is non-nil. The two are welded: restoring the nil
			// check at the gate alone leaves a nil identity flowing into
			// userIdent.ID(). So this arm covers both, and site 6 has no
			// independent behavioural witness while the gate above stands. The
			// structural test at the bottom of this file is the only thing that
			// can see site 6 alone.
			name:   "addGroupMember",
			method: http.MethodPost,
			path:   func(f *gateLivenessFixture) string { return groupPath(f) + "/members" },
			body: func(f *gateLivenessFixture) interface{} {
				return AddGroupMemberRequest{
					MemberType: store.GroupMemberTypeUser,
					MemberID:   f.newcomer.ID,
					Role:       store.GroupMemberRoleMember,
				}
			},
			bypassCode:  http.StatusCreated,
			successCode: http.StatusCreated,
			assertUnwritten: func(t *testing.T, f *gateLivenessFixture) {
				_, err := getMembership(t, f, f.newcomer.ID)
				require.ErrorIs(t, err, store.ErrNotFound,
					"a membership was minted by a caller the gate must refuse")
			},
			assertWritten: func(t *testing.T, f *gateLivenessFixture) {
				m, err := getMembership(t, f, f.newcomer.ID)
				require.NoError(t, err,
					"the admin control did not actually mint the membership, so the "+
						"no-membership assertion on the deny arms proves nothing")
				require.Equal(t, store.GroupMemberRoleMember, m.Role)
			},
		},
		{
			// SITE 4 — handlers_groups.go removeGroupMember, ActionRemoveMember.
			name:   "removeGroupMember",
			method: http.MethodDelete,
			path: func(f *gateLivenessFixture) string {
				return groupPath(f) + "/members/" + store.GroupMemberTypeUser + "/" + f.victim.ID
			},
			bypassCode:  http.StatusNoContent,
			successCode: http.StatusNoContent,
			assertUnwritten: func(t *testing.T, f *gateLivenessFixture) {
				m, err := getMembership(t, f, f.victim.ID)
				require.NoError(t, err,
					"the membership is gone — a caller the gate must refuse removed it")
				require.Equal(t, store.GroupMemberRoleMember, m.Role)
			},
			assertWritten: func(t *testing.T, f *gateLivenessFixture) {
				_, err := getMembership(t, f, f.victim.ID)
				require.ErrorIs(t, err, store.ErrNotFound,
					"the admin control did not actually remove the membership, so the "+
						"member-still-present assertion on the deny arms proves nothing")
			},
		},
		{
			// SITE 5 — pty_handlers.go handleAgentPTY, ActionAttach.
			//
			// A PTY attach has no store effect at the point of the gate, so the
			// status IS the whole invariant — and it has to be 403 exactly. 422
			// no_runtime_broker is reachable only from PAST the gate, so a 422
			// here means the caller was never authorized at all: that is the bug
			// shape, and it is why this arm cannot be written as NotEqual(200).
			//
			// The admin control below also lands on 422, for the opposite reason:
			// it cleared the gate. Same code, different cause. The control's job
			// is only to show the request is well formed and reaches the handler,
			// so that the agent and broker 403s are gate decisions rather than
			// routing accidents.
			name:        "handleAgentPTY",
			method:      http.MethodGet,
			websocket:   true,
			path:        func(f *gateLivenessFixture) string { return ptyPath(f.ptyTarget.ID) },
			bypassCode:  http.StatusUnprocessableEntity,
			successCode: http.StatusUnprocessableEntity,
		},
	}
}

// TestGateLiveness_NonUserCallersRefusedAtGroupWritesAndPTY is the pin.
//
// RED-ON-REVERT (measured): with the fail-closed conversion reverted to
// `if userIdent != nil`, the agent and broker arms return the site's bypassCode
// and the store mutation lands — updateGroup 200 with the name rewritten,
// deleteGroup 204 with the row gone, addGroupMember 201 with the membership
// minted, removeGroupMember 204 with the member gone, handleAgentPTY 422.
func TestGateLiveness_NonUserCallersRefusedAtGroupWritesAndPTY(t *testing.T) {
	for _, site := range gateLivenessSites() {
		site := site
		t.Run(site.name, func(t *testing.T) {
			for _, caller := range nonUserCallers() {
				caller := caller
				t.Run(caller.name+" is refused and writes nothing", func(t *testing.T) {
					// A fresh fixture per arm: under a revert these requests
					// mutate, and an arm must not inherit the damage done by the
					// arm before it.
					f := setupGateLiveness(t)

					var body interface{}
					if site.body != nil {
						body = site.body(f)
					}
					req := f.newRequest(t, site.method, site.path(f), body)
					if site.websocket {
						ptyUpgradeHeaders(req)
					}
					caller.authenticate(t, f, req)

					if site.assertUnwritten != nil {
						site.assertUnwritten(t, f) // precondition
					}
					rec := f.serve(req)

					require.Equal(t, http.StatusForbidden, rec.Code,
						"%s must refuse a %s caller: it is neither a UserIdentity nor "+
							"anonymous, which is the caller kind the pre-fix "+
							"`if userIdent != nil` guard skipped. %d is the bypass shape "+
							"this test exists to catch. body=%s",
						site.name, caller.name, site.bypassCode, rec.Body.String())
					require.Contains(t, rec.Body.String(), ErrCodeForbidden,
						"the 403 must be the authorization denial and not some other "+
							"refusal that happens to share the status; body=%s", rec.Body.String())

					if site.assertUnwritten != nil {
						site.assertUnwritten(t, f)
					}
				})
			}

			t.Run("CONTROL hub admin performs the same request and it takes effect", func(t *testing.T) {
				// Establishes only that the method, path and body are well formed
				// and effective, which is what makes the unchanged-store
				// assertions above non-vacuous. An admin is authorized both with
				// and without the gate, so this arm is green either way and is
				// not evidence about the gate.
				f := setupGateLiveness(t)

				var body interface{}
				if site.body != nil {
					body = site.body(f)
				}
				req := f.newRequest(t, site.method, site.path(f), body)
				if site.websocket {
					ptyUpgradeHeaders(req)
				}
				svc := f.srv.GetUserTokenService()
				require.NotNil(t, svc)
				tok, _, err := svc.GenerateAccessToken(f.admin.ID, f.admin.Email,
					f.admin.DisplayName, string(f.admin.Role), ClientTypeAPI)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+tok)

				rec := f.serve(req)
				require.Equal(t, site.successCode, rec.Code,
					"the control request must be accepted; if it is not, the deny arms "+
						"may be refusing a malformed request rather than an unauthorized "+
						"caller. body=%s", rec.Body.String())
				if site.assertWritten != nil {
					site.assertWritten(t, f)
				}
			})
		})
	}
}

// TestGateLiveness_NoFailOpenIdentityGuardRemainsInGroupHandlers is a STRUCTURAL
// tripwire over handlers_groups.go, and it earns its place for one reason: the
// role-hierarchy block in addGroupMember (the sixth converted site) has no
// behavioural witness of its own. While the gate above it stands, restoring its
// `if userIdent != nil` wrapper changes no response — a non-user caller is already
// refused upstream. Only the source shape can see it.
//
// Its reds are CORRELATED with the behavioural arms above: reverting any of the
// four group gates fails both this test and that one. Two failures, one fault —
// do not read them as two confirmations.
func TestGateLiveness_NoFailOpenIdentityGuardRemainsInGroupHandlers(t *testing.T) {
	const src = "handlers_groups.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	require.NoError(t, err)

	pos := func(n ast.Node) string { return fset.Position(n.Pos()).String() }

	isCallTo := func(e ast.Expr, name string) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			return fn.Name == name
		case *ast.SelectorExpr:
			return fn.Sel.Name == name
		}
		return false
	}

	// The population is EVERY GetUserIdentityFromContext call in the file, found
	// wherever it sits. Deriving it from the compliant shape instead would make the
	// test blind to exactly the regression it is looking for: the pre-fix idiom
	// hides the lookup in an `if` initializer, so a search for plain assignments
	// finds nothing and reports an empty population rather than a fail-open guard.
	var lookups []*ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isCallTo(call, "GetUserIdentityFromContext") {
			lookups = append(lookups, call)
		}
		return true
	})
	require.NotEmpty(t, lookups,
		"no GetUserIdentityFromContext call remains in %s — this test is now measuring "+
			"nothing. If the identity lookup moved to a helper, re-point this test at it; "+
			"do not delete it.", src)

	// Pass 1: each lookup must be a plain assignment IMMEDIATELY followed by a
	// fail-closed nil refusal. Immediacy is the point — a refusal that has drifted
	// past an intervening statement no longer guards what sits between them.
	// Compliant lookups are recorded by position, so anything left over is an
	// offender no matter what shape it took.
	compliant := map[token.Pos]bool{}
	identityVars := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		for i, stmt := range fn.Body.List {
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 || !isCallTo(assign.Rhs[0], "GetUserIdentityFromContext") {
				continue
			}
			name, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				continue
			}
			identityVars[name.Name] = true

			if i+1 >= len(fn.Body.List) {
				continue
			}
			ifs, ok := fn.Body.List[i+1].(*ast.IfStmt)
			if !ok {
				continue
			}
			bin, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok || bin.Op != token.EQL {
				continue
			}
			x, xok := bin.X.(*ast.Ident)
			y, yok := bin.Y.(*ast.Ident)
			if !xok || !yok || x.Name != name.Name || y.Name != "nil" {
				continue
			}
			ast.Inspect(ifs.Body, func(b ast.Node) bool {
				if _, ok := b.(*ast.ReturnStmt); ok {
					compliant[assign.Rhs[0].Pos()] = true
				}
				return true
			})
		}
		return true
	})

	// An identity bound in an `if` initializer is scoped to that `if`, so collect
	// those names too — otherwise pass 2 cannot recognise the very variable the
	// fail-open idiom tests.
	ast.Inspect(file, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || ifs.Init == nil {
			return true
		}
		if assign, ok := ifs.Init.(*ast.AssignStmt); ok && len(assign.Rhs) == 1 &&
			isCallTo(assign.Rhs[0], "GetUserIdentityFromContext") {
			if name, ok := assign.Lhs[0].(*ast.Ident); ok {
				identityVars[name.Name] = true
			}
		}
		return true
	})

	var offenders []string
	for _, call := range lookups {
		if !compliant[call.Pos()] {
			offenders = append(offenders, "  "+pos(call))
		}
	}
	require.Empty(t, offenders,
		"every GetUserIdentityFromContext in %s must be a plain assignment followed "+
			"IMMEDIATELY by a fail-closed `if <ident> == nil { ...; return }`. These are "+
			"not:\n%s\n"+
			"Such a lookup is either nested in a conditional — the #591 fail-open idiom, "+
			"where a non-user caller skips the block and reaches the write — or separated "+
			"from its refusal, which leaves the statements in between unguarded. Read the "+
			"listed lines to see which; both are defects.", src, offenders)

	// Pass 2: the fail-open idiom itself. Any `<identity> != nil` condition whose
	// body carries an authorization decision — a CheckAccess call or a 403 — is
	// the shape that was removed: it makes the decision CONDITIONAL on the caller
	// being a user, so a non-user caller falls through it untouched.
	var failOpen []string
	ast.Inspect(file, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		bin, ok := ifs.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.NEQ {
			return true
		}
		x, xok := bin.X.(*ast.Ident)
		y, yok := bin.Y.(*ast.Ident)
		if !xok || !yok || y.Name != "nil" || !identityVars[x.Name] {
			return true
		}
		decides := false
		ast.Inspect(ifs.Body, func(b ast.Node) bool {
			if call, ok := b.(*ast.CallExpr); ok && isCallTo(call, "CheckAccess") {
				decides = true
			}
			if sel, ok := b.(*ast.SelectorExpr); ok && sel.Sel.Name == "StatusForbidden" {
				decides = true
			}
			return true
		})
		if decides {
			failOpen = append(failOpen, "  "+pos(ifs))
		}
		return true
	})
	require.Empty(t, failOpen,
		"%s contains an authorization decision nested inside `<identity> != nil`:\n%s\n"+
			"That is the #591 fail-open idiom: an agent or broker token yields a nil user "+
			"identity, skips the whole block, and reaches the privileged write. Refuse the "+
			"nil identity first, then decide unconditionally. If a nil-tolerant branch is "+
			"genuinely needed here, it must not contain the decision — narrow this check "+
			"deliberately rather than deleting it.", src, failOpen)
}
