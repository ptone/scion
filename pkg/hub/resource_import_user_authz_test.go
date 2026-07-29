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

// #591 N78 regression for the resource-import/discover USER branch
// (handlers_resource_import.go). 1b72a060 fixed the AGENT branch — pinned in
// resource_import_authz_test.go — and left the user branch open, while the
// denial reason it wrote ("create scope does not authorize read enumeration")
// was already caller agnostic. The user branch authorized a READ enumeration on
// an ActionCreate grant alone, so a plain project member holding only
// agent-create could enumerate a project subtree it cannot read.
//
// UNLIKE THE AGENT CASE, NO DENY POLICY IS REQUIRED. The agent pin has to revoke
// a read baseline with an explicit deny to reach the hole; the user branch is
// reachable out of the box, because a user has no read baseline to revoke — an
// absent grant is enough. riUserMember therefore binds NO deny policy anywhere,
// and the workspace-route baseline below proves the refusal comes from an absent
// allow rather than a present deny.
//
// WHAT THE TABLE IS FOR. Route rows are enumerated from the handler file and
// crossed with four caller rows, two of which MUST PASS. A gate that refused
// everything would satisfy "create-only is refused" vacuously; the passing rows
// are what distinguish gate-present from predicate-matches-everything. The agent
// rows ride in the same table as the cross-caller control: the property is that
// the two caller kinds now agree on this route set, which is exactly what
// 1b72a060 left untrue.
//
// RED-WITHOUT-FIX (measured before commit, by neutering authorizeImportUserRead
// to `return true`): the create-only member goes 403 -> 200 on discover-templates
// WITH riGateVictimDir present in the body, 403 -> 200 on import-templates, and
// 403 -> 400-downstream on the other four. All six refuse arms red; every pass
// arm and every agent arm stays green.
//
// Test naming: everything file-local is prefixed riUser.

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// riUserSourceFile is the handler file whose user branches this file pins. The
// inventory test reads it, so a rename must update this constant or fail loudly
// rather than silently measuring nothing.
const riUserSourceFile = "handlers_resource_import.go"

// riUserMember creates a PLAIN member user, chosen so every bypass in
// checkAccessForUser misses and the decision falls through to policy:
//   - not role=admin, so the admin bypass (authz.go step 1) misses;
//   - not the owner of projA (f.owner is), and the resources these handlers
//     build carry no OwnerID anyway, so the owner bypass (step 2) misses;
//   - no ancestry relationship, so canAccessAsAncestor (step 2.5) misses;
//   - never added to the project members group, so isProjectOwnerOrAdmin
//     (step 2.6) misses.
//
// If a future change gives plain members a project read baseline, the create-only
// arms below stop being refused and this test reds — which is the correct
// outcome, not a flake: it would mean the disclosure is reachable again.
func riUserMember(t *testing.T, f *wsGateFixture, name string) *store.User {
	t.Helper()
	u := &store.User{
		ID: tid(name), Email: name + "@example.com",
		DisplayName: name, Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, f.store.CreateUser(context.Background(), u))
	return u
}

// riUserGrant binds an ALLOW policy for the given resource type and actions,
// scoped to projA. Effect is allow at every call site in this file: the point of
// N78 is that the hole needs no deny policy.
func riUserGrant(t *testing.T, f *wsGateFixture, userID, name, resourceType string, actions []string) {
	t.Helper()
	ctx := context.Background()
	p := &store.Policy{
		ID: tid(name), Name: name,
		ScopeType: "project", ScopeID: f.projA.ID, ResourceType: resourceType,
		Actions: actions, Effect: "allow", Priority: 50,
	}
	require.NoError(t, f.store.CreatePolicy(ctx, p))
	require.NoError(t, f.store.AddPolicyBinding(ctx, &store.PolicyBinding{
		PolicyID: p.ID, PrincipalType: "user", PrincipalID: userID,
	}))
}

// riUserGrantCreate grants create on BOTH resource types the routes gate on.
// The templates routes check Resource{Type:"agent"}; the harness-config routes
// check Resource{Type:"harness_config"}. Granting only "agent" leaves the
// harness routes refused AT THE CREATE CHECK, which renders the byte-identical
// 403 the read gate renders — the create-only arm would then pass for the wrong
// reason and stay green with the fix reverted. That near-miss is why the
// create+read row exists: it varies read alone.
func riUserGrantCreate(t *testing.T, f *wsGateFixture, u *store.User, tag string) {
	t.Helper()
	riUserGrant(t, f, u.ID, "riuser-create-agent-"+tag, "agent", []string{"create"})
	riUserGrant(t, f, u.ID, "riuser-create-harness-"+tag, "harness_config", []string{"create"})
}

type riUserRoute struct {
	name string
	path string
	body []byte
	// passCode and passBody are the content-bearing signature of a caller who
	// PASSED the gate: either the real 200 payload, or the specific downstream
	// failure reached only after the gate allowed the request. Pinning the
	// specific code rather than NotEqual(403) is N44: a dead route, a rename or a
	// moved middleware returns 404/401, which satisfies NotEqual(403) vacuously
	// and would keep the allow rows green through exactly the refactor this file
	// exists to catch.
	passCode int
	passBody string
}

func riUserRoutes(t *testing.T, f *wsGateFixture, wsPath string) []riUserRoute {
	t.Helper()
	base := "/api/v1/projects/" + f.projA.ID
	ws := riGateDiscoverBody(t, wsPath)

	// A remote source that cannot be fetched: a caller who passes the gate falls
	// through to that failure, which is how the generic routes distinguish
	// "gate passed" from "gate refused" without a real remote.
	const bogusSource = "https://example.invalid/does-not-exist.git"
	generic := func(kind string) []byte {
		b, err := json.Marshal(map[string]string{
			"kind": kind, "scope": "project", "scopeId": f.projA.ID, "sourceUrl": bogusSource,
		})
		require.NoError(t, err)
		return b
	}

	return []riUserRoute{
		{"discover-templates", base + "/discover-templates", ws, http.StatusOK, riGateGoodTemplate},
		{"discover-harness-configs", base + "/discover-harness-configs", ws, http.StatusBadRequest, "discover_failed"},
		{"import-templates", base + "/import-templates", ws, http.StatusOK, riGateGoodTemplate},
		{"import-harness-configs", base + "/import-harness-configs", ws, http.StatusBadRequest, "import_failed"},
		// Both generic routes reach the shared authorizeProjectImport helper on
		// scope=project — one edit site covering two routes.
		{"resources/discover", "/api/v1/resources/discover", generic("template"), http.StatusBadRequest, "discover_failed"},
		{"resources/import", "/api/v1/resources/import", generic("template"), http.StatusBadRequest, "import_failed"},
	}
}

// TestRIUserGate_AllProjectScopeArmsRequireRead is the behavioural pin: every
// project-scope user arm refuses a create-only member, and does so because of
// the READ grant specifically — the create+read row differs from the create-only
// row in exactly one policy.
func TestRIUserGate_AllProjectScopeArmsRequireRead(t *testing.T) {
	f := wsGateSetup(t)
	f.srv.SetStorage(newMockStorage("test-bucket"))
	wsPath := riGateSeedWorkspace(t, f)
	routes := riUserRoutes(t, f, wsPath)

	createOnly := riUserMember(t, f, "riuser-createonly")
	riUserGrantCreate(t, f, createOnly, "co")

	createRead := riUserMember(t, f, "riuser-createread")
	riUserGrantCreate(t, f, createRead, "cr")
	riUserGrant(t, f, createRead.ID, "riuser-read-cr", "project", []string{"read", "list"})

	// The ROW VALIDITY WITNESS, and it deliberately sits OFF the axis under test.
	// A member with no grants at all must be refused on every row, before and
	// after the fix alike — this row is fix-invariant, so it reports whether the
	// row itself is wired up (right path, right method, request actually reaching
	// the handler) without borrowing any of its credibility from the behaviour
	// being pinned. Using "the read-holding arm was served" as the witness instead
	// would be circular: pre-fix the read grant changes nothing, so a broken row
	// and the bug itself look identical (rev1 hit exactly this and reported it).
	noGrant := riUserMember(t, f, "riuser-nogrant")

	// WITNESS 2 — THE AXIS IS LIVE, and it fails INDEPENDENTLY of witness 1
	// above. The no-grant arm proves a row REACHES the decision; it says nothing
	// about whether the read grant under test actually FUNCTIONS. Those two can
	// fail separately: a row can reach a gate whose axis is dead, and an axis can
	// be live on rows that never arrive. This checks the read grant against
	// /workspace/files — an endpoint that already honours project read and that
	// the N78 fix does not touch, so the witness stays valid after the fix.
	//
	// Without it a MISSPELLED grant is invisible pre-fix, because both arms
	// simply agree — which is exactly what the bug looks like. Not hypothetical:
	// a grant on "harness-config" where the code keys "harness_config" sent two
	// arms green on a grant that never existed. This converts that silence into
	// a failure.
	//
	// This pair is also the finding's FRAMING: the SAME read grant, SAME caller,
	// SAME project is HONOURED here and IGNORED at all six import/discover rows.
	t.Run("WITNESS 2 the read axis is live at an endpoint N78 does not touch", func(t *testing.T) {
		wsFiles := "/api/v1/projects/" + f.projA.ID + "/workspace/files"
		// Measured in THIS file, not inherited: a renamed witness route (decoy B2)
		// lands on THIS arm as a 404, not on the arm below — the create-only call
		// happens first, so it absorbs the route error. An unseparated "axis dead"
		// message would therefore have named a cause it did not establish, on a
		// run where the axis was perfectly live. 404 is separable, so it earns a
		// named cause; the remaining codes do not.
		rec := f.asUser(t, createOnly, http.MethodGet, wsFiles, nil)
		if rec.Code != http.StatusForbidden {
			if rec.Code == http.StatusNotFound {
				t.Fatalf("WITNESS FAILED (route wrong): the witness endpoint %s returned 404. "+
					"Cause IS established: the route is renamed or misspelled — this says NOTHING "+
					"about the read axis. body=%s", wsFiles, strings.TrimSpace(rec.Body.String()))
			}
			t.Fatalf("WITNESS FAILED (axis not established): a create-only member was not refused "+
				"project read at %s (code=%d). CAUSE NOT ESTABLISHED — this arm fires identically "+
				"on several unrelated faults. Candidates: the read axis genuinely is not live; the "+
				"fixture principal carries a grant it should not; an auth/token setup fault; a "+
				"handler that answers before authorization. Nothing below is a read finding until "+
				"this is resolved. body=%s", wsFiles, rec.Code, strings.TrimSpace(rec.Body.String()))
		}

		// MESSAGE SPECIFICITY IS EARNED PER CODE, NOT ASSUMED. A decoy proves a
		// control is not dead; it does not prove it is not PROMISCUOUS. Measured
		// with three decoys at 5e757ad0: (A) resource type misspelled and (B1)
		// fixture principal absent from the store BOTH answer 403 on this arm and
		// are indistinguishable here, while (B2) a renamed route answers 404. So
		// the non-404 text must NOT name a cause — AN ERROR STRING THAT NAMES A
		// CAUSE IT DID NOT ESTABLISH LICENSES A FIX SCOPED TO THAT CAUSE. The 404
		// does separate, so it alone earns a named cause.
		rec = f.asUser(t, createRead, http.MethodGet, wsFiles, nil)
		if rec.Code != http.StatusOK {
			if rec.Code == http.StatusNotFound {
				t.Fatalf("WITNESS FAILED (route wrong): the witness endpoint %s returned 404. "+
					"Cause IS established: the route is renamed or misspelled. body=%s",
					wsFiles, strings.TrimSpace(rec.Body.String()))
			}
			t.Fatalf("WITNESS FAILED (grant inert): the read grant did not admit its holder at %s "+
				"(code=%d). CAUSE NOT ESTABLISHED — decoys A and B1 both produce this exact code. "+
				"Candidates, in the order they have actually bitten: (a) resource-type SPELLING, "+
				"e.g. harness-config vs harness_config; (b) the fixture principal is absent from "+
				"the store or its policy binding was not created; (c) the grant's scope or project "+
				"does not match the resource; (d) an expired or malformed token. The list is not "+
				"closed — do not read it as a diagnosis. Do not read any row below as a security "+
				"result. body=%s",
				wsFiles, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	})

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			t.Run("ROW VALIDITY no-grant member refused", func(t *testing.T) {
				rec := f.asUser(t, noGrant, http.MethodPost, route.path, route.body)
				require.Equal(t, http.StatusForbidden, rec.Code,
					"ROW INVALID for %s — a member with NO grants was not refused (got %d). "+
						"This row proves nothing about N78 either way: treat the sibling "+
						"assertions in this row as unrun, not as passes. Most likely the path "+
						"or method is wrong and the request never reached the handler; body=%s",
					route.name, rec.Code, rec.Body.String())
			})

			t.Run("create-only member refused", func(t *testing.T) {
				rec := f.asUser(t, createOnly, http.MethodPost, route.path, route.body)
				// THE PREDICATE IS "REFUSED", NOT "NOT 200", and the difference is
				// not pedantry: pre-fix, four of these six rows answered 400 from
				// DOWNSTREAM validation, which a NotEqual(200) assertion banks as a
				// pass. Those 400s were the opposite of a refusal — "no scion
				// harness-configs found at workspace path ..." proves the handler
				// READ THE WORKSPACE for an unauthorized caller, and the generic
				// rows' fetch failure proves the server made an OUTBOUND REQUEST on
				// an unauthorized caller's instruction. Both are work done on behalf
				// of someone who should have been turned away at the door.
				require.Equal(t, http.StatusForbidden, rec.Code,
					"a create-only member must be REFUSED at %s, not merely fail downstream: "+
						"any non-403 here means the caller got past the gate and the server "+
						"acted for them; body=%s",
					route.name, rec.Body.String())
				riGateRequireEnumerationAbsent(t, rec.Body.String())
			})

			t.Run("create+read member passes", func(t *testing.T) {
				rec := f.asUser(t, createRead, http.MethodPost, route.path, route.body)
				require.Equal(t, route.passCode, rec.Code,
					"adding ONLY the project read grant must let the same caller through %s, "+
						"proving the gate keys on read and does not refuse everything; body=%s",
					route.name, rec.Body.String())
				require.Contains(t, rec.Body.String(), route.passBody,
					"a passed gate must reach the handler and render %q, proving the request was "+
						"not dead-routed; body=%s", route.passBody, rec.Body.String())
			})
		})
	}
}

// TestRIUserGate_AgentArmsAgreeOnTheSameRoutes carries the five agent arms
// through the SAME route table as the cross-caller control. Before N78 these two
// caller kinds disagreed on this route set; the property pinned here is that they
// now agree. Keeping it in this file means a change that re-splits them fails
// next to the user rows rather than in a different file.
func TestRIUserGate_AgentArmsAgreeOnTheSameRoutes(t *testing.T) {
	f := wsGateSetup(t)
	f.srv.SetStorage(newMockStorage("test-bucket"))
	wsPath := riGateSeedWorkspace(t, f)
	routes := riUserRoutes(t, f, wsPath)

	good := riGateCreateAgent(t, f, "riuser-agent-default")
	revoked := riGateCreateAgent(t, f, "riuser-agent-revoked")
	riGateRevokeRead(t, f, revoked.ID)

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			t.Run("read-revoked agent refused", func(t *testing.T) {
				rec := riGateAsAgentCreate(t, f, revoked, http.MethodPost, route.path, route.body)
				require.Equal(t, http.StatusForbidden, rec.Code,
					"read-revoked agent must be refused at %s; body=%s", route.name, rec.Body.String())
				riGateRequireEnumerationAbsent(t, rec.Body.String())
			})

			t.Run("default agent passes", func(t *testing.T) {
				rec := riGateAsAgentCreate(t, f, good, http.MethodPost, route.path, route.body)
				require.Equal(t, route.passCode, rec.Code,
					"default in-project create-scope agent must still pass %s; body=%s",
					route.name, rec.Body.String())
				require.Contains(t, rec.Body.String(), route.passBody,
					"a passed gate must render %q; body=%s", route.passBody, rec.Body.String())
			})
		})
	}
}

// TestRIUserGate_EveryProjectScopeUserArmIsGated is the inventory, and it is the
// reason an EIGHTH arm cannot be added silently. It does not compare against a
// number written down here — a literal count is a measurement with its filter
// amputated, and it goes stale on the first addition. It derives every site from
// the handler source and asserts ADJACENCY: each project-scoped user create check
// must be followed by a read gate IN ITS OWN ARM.
//
// IT ASSERTS ADJACENCY BECAUSE CARDINALITY WAS NOT ENOUGH, and that is measured,
// not anticipated (N84, rev1, against b6e8ddb9). The first version of this test
// compared two derived totals, project-scope arms against read gates. rev1 moved
// the discover-templates gate into the import-templates arm — one arm with two
// gates, one with none, totals still 5 == 5 — and this test PASSED, as did the
// cardinality tripwire below. Only the behavioural table caught it. A COUNT
// SURVIVES ANY REARRANGEMENT THAT PRESERVES THE TOTAL, so it can see absence but
// never displacement. The AST walk below compares positions against enclosing
// blocks instead, which makes displacement unsayable rather than merely unlikely,
// and names the offending arm by line.
//
// The behavioural table is not a substitute for this. Its six rows are HAND
// WRITTEN literals, so a NEW project-scope arm whose gate lands in the wrong arm
// is invisible to it — there is no row for a route nobody added a row for. That
// is the exact gap this test now closes, and it is why the two are kept together.
//
// Add a project-scoped user arm without authorizeImportUserRead and this reds,
// naming the arm. Add one WITH the gate and it stays green, correctly.
//
// The two global-scope arms of handleResourcesImport/handleResourcesDiscover are
// excluded by SHAPE, not by name or line: they pass Resource{Type: authzType},
// deliberately ownerless and parentless (hub-admin by construction), and carry no
// project id. authorizeImportUserRead keys on Resource{Type:"project", ID: projectID},
// which is meaningless with an empty id, so the mirror is undefined there rather
// than merely unapplied. Whether those arms need a hub-wide read of their own is
// filed separately as N80 and must be measured on its own justification — do not
// fold it in here by widening this filter.
func TestRIUserGate_EveryProjectScopeUserArmIsGated(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, riUserSourceFile, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "inventory cannot run if %s moved or stopped parsing; update riUserSourceFile", riUserSourceFile)

	// All block statements, so the ARM an expression sits in can be identified
	// structurally instead of by counting lines between it and its neighbour.
	var blocks []*ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if b, ok := n.(*ast.BlockStmt); ok {
			blocks = append(blocks, b)
		}
		return true
	})
	// The arm is the SMALLEST block containing the position. Smallest matters:
	// every one of these calls is also inside its function body, and comparing
	// function bodies would let two arms in one function be treated as one.
	arm := func(pos token.Pos) *ast.BlockStmt {
		var best *ast.BlockStmt
		for _, b := range blocks {
			if b.Pos() <= pos && pos <= b.End() {
				if best == nil || b.Pos() > best.Pos() {
					best = b
				}
			}
		}
		return best
	}

	isIdent := func(e ast.Expr, name string) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == name
	}
	// GLOBAL SHAPE, excluded BY SHAPE and not by name or line: a resource literal
	// whose only field is Type. Those arms are deliberately ownerless and
	// parentless and carry no project id, so the mirror is undefined there rather
	// than merely unapplied (N80 measures them separately — do not fold them in
	// here by widening this filter).
	isGlobalResource := func(e ast.Expr) bool {
		lit, ok := e.(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 1 {
			return false
		}
		kv, ok := lit.Elts[0].(*ast.KeyValueExpr)
		return ok && isIdent(kv.Key, "Type")
	}

	var projectCreates, gates []token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "CheckAccess":
			if len(call.Args) == 4 && isIdent(call.Args[1], "userIdent") &&
				isIdent(call.Args[3], "ActionCreate") && !isGlobalResource(call.Args[2]) {
				projectCreates = append(projectCreates, call.Pos())
			}
		case "authorizeImportUserRead":
			gates = append(gates, call.Pos())
		}
		return true
	})

	// ANTI-VACUITY, both sides. A filter that has stopped matching produces a
	// green indistinguishable from a clean file.
	require.NotEmpty(t, projectCreates,
		"found no project-scope user create checks in %s — the AST filter has stopped "+
			"matching and this test is measuring nothing", riUserSourceFile)
	require.NotEmpty(t, gates,
		"zero authorizeImportUserRead calls found: the fix has been removed wholesale, or "+
			"the call form changed and this inventory is now blind")

	// ADJACENCY, not cardinality. Each project-scope create must be followed by a
	// gate IN ITS OWN ARM. Totals cannot launder a displacement: moving a gate out
	// of one arm and into another keeps every count identical and reds here twice,
	// naming both the starved arm and the doubled one by line.
	gatesFor := func(block *ast.BlockStmt, after token.Pos) int {
		n := 0
		for _, g := range gates {
			if arm(g) == block && g > after {
				n++
			}
		}
		return n
	}
	//
	// Every offending arm is collected and reported TOGETHER rather than failing
	// on the first. A displacement always produces at least two faults — a starved
	// arm and a doubled one — and stopping at the first names only one of them,
	// which reads as a simple missing gate and invites exactly the wrong repair:
	// adding a second gate to the arm that already has two.
	var offenders []string
	for _, c := range projectCreates {
		pos := fset.Position(c)
		block := arm(c)
		require.NotNil(t, block, "%s:%d: create check is not inside any block", pos.Filename, pos.Line)
		if n := gatesFor(block, c); n != 1 {
			offenders = append(offenders, fmt.Sprintf(
				"  %s:%d has %d in-arm read gates, want exactly 1", pos.Filename, pos.Line, n))
		}
	}
	require.Empty(t, offenders,
		"every project-scope user create check must be followed by exactly one "+
			"s.authorizeImportUserRead IN ITS OWN ARM. Offending arms:\n%s\n"+
			"Either a new arm was added without the read gate, or a gate was moved into a "+
			"neighbouring arm — which leaves every total unchanged, so counting cannot see "+
			"it. If one arm shows 0 and another shows 2, that is a DISPLACEMENT: move the "+
			"gate back, do not add a new one.",
		strings.Join(offenders, "\n"))

	// The converse: no gate may sit in an arm that has no project-scope create.
	// Without this, the destination of a displacement is only implicated through
	// the arm it robbed, and a gate parked in an unrelated block passes unnoticed.
	for _, g := range gates {
		pos := fset.Position(g)
		block := arm(g)
		found := false
		for _, c := range projectCreates {
			if arm(c) == block && g > c {
				found = true
				break
			}
		}
		require.True(t, found,
			"%s:%d — this authorizeImportUserRead does not follow any project-scope user "+
				"create check in its own arm. A read gate that is not behind a create check "+
				"is either misplaced or guarding an arm this inventory does not model.",
			pos.Filename, pos.Line)
	}

	t.Logf("adjacency verified: %d project-scope user arms, %d read gates, each paired in-arm",
		len(projectCreates), len(gates))
}

// TestN78_BranchCoverage is the CARDINALITY control, authored by rev1 and kept
// deliberately. Its failure symptom is the one the other tests cannot produce:
// SILENCE. It counts identity branches in the handler file — a source the fix
// author does not get to restate — so that a change in the SHAPE of the file
// surfaces even when every behavioural row still passes.
//
// WHY A LITERAL COUNT IS CORRECT HERE, having argued the opposite elsewhere in
// this package. A count that DUPLICATES a list it sits next to is a liability:
// it restates a fact the list already carries, and an addition invalidates it
// while touching neither (see the OwnerID-visibility note in
// handlers_agents_core.go, where the numeral was deleted for exactly that
// reason). This count duplicates nothing. It is a TRIPWIRE whose entire job is to
// fail when the number changes, and failing on addition is the feature rather
// than the defect. Same numeral, opposite function — so do not "fix" this by
// deriving it, and do not delete it to make it stop failing. If an arm is added
// or removed, RE-DERIVE the row set above and then update these numbers as a
// deliberate act.
//
// It also covers a gap the relationship assertion in
// TestRIUserGate_EveryProjectScopeUserArmIsGated provably cannot. That test
// compares project-scoped arms against read gates, so it stays green if BOTH
// move together — including the dangerous case where a project-scoped arm is
// converted to the parentless global shape, which silently drops one route out
// of the gated set while keeping the two derived numbers equal. The pair is
// addition-safe AND conversion-safe; neither is both on its own.
//
// 7 user / 5 agent measured at 5e757ad0 by rev1 and independently at this tip;
// the N78 fix adds authorization calls, not identity branches, so it does not
// move either number.
func TestN78_BranchCoverage(t *testing.T) {
	src, err := os.ReadFile(riUserSourceFile)
	require.NoError(t, err)
	users := regexp.MustCompile(`GetUserIdentityFromContext`).FindAllString(string(src), -1)
	agents := regexp.MustCompile(`GetAgentIdentityFromContext`).FindAllString(string(src), -1)
	t.Logf("enumerated user branches=%d agent branches=%d", len(users), len(agents))
	require.Equal(t, 7, len(users),
		"user-branch count changed: RE-DERIVE the N78 row set from the file, do not patch this number")
	require.Equal(t, 5, len(agents),
		"agent-branch count changed: RE-DERIVE the control side")
}
