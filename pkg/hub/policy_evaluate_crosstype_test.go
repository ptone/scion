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

// Cross-type identity confusion at the /api/v1/policies/evaluate self-arm
// (ptone/scion#591).
//
// The endpoint admits two callers: a hub admin, and the principal being
// evaluated. "The principal being evaluated" is a PAIR — a principalType and a
// principalId — and both halves are supplied by the caller. A gate that
// compares only the id half treats "same id" as "same principal", so an
// identity of one kind whose ID() equals a principal of another kind is
// admitted as that other principal.
//
// The tests below are a TABLE over every concrete Identity implementation in
// this package, not a pair of examples. The census in
// TestPolicyEvaluate_IdentityKindCensus derives the population from the source
// rather than from a hand-written list, so a seventh identity kind added later
// fails this file by default instead of silently leaving the population
// untested.

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
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Identity-kind census — derived from the source, not from memory
// ============================================================================

// f3CensusReachesGate and f3CensusNotACaller are the two buckets every identity
// implementation must fall into. Every cell is enumerated: a kind is either
// exercised by the behaviour table below, or it carries a stated reason why no
// request can present it to the gate.
const (
	f3CensusReachesGate = "reaches the evaluate gate — must have a row in the behaviour table"
	f3CensusNotACaller  = "cannot reach the gate"
)

// f3IdentityCensus is the pinned classification of every concrete type in
// package hub that implements Identity. TestPolicyEvaluate_IdentityKindCensus
// asserts this map is exactly the set the source scan finds, in both
// directions, so adding a seventh identity kind fails here until it is
// classified and (if it can authenticate) given a row in the table.
var f3IdentityCensus = map[string]struct {
	bucket string
	// note is the reason a kind is in f3CensusNotACaller, or the table label
	// under which it is exercised.
	note string
}{
	"AuthenticatedUser": {f3CensusReachesGate,
		"user JWT — rows 'admin user' and 'member user'"},
	"DevUser": {f3CensusReachesGate,
		"dev token — row 'dev user'"},
	"ScopedUserIdentity": {f3CensusReachesGate,
		"user access token — row 'UAT'; the VALUE form is covered by " +
			"TestPolicyEvaluate_ValueScopedIdentityIsNotProducible"},
	"agentIdentityWrapper": {f3CensusReachesGate,
		"agent token — rows 'agent'"},
	"brokerIdentityImpl": {f3CensusReachesGate,
		"HMAC-signed broker request — rows 'broker …'"},
	"evaluateAgentIdentity": {f3CensusNotACaller,
		"constructed by handlePolicyEvaluate as the EVALUATED SUBJECT after the " +
			"gate has already run; no authentication path installs it as a " +
			"caller identity, so it is never the value the gate inspects"},
}

// f3ScanIdentityImplementations returns every named type in the non-test
// sources of this package that implements Identity, by either of the two routes
// a Go type can take: declaring `Type() string` itself, or embedding one of the
// identity interfaces and promoting it.
//
// It is deliberately a source scan rather than a list. A list records what the
// author knew; the scan records what is there.
func f3ScanIdentityImplementations(t *testing.T) (map[string]bool, int) {
	t.Helper()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	// Interface declarations are the identity contracts themselves, not
	// implementations of them.
	contracts := map[string]bool{
		"Identity": true, "UserIdentity": true, "AgentIdentity": true,
		"BrokerIdentity": true,
	}

	found := map[string]bool{}
	examined := 0
	fset := token.NewFileSet()

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parsing %s", name)
		examined++

		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				// A declared `Type() string` method.
				if d.Recv == nil || d.Name.Name != "Type" {
					continue
				}
				if d.Type.Params != nil && len(d.Type.Params.List) != 0 {
					continue
				}
				if d.Type.Results == nil || len(d.Type.Results.List) != 1 {
					continue
				}
				if id, ok := d.Type.Results.List[0].Type.(*ast.Ident); !ok || id.Name != "string" {
					continue
				}
				if base := f3ReceiverTypeName(d.Recv); base != "" && !contracts[base] {
					found[base] = true
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || contracts[ts.Name.Name] {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					for _, field := range st.Fields.List {
						if len(field.Names) != 0 {
							continue // named field, not an embedding
						}
						if id, ok := field.Type.(*ast.Ident); ok && contracts[id.Name] {
							found[ts.Name.Name] = true
						}
					}
				}
			}
		}
	}
	return found, examined
}

// f3ReceiverTypeName returns the base type name of a method receiver, stripping
// a pointer if present.
func f3ReceiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) != 1 {
		return ""
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// TestPolicyEvaluate_IdentityKindCensus pins the population the behaviour table
// must cover. It fails in both directions: an identity kind present in the
// source and absent from the census, and a census entry naming a type that no
// longer exists.
func TestPolicyEvaluate_IdentityKindCensus(t *testing.T) {
	found, examined := f3ScanIdentityImplementations(t)

	// Instrument liveness first: a scanner that silently examined nothing
	// returns an empty set, which is byte-identical to "the package has no
	// identity kinds". Assert it saw files, saw a member it must see, and did
	// NOT return the interface contracts.
	require.Greater(t, examined, 10,
		"the scan examined %d files; it is not reading the package", examined)
	require.True(t, found["brokerIdentityImpl"],
		"the scan missed a type that declares Type() string — the scan is broken, not the tree")
	require.True(t, found["ScopedUserIdentity"],
		"the scan missed a type that implements Identity by embedding — the scan is broken, not the tree")
	require.False(t, found["Identity"],
		"the scan returned an interface contract as an implementation")

	var missing, stale []string
	for name := range found {
		if _, ok := f3IdentityCensus[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range f3IdentityCensus {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	assert.Emptyf(t, missing,
		"identity kinds %v implement Identity and are not classified in f3IdentityCensus. "+
			"An identity kind that is not classified is not tested against the "+
			"policy-evaluate gate: add it to the census, and either give it a row in "+
			"TestPolicyEvaluate_CrossTypeSelfArm or state why no request can present it.",
		missing)
	assert.Emptyf(t, stale,
		"f3IdentityCensus names %v, which the source scan does not find. "+
			"A census entry for a type that no longer exists reads as coverage that "+
			"is not there.", stale)
}

// ============================================================================
// Fixture
// ============================================================================

// f3Action is the action every request below evaluates. It is deliberately not
// "read": the hub seeds a hub-member-read-all policy at startup, so "read" is
// allowed for every member and could not tell one principal's decision from
// another's.
const f3Action = "delete"

// f3Broker is an authenticated broker principal: an ID and the HMAC key that
// signs for it.
type f3Broker struct {
	id  string
	key []byte
}

type f3Fixture struct {
	srv   *Server
	store store.Store

	admin       *store.User // hub admin — the caller the endpoint is designed for
	attacker    *store.User // role=member; the lowest privilege that can authenticate
	victimAllow *store.User // bound to an allow policy and in a group
	victimDeny  *store.User // no policy, no group, owns nothing
	owner       *store.User // owns the project and agent, so victimDeny does not

	proj  *store.Project
	agent *store.Agent

	// brokerAsUser and brokerAsAgent carry an ID equal to another principal's.
	// brokerPlain is the discriminating control: same kind, same route, same
	// signing, non-colliding ID.
	brokerAsUser  f3Broker
	brokerAsAgent f3Broker
	brokerPlain   f3Broker

	policyID string
	groupID  string
}

func f3Setup(t *testing.T) *f3Fixture {
	t.Helper()
	ctx := context.Background()

	s, err := newTestStore(":memory:")
	if err != nil {
		t.Skipf("skipping: test store unavailable (%v)", err)
	}
	require.NoError(t, s.Migrate(ctx))

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	cfg.DevUserConfig = DevUserConfig{
		Username: "dev", DisplayName: "Development User", Email: "dev@localhost",
	}
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-id")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	f := &f3Fixture{srv: srv, store: s}

	mkUser := func(name, role string) *store.User {
		u := &store.User{
			ID: tid("f3-" + name), Email: name + "@f3.test", DisplayName: name,
			Role: role, Status: "active", Created: time.Now(),
		}
		require.NoError(t, s.CreateUser(ctx, u))
		return u
	}
	f.admin = mkUser("admin", store.UserRoleAdmin)
	f.attacker = mkUser("attacker", store.UserRoleMember)
	f.victimAllow = mkUser("victim-allow", store.UserRoleMember)
	f.victimDeny = mkUser("victim-deny", store.UserRoleMember)
	f.owner = mkUser("owner", store.UserRoleMember)

	// The project and agent are owned by a user who appears nowhere else in the
	// table. If victimDeny owned them it would be allowed as resource owner and
	// the "no policy" arm below would stop being a deny.
	f.proj = &store.Project{
		ID: tid("f3-project"), Name: "F3 Project", Slug: "f3-project",
		OwnerID: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.proj))

	f.agent = &store.Agent{
		ID: tid("f3-agent"), Slug: "f3-agent", Name: "F3 Agent",
		ProjectID: f.proj.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.owner.ID, OwnerID: f.owner.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, f.agent))

	// victimAllow is in a group and carries a hub-scoped allow policy, so a
	// successful evaluation of victimAllow returns a body that VARIES from
	// every other principal's: allowed=true, a matched policy, a policy name
	// and a group list. Without that variation a 200 could not be distinguished
	// from a canned response.
	//
	// The policy grants f3Action, NOT "read". The hub seeds a
	// hub-member-read-all policy for every member at startup, so an evaluation
	// on "read" is allowed for every user here and would make the allow and
	// deny arms indistinguishable.
	f.groupID = tid("f3-group")
	require.NoError(t, s.CreateGroup(ctx, &store.Group{
		ID: f.groupID, Name: "F3 Group", Slug: "f3-group", GroupType: "explicit",
	}))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{
		GroupID: f.groupID, MemberType: "user", MemberID: f.victimAllow.ID,
		Role: "member", AddedAt: time.Now(),
	}))
	f.policyID = tid("f3-policy")
	require.NoError(t, s.CreatePolicy(ctx, &store.Policy{
		ID: f.policyID, Name: "F3 Allow", ScopeType: "hub", ResourceType: "agent",
		Actions: []string{f3Action}, Effect: "allow",
	}))
	require.NoError(t, s.AddPolicyBinding(ctx, &store.PolicyBinding{
		PolicyID: f.policyID, PrincipalType: "user", PrincipalID: f.victimAllow.ID,
	}))

	// The colliding broker rows are seeded into the store directly rather than
	// minted through POST /api/v1/brokers. That is deliberate and is not a
	// fabricated credential:
	//
	//   - the shape is a persisted ROW, and rows outlive the code that wrote
	//     them: any broker registered before the registration-side check was
	//     added is still in the database with whatever ID it was given;
	//   - broker rows enter the store by more than one route, and the
	//     registration path is only one of them;
	//   - the secret is a real BrokerSecret and every request below is really
	//     HMAC-signed and really passes through UnifiedAuthMiddleware and
	//     BrokerAuthMiddleware. Nothing about the identity is injected.
	//
	// So this gate is what holds once an arbitrary-ID broker row exists at all,
	// which is exactly the property a second, independent gate must have.
	f.brokerAsUser = f3SeedBroker(t, s, "f3-broker-as-user", f.victimAllow.ID)
	f.brokerAsAgent = f3SeedBroker(t, s, "f3-broker-as-agent", f.agent.ID)
	f.brokerPlain = f3SeedBroker(t, s, "f3-broker-plain", uuid.New().String())

	return f
}

// f3SeedBroker creates a runtime broker row with the given ID and an active
// HMAC secret.
func f3SeedBroker(t *testing.T, s store.Store, name, id string) f3Broker {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: id, Name: name, Slug: name, Status: store.BrokerStatusOnline,
		Created: time.Now(), Updated: time.Now(),
	}))
	key := []byte("f3-secret-key-32-bytes-long-ok!!")
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID: id, SecretKey: key,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))
	return f3Broker{id: id, key: key}
}

// ============================================================================
// Callers — one per production authentication route
// ============================================================================

// f3Evaluate posts an evaluate request with a user JWT.
func (f *f3Fixture) asUser(t *testing.T, u *store.User, req EvaluateRequest) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestAsUser(t, f.srv, u, http.MethodPost, "/api/v1/policies/evaluate", req)
}

// asDev posts with the development token, which installs a *DevUser.
func (f *f3Fixture) asDev(t *testing.T, req EvaluateRequest) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, f.srv, http.MethodPost, "/api/v1/policies/evaluate", req)
}

// asUAT posts with a user access token, which installs a *ScopedUserIdentity.
func (f *f3Fixture) asUAT(t *testing.T, u *store.User, req EvaluateRequest) *httptest.ResponseRecorder {
	t.Helper()
	tok, _, err := f.srv.uatService.CreateToken(context.Background(), u.ID,
		"f3-uat-"+u.ID, f.proj.ID, []string{store.UATScopeAgentRead}, nil)
	require.NoError(t, err)
	return f3Post(t, f.srv, req, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	})
}

// asAgent posts with an agent token, which installs an *agentIdentityWrapper.
func (f *f3Fixture) asAgent(t *testing.T, req EvaluateRequest) *httptest.ResponseRecorder {
	t.Helper()
	tok, err := f.srv.GetAgentTokenService().GenerateAgentToken(
		f.agent.ID, f.agent.ProjectID, nil, nil)
	require.NoError(t, err)
	return f3Post(t, f.srv, req, func(r *http.Request) {
		r.Header.Set("X-Scion-Agent-Token", tok)
	})
}

// asBroker posts an HMAC-signed broker request, which installs a
// *brokerIdentityImpl.
func (f *f3Fixture) asBroker(t *testing.T, b f3Broker, req EvaluateRequest) *httptest.ResponseRecorder {
	t.Helper()
	return f3Post(t, f.srv, req, func(r *http.Request) {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := "f3-" + uuid.New().String()
		r.Header.Set(HeaderBrokerID, b.id)
		r.Header.Set(HeaderTimestamp, ts)
		r.Header.Set(HeaderNonce, nonce)
		svc := f.srv.brokerAuthService
		require.NotNil(t, svc)
		mac := hmac.New(sha256.New, b.key)
		mac.Write(svc.buildCanonicalString(r, ts, nonce))
		r.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	})
}

func f3Post(t *testing.T, srv *Server, body EvaluateRequest, auth func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/evaluate", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	auth(req)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// ============================================================================
// The table
// ============================================================================

// f3Case is one (caller kind, named principal) pair and the status the gate
// must produce for it.
type f3Case struct {
	// kind is the census key of the concrete identity the caller installs.
	kind string
	// caller is a human-readable label; it is also what the census note points
	// at, so keep the two in step.
	caller string
	// call issues the request as that caller.
	call func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder
	// principalType / principalID name the principal the request asks about.
	principalType string
	principalID   func(f *f3Fixture) string
	want          int
	why           string
}

func f3Cases() []f3Case {
	asUser := func(pick func(*f3Fixture) *store.User) func(*testing.T, *f3Fixture, EvaluateRequest) *httptest.ResponseRecorder {
		return func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder {
			return f.asUser(t, pick(f), req)
		}
	}
	asBroker := func(pick func(*f3Fixture) f3Broker) func(*testing.T, *f3Fixture, EvaluateRequest) *httptest.ResponseRecorder {
		return func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder {
			return f.asBroker(t, pick(f), req)
		}
	}
	idAdmin := func(f *f3Fixture) string { return f.admin.ID }
	idAttacker := func(f *f3Fixture) string { return f.attacker.ID }
	idVictimAllow := func(f *f3Fixture) string { return f.victimAllow.ID }
	idVictimDeny := func(f *f3Fixture) string { return f.victimDeny.ID }
	idAgent := func(f *f3Fixture) string { return f.agent.ID }

	return []f3Case{
		// ---- hub admins: unchanged, and the reason the endpoint exists ----
		{"AuthenticatedUser", "admin user", asUser(func(f *f3Fixture) *store.User { return f.admin }),
			"user", idVictimAllow, http.StatusOK,
			"a hub admin may evaluate any principal"},
		{"AuthenticatedUser", "admin user", asUser(func(f *f3Fixture) *store.User { return f.admin }),
			"agent", idAgent, http.StatusOK,
			"a hub admin may evaluate an agent principal"},
		{"DevUser", "dev user", func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder {
			return f.asDev(t, req)
		}, "user", idVictimAllow, http.StatusOK,
			"the development pseudo-user is an admin and keeps admin reach"},

		// ---- self-evaluation: the second admitted caller, per kind ----
		{"AuthenticatedUser", "member user", asUser(func(f *f3Fixture) *store.User { return f.attacker }),
			"user", idAttacker, http.StatusOK,
			"a user evaluating its own policy is the documented non-admin case"},
		{"ScopedUserIdentity", "UAT", func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder {
			return f.asUAT(t, f.victimAllow, req)
		}, "user", idVictimAllow, http.StatusOK,
			"a project-scoped token still evaluates its own user; Role() is empty " +
				"for a UAT, so this passes on the self-arm and not as an admin"},
		{"agentIdentityWrapper", "agent", func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder {
			return f.asAgent(t, req)
		}, "agent", idAgent, http.StatusOK,
			"an agent evaluating its OWN policy is the same documented case as a " +
				"user evaluating its own; narrowing the self-arm to users would " +
				"remove it"},

		// ---- same-kind, different principal: denied before and after ----
		{"AuthenticatedUser", "member user", asUser(func(f *f3Fixture) *store.User { return f.attacker }),
			"user", idVictimAllow, http.StatusForbidden,
			"a non-admin user may not evaluate another user"},
		{"ScopedUserIdentity", "UAT", func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder {
			return f.asUAT(t, f.victimAllow, req)
		}, "user", idVictimDeny, http.StatusForbidden,
			"a UAT may not evaluate another user"},
		{"AuthenticatedUser", "member user", asUser(func(f *f3Fixture) *store.User { return f.attacker }),
			"agent", idAgent, http.StatusForbidden,
			"a non-admin user may not evaluate an agent it is not"},

		// ---- cross-kind by ID collision: the defect ----
		{"brokerIdentityImpl", "broker whose ID equals a user's", asBroker(func(f *f3Fixture) f3Broker { return f.brokerAsUser }),
			"user", idVictimAllow, http.StatusForbidden,
			"a broker is not the user whose id it carries"},
		{"brokerIdentityImpl", "broker whose ID equals an agent's", asBroker(func(f *f3Fixture) f3Broker { return f.brokerAsAgent }),
			"agent", idAgent, http.StatusForbidden,
			"a broker is not the agent whose id it carries"},

		// ---- cross-kind, no collision: the discriminating control ----
		//
		// Same kind, same route, same signing, ID that matches nothing. If this
		// row and the two above ever agree on a 200, the collision is not what
		// the rows above are measuring.
		{"brokerIdentityImpl", "broker with an unrelated ID", asBroker(func(f *f3Fixture) f3Broker { return f.brokerPlain }),
			"user", idVictimAllow, http.StatusForbidden,
			"control: a broker with no collision is refused, so the gate is on the path"},
		{"brokerIdentityImpl", "broker with an unrelated ID", asBroker(func(f *f3Fixture) f3Broker { return f.brokerPlain }),
			"agent", idAgent, http.StatusForbidden,
			"control: same, on the agent principal type"},
		{"brokerIdentityImpl", "broker whose ID equals a user's", asBroker(func(f *f3Fixture) f3Broker { return f.brokerAsUser }),
			"agent", idAgent, http.StatusForbidden,
			"the collision must not transfer across principal types either"},

		// ---- cross-kind between the two evaluated types ----
		//
		// These two are LATENT, not reachable: user ids and agent ids are both
		// unprefixed v4 UUIDs minted by the hub, so making one equal the other
		// is not something a caller can arrange. They are here because the gate
		// should not depend on that, and because they are the rows that
		// distinguish a TYPE-MATCH gate from a USER-ONLY one — a user-only gate
		// leaves both of them passing the gate and failing later in the lookup,
		// which is a different status for the same wrong reason.
		{"AuthenticatedUser", "member user", asUser(func(f *f3Fixture) *store.User { return f.attacker }),
			"agent", idAttacker, http.StatusForbidden,
			"latent: a user naming its own id under principalType=agent is not that agent"},
		{"agentIdentityWrapper", "agent", func(t *testing.T, f *f3Fixture, req EvaluateRequest) *httptest.ResponseRecorder {
			return f.asAgent(t, req)
		}, "user", idAgent, http.StatusForbidden,
			"latent: an agent naming its own id under principalType=user is not that user"},

		// ---- an admin is still an admin under every principal type ----
		{"AuthenticatedUser", "admin user", asUser(func(f *f3Fixture) *store.User { return f.admin }),
			"user", idAdmin, http.StatusOK,
			"an admin evaluating itself is still allowed"},
	}
}

// TestPolicyEvaluate_CrossTypeSelfArm is the behaviour table. Every row states
// the caller's identity kind, the principal it names, and the status the gate
// must return.
func TestPolicyEvaluate_CrossTypeSelfArm(t *testing.T) {
	for i, tc := range f3Cases() {
		name := strconv.Itoa(i) + "_" + tc.caller + "_names_" + tc.principalType +
			"_wants_" + strconv.Itoa(tc.want)
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			f := f3Setup(t)
			rec := tc.call(t, f, EvaluateRequest{
				PrincipalType: tc.principalType,
				PrincipalID:   tc.principalID(f),
				ResourceType:  "agent",
				ResourceID:    f.agent.ID,
				Action:        f3Action,
			})
			assert.Equalf(t, tc.want, rec.Code,
				"%s naming principalType=%s: %s\nbody: %s",
				tc.caller, tc.principalType, tc.why, rec.Body.String())

			// A refusal must not carry an evaluation. Asserting on the status
			// alone would not catch a body written before the deny.
			if tc.want != http.StatusOK {
				assert.NotContains(t, rec.Body.String(), f.policyID,
					"a refused evaluation must not disclose the matched policy")
				assert.NotContains(t, rec.Body.String(), f.groupID,
					"a refused evaluation must not disclose group membership")
			}
		})
	}
}

// TestPolicyEvaluate_TableCoversEveryReachableKind closes the loop between the
// census and the table: every kind the census says reaches the gate must
// actually appear in the table.
func TestPolicyEvaluate_TableCoversEveryReachableKind(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range f3Cases() {
		covered[tc.kind] = true
	}
	for name, entry := range f3IdentityCensus {
		if entry.bucket != f3CensusReachesGate {
			continue
		}
		assert.Truef(t, covered[name],
			"identity kind %q is classified as reaching the evaluate gate (%s) but has "+
				"no row in f3Cases(). An unexercised kind is an untested kind.",
			name, entry.note)
	}
}

// TestPolicyEvaluate_AllowedEvaluationIsReal is the liveness arm for the table.
// A gate that refused everything would satisfy every deny row above; this shows
// the allowed rows carry a real, principal-specific evaluation, so a 200 is
// evidence and not a default.
func TestPolicyEvaluate_AllowedEvaluationIsReal(t *testing.T) {
	f := f3Setup(t)

	decode := func(rec *httptest.ResponseRecorder) EvaluateResponse {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp EvaluateResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp
	}

	base := EvaluateRequest{ResourceType: "agent", ResourceID: f.agent.ID, Action: f3Action}

	withPolicy := base
	withPolicy.PrincipalType, withPolicy.PrincipalID = "user", f.victimAllow.ID
	got := decode(f.asUser(t, f.admin, withPolicy))
	assert.True(t, got.Allowed, "the bound allow policy must produce allowed=true")
	assert.Equal(t, f.policyID, got.MatchedPolicy)
	assert.Contains(t, got.EffectiveGroups, f.groupID)

	withoutPolicy := base
	withoutPolicy.PrincipalType, withoutPolicy.PrincipalID = "user", f.victimDeny.ID
	got = decode(f.asUser(t, f.admin, withoutPolicy))
	assert.False(t, got.Allowed, "a principal with no policy must produce allowed=false")
	assert.Empty(t, got.MatchedPolicy)
}

// TestPolicyEvaluate_BrokerSigningIsRealDecoy separates the authentication axis
// from the authorization axis. Without it, a refusal on the broker rows could
// be re-read as "the harness never signed anything"; here a correct broker id
// with the wrong key fails at authentication (401) and is therefore
// distinguishable from a gate refusal (403).
func TestPolicyEvaluate_BrokerSigningIsRealDecoy(t *testing.T) {
	f := f3Setup(t)
	wrong := f3Broker{id: f.brokerAsUser.id, key: []byte("wrong-key-wrong-key-wrong-key!!!")}
	rec := f.asBroker(t, wrong, EvaluateRequest{
		PrincipalType: "user", PrincipalID: f.victimAllow.ID,
		ResourceType: "agent", ResourceID: f.agent.ID, Action: f3Action,
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a broker request with a bad signature must fail at authentication, not at the gate; body: %s",
		rec.Body.String())
}

// ============================================================================
// Attribution — what specifically is holding the table up
// ============================================================================

// TestPolicyEvaluate_SelfArmComparesTypeAndID states the property directly at
// the gate helper, so that the table above cannot be satisfied by some other
// change elsewhere on the path.
//
// It is written as the pair of facts that must hold TOGETHER: the ids are
// equal, and the principals are still not the same principal. Weakening
// callerIsEvaluatedPrincipal back to an id comparison makes the second half
// false while the first stays true, and this test names which half went.
func TestPolicyEvaluate_SelfArmComparesTypeAndID(t *testing.T) {
	const sharedID = "11111111-2222-3333-4444-555555555555"

	user := NewAuthenticatedUser(sharedID, "u@f3.test", "U", store.UserRoleMember, "cli")
	broker := NewBrokerIdentity(sharedID)
	agent := &agentIdentityWrapper{&AgentTokenClaims{
		Claims: jwt.Claims{Subject: sharedID},
	}}

	// Precondition: the ids really do collide. If this ever stops holding the
	// assertions below would pass for the wrong reason.
	require.Equal(t, user.ID(), broker.ID())
	require.Equal(t, user.ID(), agent.ID())
	require.NotEqual(t, user.Type(), broker.Type())
	require.NotEqual(t, user.Type(), agent.Type())

	assert.True(t, callerIsEvaluatedPrincipal(user, "user", sharedID),
		"a user IS the user principal with its own id")
	assert.True(t, callerIsEvaluatedPrincipal(agent, "agent", sharedID),
		"an agent IS the agent principal with its own id")

	assert.False(t, callerIsEvaluatedPrincipal(broker, "user", sharedID),
		"a broker is not the user principal that shares its id")
	assert.False(t, callerIsEvaluatedPrincipal(broker, "agent", sharedID),
		"a broker is not the agent principal that shares its id")
	assert.False(t, callerIsEvaluatedPrincipal(agent, "user", sharedID),
		"an agent is not the user principal that shares its id")
	assert.False(t, callerIsEvaluatedPrincipal(user, "agent", sharedID),
		"a user is not the agent principal that shares its id")

	// A principal type the endpoint cannot evaluate must not satisfy the
	// self-arm even for an identity of exactly that type. Otherwise the gate
	// would be passed and the refusal would come from the switch below it,
	// which is a different guarantee.
	assert.False(t, callerIsEvaluatedPrincipal(broker, "broker", sharedID),
		"a broker naming principalType=broker must be refused at the gate, not downstream")
	assert.False(t, callerIsEvaluatedPrincipal(user, "", sharedID))
	assert.False(t, callerIsEvaluatedPrincipal(user, "user", ""))
	assert.False(t, callerIsEvaluatedPrincipal(nil, "user", sharedID))
}

// TestPolicyEvaluate_EvaluatableTypesMatchHandler pins evaluatablePrincipalTypes
// against the principal-type switch in handlePolicyEvaluate, by reading the
// switch out of the source rather than restating it.
//
// The two can drift in both directions and both are bad: a type the switch
// handles but the set omits is a principal that can never self-evaluate, and a
// type the set admits but the switch does not handle is an identity kind passed
// through the gate on the strength of a subject the handler cannot build.
func TestPolicyEvaluate_EvaluatableTypesMatchHandler(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "handlers_policies.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "handlePolicyEvaluate" {
			fn = d
		}
	}
	require.NotNil(t, fn, "handlePolicyEvaluate not found in handlers_policies.go")

	switchCases := map[string]bool{}
	foundSwitch := false
	ast.Inspect(fn, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "PrincipalType" {
			return true
		}
		foundSwitch = true
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				require.Truef(t, ok && lit.Kind == token.STRING,
					"the principal-type switch has a non-literal case; this test can no "+
						"longer read the handled set out of the source and must be rewritten")
				val, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				switchCases[val] = true
			}
		}
		return true
	})

	require.True(t, foundSwitch,
		"no `switch req.PrincipalType` found in handlePolicyEvaluate; the scan is "+
			"broken, or the handler was restructured and this pin must follow it")
	require.NotEmpty(t, switchCases, "the switch was found but no cases were read from it")

	handled := make([]string, 0, len(switchCases))
	for k := range switchCases {
		handled = append(handled, k)
	}
	admitted := make([]string, 0, len(evaluatablePrincipalTypes))
	for k := range evaluatablePrincipalTypes {
		admitted = append(admitted, k)
	}
	sort.Strings(handled)
	sort.Strings(admitted)
	assert.Equal(t, handled, admitted,
		"evaluatablePrincipalTypes and the principal-type switch in handlePolicyEvaluate "+
			"disagree. Adding a principal type to the handler without adding it here "+
			"silently denies that principal its own self-evaluation; adding it here "+
			"without the handler admits a caller the handler cannot serve.")
}

// TestPolicyEvaluate_ValueScopedIdentityIsNotProducible is the enumerated cell
// for the VALUE form of ScopedUserIdentity, which the behaviour table cannot
// reach over HTTP.
//
// The value form is not a hypothetical: ScopedUserIdentity is a struct, so
// `ScopedUserIdentity{...}` is writable. It is absent from the table because no
// authentication path emits one — the sole constructor returns a pointer — and
// this test is what makes that a checked claim rather than an assumption. The
// second half records what the gate would do if one ever appeared, so the cell
// is answered rather than merely excused.
func TestPolicyEvaluate_ValueScopedIdentityIsNotProducible(t *testing.T) {
	const uid = "66666666-7777-8888-9999-000000000000"
	inner := NewAuthenticatedUser(uid, "u@f3.test", "U", store.UserRoleAdmin, "cli")

	minted := NewScopedUserIdentity(inner, "proj", []string{store.UATScopeAgentRead})
	require.Equal(t, reflect.Ptr, reflect.ValueOf(minted).Kind(),
		"the only constructor for a scoped identity must return a pointer; if it "+
			"returns a value, the value form becomes producible and needs a table row")

	// A value ScopedUserIdentity does not satisfy UserIdentity at all: Role() is
	// declared on the pointer receiver and shadows the promoted one, so it is
	// not in the value's method set. It is still an Identity, and its Type()
	// promotes to the inner user's.
	value := ScopedUserIdentity{UserIdentity: inner, projectID: "proj"}
	var asIdentity Identity = value
	_, isUserIdentity := asIdentity.(UserIdentity)
	assert.False(t, isUserIdentity,
		"a value ScopedUserIdentity must not satisfy UserIdentity (see the Role() "+
			"doc comment in identity.go); if it does, it would reach the admin arm "+
			"with the empty role and needs its own table row")
	assert.Equal(t, "user", asIdentity.Type())

	// Consequence at the gate: it cannot be an admin, and it can only ever be
	// the one user it wraps.
	assert.True(t, callerIsEvaluatedPrincipal(asIdentity, "user", uid))
	assert.False(t, callerIsEvaluatedPrincipal(asIdentity, "user", "some-other-id"))
	assert.False(t, callerIsEvaluatedPrincipal(asIdentity, "agent", uid))
}
