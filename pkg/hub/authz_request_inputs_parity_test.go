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

// Parity test matrix for the request-local authorization input memo
// (ptone/scion#2376/#2377), per
// gs://scion-xproject-exchange/slow-list/design/authz-reuse.md (v3.4),
// section 8. Row IDs in test/comment names (P1, A7, X7, ...) match the
// design's table exactly.
//
// Harness rules (design 8, H1-H5) implemented below:
//   - H1: faults are selected independently per run, by an n-th-call number
//     or a ctx predicate. A candidate run NEVER reuses a call index computed
//     from the reference run (R3-B1) — the memo shifts indices.
//   - H2: every fault/cancel row asserts the fault actually fired where
//     expected, both to prove the row isn't vacuous and to prove the
//     candidate skipped a call the row says it should skip.
//   - H3: any row comparing store-call counts builds a fresh *AuthzService
//     per run (backfillDone latches for the service's lifetime).
//   - H4: two cancellation store variants — (i) wraps ctx.Err(), (ii)
//     ignores cancellation.
//   - H5: a test-local store-call counting wrapper (memoTestStore) stands in
//     for #2392's per-request counters, which do not exist yet.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Harness: fault-injecting, call-counting store wrapper (H1, H3, H5)
// =============================================================================

// storeCallRecord captures one intercepted call for X6/X7-style assertions
// that a call did or did not observe an input/edges memo, or run under the
// per-decision delegation ceiling cache.
type storeCallRecord struct {
	method        string
	n             int // 1-based call number for this method on this wrapper
	hasInputMemo  bool
	hasEdgesMemo  bool
	ceilingActive bool
}

// memoTestStore wraps a real store.Store and intercepts the methods the
// design's memo serves or that its tests need to fault: the five memoized
// loaders (GetEffectiveGroups[ForAgent], ListRoleBindingsForPrincipals,
// GetRoleDefinitionsByIDs, ListAccessConstraints, GetDelegationEdgesForDelegate)
// plus the ceiling's own direct calls (GetRoleDefinition, GetUser, GetAgent,
// GetHubSetting), which the memo never serves (H1).
//
// fault, when non-nil, is consulted after the call is counted and (if
// recordCalls) recorded, and before delegating to the real store. Returning
// a non-nil error short-circuits the real call, exactly modeling a store
// fault; the wrapper never mutates or fabricates a success.
type memoTestStore struct {
	store.Store

	mu          sync.Mutex
	counts      map[string]int
	records     []storeCallRecord
	recordCalls bool
	fault       func(method string, n int, ctx context.Context) error

	// ignoreCancel implements H4 cancellation store variant (ii): the real
	// delegate call is made on a ctx with cancellation stripped, so the real
	// store answers as if the ctx were live even after the caller's ctx was
	// cancelled. Variant (i) needs no special handling here — a fault func
	// that checks ctx.Err() and returns a wrapped error is sufficient.
	ignoreCancel bool
}

func newMemoTestStore(s store.Store) *memoTestStore {
	return &memoTestStore{Store: s, counts: make(map[string]int)}
}

// delegateCtx returns the ctx to use for the real store call: unchanged for
// H4 variant (i) (and for every non-cancellation row, where ignoreCancel is
// false), or with cancellation stripped for variant (ii).
func (m *memoTestStore) delegateCtx(ctx context.Context) context.Context {
	if m.ignoreCancel {
		return context.WithoutCancel(ctx)
	}
	return ctx
}

// cancelHonouringFault is H4 store variant (i): once the ctx is done, every
// intercepted call returns a wrapped ctx.Err() instead of reaching the real
// store.
func cancelHonouringFault(_ string, _ int, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("store call after cancel: %w", err)
	}
	return nil
}

func (m *memoTestStore) call(ctx context.Context, method string) error {
	m.mu.Lock()
	m.counts[method]++
	n := m.counts[method]
	if m.recordCalls {
		m.records = append(m.records, storeCallRecord{
			method:        method,
			n:             n,
			hasInputMemo:  authzInputMemoFromContext(ctx) != nil,
			hasEdgesMemo:  delegationEdgesMemoFromContext(ctx) != nil,
			ceilingActive: getDelegationCeilingCache(ctx) != nil,
		})
	}
	fault := m.fault
	m.mu.Unlock()
	if fault != nil {
		return fault(method, n, ctx)
	}
	return nil
}

func (m *memoTestStore) countOf(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[method]
}

func (m *memoTestStore) recordedCalls() []storeCallRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]storeCallRecord, len(m.records))
	copy(out, m.records)
	return out
}

func (m *memoTestStore) GetEffectiveGroups(ctx context.Context, userID string) ([]string, error) {
	if err := m.call(ctx, "GetEffectiveGroups"); err != nil {
		return nil, err
	}
	return m.Store.GetEffectiveGroups(m.delegateCtx(ctx), userID)
}

func (m *memoTestStore) GetEffectiveGroupsForAgent(ctx context.Context, agentID string) ([]string, error) {
	if err := m.call(ctx, "GetEffectiveGroupsForAgent"); err != nil {
		return nil, err
	}
	return m.Store.GetEffectiveGroupsForAgent(m.delegateCtx(ctx), agentID)
}

func (m *memoTestStore) ListRoleBindingsForPrincipals(ctx context.Context, principals []store.PrincipalRef, scopeTypes, scopeIDs []string) ([]*store.RoleBinding, error) {
	if err := m.call(ctx, "ListRoleBindingsForPrincipals"); err != nil {
		return nil, err
	}
	return m.Store.ListRoleBindingsForPrincipals(m.delegateCtx(ctx), principals, scopeTypes, scopeIDs)
}

func (m *memoTestStore) GetRoleDefinitionsByIDs(ctx context.Context, ids []string) (map[string]*store.RoleDefinition, error) {
	if err := m.call(ctx, "GetRoleDefinitionsByIDs"); err != nil {
		return nil, err
	}
	return m.Store.GetRoleDefinitionsByIDs(m.delegateCtx(ctx), ids)
}

func (m *memoTestStore) GetRoleDefinition(ctx context.Context, id string) (*store.RoleDefinition, error) {
	if err := m.call(ctx, "GetRoleDefinition"); err != nil {
		return nil, err
	}
	return m.Store.GetRoleDefinition(m.delegateCtx(ctx), id)
}

func (m *memoTestStore) ListAccessConstraints(ctx context.Context, limit, offset int) ([]*store.AccessConstraint, error) {
	if err := m.call(ctx, "ListAccessConstraints"); err != nil {
		return nil, err
	}
	return m.Store.ListAccessConstraints(m.delegateCtx(ctx), limit, offset)
}

func (m *memoTestStore) GetDelegationEdgesForDelegate(ctx context.Context, delegateType, delegateID string) ([]*store.DelegationEdge, error) {
	if err := m.call(ctx, "GetDelegationEdgesForDelegate"); err != nil {
		return nil, err
	}
	return m.Store.GetDelegationEdgesForDelegate(m.delegateCtx(ctx), delegateType, delegateID)
}

func (m *memoTestStore) GetUser(ctx context.Context, id string) (*store.User, error) {
	if err := m.call(ctx, "GetUser"); err != nil {
		return nil, err
	}
	return m.Store.GetUser(m.delegateCtx(ctx), id)
}

func (m *memoTestStore) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	if err := m.call(ctx, "GetAgent"); err != nil {
		return nil, err
	}
	return m.Store.GetAgent(m.delegateCtx(ctx), id)
}

func (m *memoTestStore) GetHubSetting(ctx context.Context, section string) (*store.HubSetting, error) {
	if err := m.call(ctx, "GetHubSetting"); err != nil {
		return nil, err
	}
	return m.Store.GetHubSetting(m.delegateCtx(ctx), section)
}

// errInjected is the sentinel fault error every row injects. Its identity is
// never asserted on (design 6.3, R5-Nit1) — callers compare Error() strings
// or errors.Is(context.Canceled) — but tests use errors.Is against this
// sentinel to confirm the deny actually came from the injected fault and not
// some unrelated error.
var errInjected = errors.New("authz_request_inputs_parity_test: injected store fault")

// callNFault returns a fault func that fails a method's n-th call only.
func callNFault(method string, n int) func(string, int, context.Context) error {
	return func(m string, callN int, _ context.Context) error {
		if m == method && callN == n {
			return fmt.Errorf("injected fault on %s call #%d: %w", m, callN, errInjected)
		}
		return nil
	}
}

// alwaysFault returns a fault func that fails every call to method.
func alwaysFault(method string) func(string, int, context.Context) error {
	return func(m string, _ int, _ context.Context) error {
		if m == method {
			return fmt.Errorf("injected fault on every %s call: %w", m, errInjected)
		}
		return nil
	}
}

// =============================================================================
// Harness: synchronous decision-audit recorder (N3)
// =============================================================================

type recordingAuditEmitter struct {
	mu      sync.Mutex
	records []*store.DecisionAuditRecord
}

func (r *recordingAuditEmitter) EmitDecisionAudit(_ context.Context, record *store.DecisionAuditRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *record
	r.records = append(r.records, &cp)
}

func (r *recordingAuditEmitter) snapshot() []*store.DecisionAuditRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*store.DecisionAuditRecord, len(r.records))
	copy(out, r.records)
	return out
}

// newRecordingAuthz builds a fresh AuthzService (H3) over s (typically a
// *memoTestStore) with a synchronous recording emitter and
// DecisionAuditSampleRate = 1.0, so every decision is audited and captured
// in call order.
func newRecordingAuthz(s store.Store) (*AuthzService, *recordingAuditEmitter) {
	authz := NewAuthzService(s, slog.Default())
	emitter := &recordingAuditEmitter{}
	authz.SetDecisionAuditEmitter(emitter)
	authz.DecisionAuditSampleRate = 1.0
	return authz, emitter
}

// assertAuditSequenceEqual compares two audit-record sequences field by
// field apart from Timestamp (N3).
func assertAuditSequenceEqual(t *testing.T, ref, cand []*store.DecisionAuditRecord, msgAndArgs ...interface{}) {
	t.Helper()
	if !assert.Equal(t, len(ref), len(cand), "audit record count mismatch") {
		return
	}
	for i := range ref {
		r, c := *ref[i], *cand[i]
		r.Timestamp, c.Timestamp = time.Time{}, time.Time{}
		assert.Equal(t, r, c, append([]interface{}{fmt.Sprintf("audit record %d mismatch", i)}, msgAndArgs...)...)
	}
}

// assertDecisionsEqual is reflect.DeepEqual on the whole Decision (N3),
// with a readable failure message.
func assertDecisionsEqual(t *testing.T, ref, cand Decision, msgAndArgs ...interface{}) {
	t.Helper()
	if !reflect.DeepEqual(ref, cand) {
		assert.Fail(t, fmt.Sprintf("decisions differ:\n  reference: %+v\n  candidate: %+v", ref, cand), msgAndArgs...)
	}
}

// =============================================================================
// Fixtures
// =============================================================================

// a1Fixture builds the A1 row's fixture and is reused, unmodified, by every
// A-row and by X7: an agent principal in its own project, with lifecycle
// scopes (AgentRoleFull), and a single-level delegation edge to a
// **non-admin** user delegator who holds an **in-scope active project
// binding** (ProjectRoleAdmin, not a system role) — required so that
// IsSystemAdmin does not short-circuit (authz_delegation_ceiling.go:635) and
// getEffectivePermissions loads constraints (authz.go:1889 at 73ebd02,
// :1918 at ba762d56), which A7/A7'/X7 all depend on.
type a1Fixture struct {
	authz       *AuthzService
	store       store.Store
	projectID   string
	otherProjID string
	delegatorID string
	agentID     string
	targetID    string // an agent resource in the agent's own project
	agent       AgentIdentity
	resource    Resource
}

// newA1Fixture creates the fixture on s using name as a per-test ID salt, so
// distinct tests sharing one store (rare in this file; most build their own
// via authzTestSetup) do not collide.
func newA1Fixture(t *testing.T, s store.Store, name string) *a1Fixture {
	t.Helper()
	projectID := tid(name + "-project")
	otherProjID := tid(name + "-other-project")
	delegatorID := tid(name + "-delegator")
	agentID := tid(name + "-agent")
	targetID := tid(name + "-target")

	createDCProject(t, s, projectID, name+"-proj")
	createDCProject(t, s, otherProjID, name+"-other-proj")
	// A non-admin delegator with an in-scope ACTIVE project binding.
	createDCUser(t, s, delegatorID, name+"-delegator@test.com", projectID, store.ProjectRoleAdmin)
	createDCAgent(t, s, agentID, projectID, delegatorID, AgentRoleFull)
	createDCAgent(t, s, targetID, projectID, delegatorID, AgentRoleFull)
	createDCEdge(t, s, store.DelegationPrincipalUser, delegatorID, store.DelegationPrincipalAgent, agentID,
		store.RoleScopeProject, projectID, string(AgentRoleFull))

	return &a1Fixture{
		store:       s,
		projectID:   projectID,
		otherProjID: otherProjID,
		delegatorID: delegatorID,
		agentID:     agentID,
		targetID:    targetID,
		agent:       dcAgentIdentity(agentID, projectID, AgentRoleFull),
		resource:    Resource{Type: "agent", ID: targetID, ParentType: "project", ParentID: projectID, OwnerID: delegatorID},
	}
}

// agentAllActions returns every (resource action, scope action) pair A1-A8
// cover: all 8 ResourceActions["agent"] plus all 4 ScopeActions["agent"].
// Scope actions are evaluated against a Resource carrying only the parent
// project, matching ComputeScopeCapabilities's shape.
func agentAllActions() []Action {
	var out []Action
	out = append(out, ResourceActions["agent"]...)
	out = append(out, ScopeActions["agent"]...)
	return out
}

// p1Fixture is an ordinary hub member reached only through a group binding
// (P1): a "members" group holding a project role binding, with the user a
// member of that group and nothing else. Also serves E1-E5b/E7, whose rows
// pin the principal to "P1, a user, so there is no delegation ceiling."
type p1Fixture struct {
	store     store.Store
	projectID string
	userID    string
	groupID   string
	user      UserIdentity
	agentRes  Resource // an agent resource in the member's project
}

func newP1Fixture(t *testing.T, s store.Store, name string) *p1Fixture {
	t.Helper()
	ctx := context.Background()
	projectID := tid(name + "-project")
	userID := tid(name + "-user")
	ownerID := tid(name + "-owner")
	agentID := tid(name + "-agent")

	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: name + "-proj", Name: name, OwnerID: ownerID}))
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: userID, Email: name + "@test.com", DisplayName: name, Role: "member", Status: "active"}))

	group := &store.Group{ID: tid(name + "-group"), Name: name + " members", Slug: "project:" + projectID + ":members", GroupType: store.GroupTypeExplicit, ProjectID: projectID}
	require.NoError(t, s.CreateGroup(ctx, group))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{GroupID: group.ID, MemberID: userID, MemberType: store.GroupMemberTypeUser, Role: store.GroupMemberRoleMember}))

	rd, err := s.GetRoleDefinitionByName(ctx, store.ProjectRoleMember, store.RoleScopeProject)
	require.NoError(t, err)
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: rd.ID,
		PrincipalType:    "group",
		PrincipalID:      group.ID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          projectID,
		CreatedBy:        "test",
	})
	require.NoError(t, err)

	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: name + "-agent", Name: name + "-agent", ProjectID: projectID, Phase: "running", OwnerID: ownerID, Ancestry: []string{ownerID}}))

	return &p1Fixture{
		store:     s,
		projectID: projectID,
		userID:    userID,
		groupID:   group.ID,
		user:      NewAuthenticatedUser(userID, name+"@test.com", name, "member", "api"),
		agentRes:  Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID},
	}
}

// projectPrincipalFixture is a project-scoped principal reached through a
// direct (non-group) role binding, for P2 (member), P4 (admin) and similar
// rows that don't need the group-closure shape P1 exercises.
type projectPrincipalFixture struct {
	store     store.Store
	projectID string
	userID    string
	user      UserIdentity
	agentRes  Resource
}

func newProjectPrincipalFixture(t *testing.T, s store.Store, name, roleName string) *projectPrincipalFixture {
	t.Helper()
	ctx := context.Background()
	projectID := tid(name + "-project")
	userID := tid(name + "-user")
	ownerID := tid(name + "-owner")
	agentID := tid(name + "-agent")

	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: name + "-proj", Name: name, OwnerID: ownerID}))
	createDCUser(t, s, userID, name+"@test.com", projectID, roleName)
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: name + "-agent", Name: name + "-agent", ProjectID: projectID, Phase: "running", OwnerID: ownerID, Ancestry: []string{ownerID}}))

	return &projectPrincipalFixture{
		store:     s,
		projectID: projectID,
		userID:    userID,
		user:      NewAuthenticatedUser(userID, name+"@test.com", name, "member", "api"),
		agentRes:  Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID},
	}
}

// =============================================================================
// Reference/candidate raw-loop runner (paths, N3)
// =============================================================================

// rawTuple is one (resource, action) pair evaluated through CheckAccess.
type rawTuple struct {
	resource Resource
	action   Action
}

// agentResourceTuples builds one tuple per ResourceActions["agent"] against
// res, in registry order.
func agentResourceTuples(res Resource) []rawTuple {
	out := make([]rawTuple, 0, len(ResourceActions["agent"]))
	for _, a := range ResourceActions["agent"] {
		out = append(out, rawTuple{res, a})
	}
	return out
}

// runParity runs tuples through the reference path (CheckAccess, plain ctx,
// fresh service) and the candidate path (CheckAccess raw loop under one
// withAuthzInputMemo ctx, fresh service — H3), and asserts deep-equal
// Decisions and audit sequences (N3) for every tuple. Both stores are
// returned for row-specific extra assertions (call counts, fault
// non-vacuity, etc). Rows that need fault injection build refStore/candStore
// themselves (via memoTestStore.fault) and call runParityWithStores instead.
func runParity(t *testing.T, s store.Store, identity Identity, tuples []rawTuple) (refDecisions, candDecisions []Decision, refStore, candStore *memoTestStore) {
	t.Helper()
	return runParityWithStores(t, newMemoTestStore(s), newMemoTestStore(s), identity, tuples)
}

// runParityWithStores is runParity, but the caller supplies (possibly
// fault-injecting) stores, already wrapping the same underlying data. The
// two AuthzServices built here are always fresh (H3).
func runParityWithStores(t *testing.T, refStore, candStore *memoTestStore, identity Identity, tuples []rawTuple) (refDecisions, candDecisions []Decision, retRefStore, retCandStore *memoTestStore) {
	t.Helper()
	ctx := context.Background()

	refAuthz, refEmit := newRecordingAuthz(refStore)
	for _, tp := range tuples {
		refDecisions = append(refDecisions, refAuthz.CheckAccess(ctx, identity, tp.resource, tp.action))
	}

	candAuthz, candEmit := newRecordingAuthz(candStore)
	mctx := withAuthzInputMemo(ctx)
	for _, tp := range tuples {
		candDecisions = append(candDecisions, candAuthz.CheckAccess(mctx, identity, tp.resource, tp.action))
	}

	require.Equal(t, len(refDecisions), len(candDecisions))
	for i := range refDecisions {
		assertDecisionsEqual(t, refDecisions[i], candDecisions[i], "tuple %d: %s %s", i, tuples[i].resource.Type, tuples[i].action)
	}
	assertAuditSequenceEqual(t, refEmit.snapshot(), candEmit.snapshot())
	return refDecisions, candDecisions, refStore, candStore
}

// =============================================================================
// Store-call count test (design section 8, "Store-call tests (#2376, branch 1)")
// =============================================================================

// TestParity_StoreCallCounts_ComputeCapabilitiesBatch pins section 9's
// headline claim for #2376: under the memo, a batch of N agents for one
// user principal costs 1 GetEffectiveGroups, 1 ListRoleBindingsForPrincipals,
// <=1 GetRoleDefinitionsByIDs and P ListAccessConstraints pages, versus 8N
// (8N*P for constraints) without it. K = len(ResourceActions["agent"]) = 8.
func TestParity_StoreCallCounts_ComputeCapabilitiesBatch(t *testing.T) {
	for _, n := range []int{1, 25, 100} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			_, s := authzTestSetup(t)
			name := fmt.Sprintf("cnt-%d", n)
			f := newP1Fixture(t, s, name)
			ctx := context.Background()

			resources := make([]Resource, n)
			for i := 0; i < n; i++ {
				agentID := tid(fmt.Sprintf("%s-agent-%d", name, i))
				require.NoError(t, s.CreateAgent(ctx, &store.Agent{
					ID: agentID, Slug: fmt.Sprintf("%s-agent-%d", name, i), Name: fmt.Sprintf("%s-agent-%d", name, i),
					ProjectID: f.projectID, Phase: "running", OwnerID: f.userID, Ancestry: []string{f.userID},
				}))
				resources[i] = Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: f.projectID, OwnerID: f.userID}
			}

			// H3: a fresh service for each of the two runs.
			memoStore := newMemoTestStore(s)
			memoAuthz, _ := newRecordingAuthz(memoStore)
			memoAuthz.ComputeCapabilitiesBatch(withAuthzInputMemo(ctx), f.user, resources, "agent")
			assert.Equal(t, 1, memoStore.countOf("GetEffectiveGroups"), "GetEffectiveGroups under the memo")
			assert.Equal(t, 1, memoStore.countOf("ListRoleBindingsForPrincipals"), "ListRoleBindingsForPrincipals under the memo")
			assert.LessOrEqual(t, memoStore.countOf("GetRoleDefinitionsByIDs"), 1, "GetRoleDefinitionsByIDs under the memo")
			pages := memoStore.countOf("ListAccessConstraints")
			assert.GreaterOrEqual(t, pages, 1, "at least one constraint page")

			plainStore := newMemoTestStore(s)
			plainAuthz, _ := newRecordingAuthz(plainStore)
			plainAuthz.ComputeCapabilitiesBatch(ctx, f.user, resources, "agent")
			k := len(ResourceActions["agent"])
			require.Equal(t, 8, k, "test assumes 8 agent resource actions; ResourceActions[\"agent\"] changed")
			assert.Equal(t, k*n, plainStore.countOf("GetEffectiveGroups"), "GetEffectiveGroups without the memo")
			assert.Equal(t, k*n, plainStore.countOf("ListRoleBindingsForPrincipals"), "ListRoleBindingsForPrincipals without the memo")
			assert.Equal(t, k*n, plainStore.countOf("GetRoleDefinitionsByIDs"), "GetRoleDefinitionsByIDs without the memo")
			assert.Equal(t, k*n*pages, plainStore.countOf("ListAccessConstraints"), "ListAccessConstraints without the memo")
		})
	}
}

// =============================================================================
// P rows: same-decision parity across principal classes
// =============================================================================

// TestParity_P1_OrdinaryMemberViaGroup is design row P1: deep-equal;
// GetEffectiveGroups loaded once under the memo.
func TestParity_P1_OrdinaryMemberViaGroup(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "p1")
	_, _, _, candStore := runParity(t, s, f.user, agentResourceTuples(f.agentRes))
	assert.Equal(t, 1, candStore.countOf("GetEffectiveGroups"), "groups loaded once under the memo")
}

// TestParity_P4_ProjectAdmin is design row P4: deep-equal; every action is
// decided. "Decided" means every tuple reaches a real decision (not a
// zero-load short-circuit like D2) — not that a project admin is allowed on
// every one: some agent actions (e.g. grant_hub_mode) have additional
// non-role-binding gates that a project-admin role binding alone does not
// satisfy. H2 non-vacuity: at least one tuple must be allowed and at least
// one denied, so the row isn't degenerate in either direction.
func TestParity_P4_ProjectAdmin(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newProjectPrincipalFixture(t, s, "p4", store.ProjectRoleAdmin)
	refStore := newMemoTestStore(s)
	refDecisions, _, _, _ := runParityWithStores(t, refStore, newMemoTestStore(s), f.user, agentResourceTuples(f.agentRes))
	var sawAllow, sawDeny bool
	for _, d := range refDecisions {
		if d.Allowed {
			sawAllow = true
		} else {
			sawDeny = true
		}
	}
	assert.True(t, sawAllow, "H2: at least one action must be allowed for a project admin")
	assert.True(t, sawDeny, "H2: at least one action must still be gated by something beyond the role binding")
	assert.Equal(t, len(ResourceActions["agent"]), refStore.countOf("GetEffectiveGroups"), "sanity: every tuple reached step 2 (no zero-load short-circuit)")
}

// TestParity_U1_ScopedUATAgentReadOnly is design row U1: a UAT scoped to
// project X with only the agent:read selector, built via
// NewScopedUserIdentityWithCeiling / BuildCeilingFromSelectors (a zero
// ceiling denies everything, so U1-U3 must carry a real one, per the
// ba762d56 rebase note). Must show deep-equal gate reasons and the 7a
// ceiling restriction.
func TestParity_U1_ScopedUATAgentReadOnly(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "u1")
	ceiling, ok := permissions.BuildCeilingFromSelectors([]string{"agent:read"})
	require.True(t, ok, "agent:read must resolve to a valid ceiling selector")
	// Step 1 (enforceUATConstraints) checks the identity's legacy scopes
	// list independently of the ceiling step 7a checks, so both must name
	// agent:read for the request to reach the kernel at all.
	scoped := NewScopedUserIdentityWithCeiling(f.user, f.projectID, []string{"agent:read"}, "u1-uat", ceiling)

	refDecisions, _, _, _ := runParity(t, s, scoped, agentResourceTuples(f.agentRes))
	// Non-vacuity: agent.read must be allowed and every other action denied
	// by the ceiling restriction, so the row actually exercises both 7a
	// branches, not just one.
	var sawAllow, sawCeilingDeny bool
	for i, d := range refDecisions {
		if ResourceActions["agent"][i] == ActionRead {
			assert.True(t, d.Allowed, "agent:read must be allowed under its own ceiling selector: reason=%q", d.Reason)
			sawAllow = true
		} else if !d.Allowed {
			sawCeilingDeny = true
		}
	}
	assert.True(t, sawAllow, "fixture must reach an allow (H2 non-vacuity)")
	assert.True(t, sawCeilingDeny, "fixture must reach a ceiling-restricted deny (H2 non-vacuity)")
}

// =============================================================================
// D rows: zero-load and pre-load denies
// =============================================================================

// TestParity_D2_UnresolvablePermission is design row D2: a bogus action that
// resolves to no registered permission must deny with zero input loads,
// before step 0/1, in both paths.
func TestParity_D2_UnresolvablePermission(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "d2")
	tuples := []rawTuple{{f.agentRes, Action("bogus-action-not-registered")}}
	_, _, refStore, candStore := runParity(t, s, f.user, tuples)
	for _, method := range []string{"GetEffectiveGroups", "ListRoleBindingsForPrincipals", "GetRoleDefinitionsByIDs", "ListAccessConstraints"} {
		assert.Equal(t, 0, refStore.countOf(method), "reference: zero %s loads for an unresolvable permission", method)
		assert.Equal(t, 0, candStore.countOf(method), "candidate: zero %s loads for an unresolvable permission", method)
	}
}

// TestParity_D3_DeliveryGateZeroLoads is design row D3 (new at ba762d56): a
// deliver request is denied at step 0 before any input load, for every
// credential kind, because deliveryCredentialKinds is empty at ba762d56.
func TestParity_D3_DeliveryGateZeroLoads(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "d3")
	tuples := []rawTuple{{Resource{Type: "secret", ID: tid("d3-secret")}, ActionDeliver}}
	refDecisions, candDecisions, refStore, candStore := runParity(t, s, f.user, tuples)
	for _, d := range append(refDecisions, candDecisions...) {
		assert.False(t, d.Allowed)
		assert.Equal(t, "deliver permissions require a delivery credential", d.Reason)
	}
	for _, method := range []string{"GetEffectiveGroups", "ListRoleBindingsForPrincipals", "GetRoleDefinitionsByIDs", "ListAccessConstraints"} {
		assert.Equal(t, 0, refStore.countOf(method), "reference: zero %s loads for a deliver request", method)
		assert.Equal(t, 0, candStore.countOf(method), "candidate: zero %s loads for a deliver request", method)
	}
}

// =============================================================================
// E rows: deterministic and first-attempt-transient faults (P1 principal)
// =============================================================================

// TestParity_E1_GetEffectiveGroupsAlwaysFails is design row E1: deep-equal
// denies; the call count equals the number of decisions reaching step 2, in
// both runs (H3; every decision reaches step 2 here).
func TestParity_E1_GetEffectiveGroupsAlwaysFails(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "e1")
	tuples := agentResourceTuples(f.agentRes)

	refStore := newMemoTestStore(s)
	refStore.fault = alwaysFault("GetEffectiveGroups")
	candStore := newMemoTestStore(s)
	candStore.fault = alwaysFault("GetEffectiveGroups")

	refDecisions, _, _, _ := runParityWithStores(t, refStore, candStore, f.user, tuples)
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "tuple %d must deny", i)
		assert.Equal(t, "principal resolution error (fail-closed)", d.Reason)
	}
	assert.Equal(t, len(tuples), refStore.countOf("GetEffectiveGroups"), "reference: one call per decision, never memoized")
	assert.Equal(t, len(tuples), candStore.countOf("GetEffectiveGroups"), "candidate: an always-failing load is never stored, so every decision retries")
}

// TestParity_E2_ListRoleBindingsAlwaysFails is design row E2: same shape as
// E1, one level deeper (principal resolution succeeds; bindings never do).
func TestParity_E2_ListRoleBindingsAlwaysFails(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "e2")
	tuples := agentResourceTuples(f.agentRes)

	refStore := newMemoTestStore(s)
	refStore.fault = alwaysFault("ListRoleBindingsForPrincipals")
	candStore := newMemoTestStore(s)
	candStore.fault = alwaysFault("ListRoleBindingsForPrincipals")

	refDecisions, _, _, _ := runParityWithStores(t, refStore, candStore, f.user, tuples)
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "tuple %d must deny", i)
		assert.Equal(t, "binding resolution error (fail-closed)", d.Reason)
	}
	assert.Equal(t, len(tuples), refStore.countOf("ListRoleBindingsForPrincipals"))
	assert.Equal(t, len(tuples), candStore.countOf("ListRoleBindingsForPrincipals"))
}

// TestParity_E3_GetRoleDefinitionsAlwaysFails is design row E3: P1 has
// bindings (so this loader is actually reached), and it always fails.
func TestParity_E3_GetRoleDefinitionsAlwaysFails(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "e3")
	tuples := agentResourceTuples(f.agentRes)

	refStore := newMemoTestStore(s)
	refStore.fault = alwaysFault("GetRoleDefinitionsByIDs")
	candStore := newMemoTestStore(s)
	candStore.fault = alwaysFault("GetRoleDefinitionsByIDs")

	refDecisions, _, _, _ := runParityWithStores(t, refStore, candStore, f.user, tuples)
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "tuple %d must deny", i)
		assert.Equal(t, "role resolution error (fail-closed)", d.Reason)
	}
	assert.Equal(t, len(tuples), refStore.countOf("GetRoleDefinitionsByIDs"))
	assert.Equal(t, len(tuples), candStore.countOf("GetRoleDefinitionsByIDs"))
}

// c3Fixture builds 501 unrelated, inapplicable access-constraint rows so
// that loadAllAccessConstraints must page twice (limit 500): offset 0 then
// offset 500. The rows target a principal/scope that never matches the P1
// fixture's closure, so they never change a decision's outcome — only the
// page count.
func c3Fixture(t *testing.T, s store.Store, name string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 501; i++ {
		_, err := s.CreateAccessConstraint(ctx, &store.AccessConstraint{
			Name:               fmt.Sprintf("%s-constraint-%d", name, i),
			SubjectKind:        store.ConstraintSubjectAllPrincipals,
			ScopeType:          ScopeTypeSystem,
			MaximumPermissions: []string{"agent.read", "agent.list", "agent.create", "agent.delete", "agent.attach", "agent.lifecycle", "agent.update", "agent.port_access", "agent.set_message_mode", "agent.stop_all", "agent.message", "project.read"},
			CreatedBy:          "test",
		})
		require.NoError(t, err)
	}
}

// TestParity_C3_PagedConstraints is design row C3: P = 2 pages, loaded once
// under the memo; deep-equal.
func TestParity_C3_PagedConstraints(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "c3")
	c3Fixture(t, s, "c3")
	tuples := agentResourceTuples(f.agentRes)
	_, _, refStore, candStore := runParity(t, s, f.user, tuples)
	assert.Equal(t, 2*len(tuples), refStore.countOf("ListAccessConstraints"), "reference: 2 pages per decision, unmemoized")
	assert.Equal(t, 2, candStore.countOf("ListAccessConstraints"), "candidate: 2 pages, loaded once under the memo")
}

// offsetFaultStore wraps memoTestStore to fail ListAccessConstraints by its
// offset argument rather than by call number, because the generic
// call-number fault (H1's first form) cannot see method arguments.
type offsetFaultStore struct {
	*memoTestStore
	failOffset int
	fired      bool
	mu         sync.Mutex
}

func newOffsetFaultStore(s store.Store, failOffset int) *offsetFaultStore {
	return &offsetFaultStore{memoTestStore: newMemoTestStore(s), failOffset: failOffset}
}

func (o *offsetFaultStore) ListAccessConstraints(ctx context.Context, limit, offset int) ([]*store.AccessConstraint, error) {
	o.memoTestStore.mu.Lock()
	o.memoTestStore.counts["ListAccessConstraints"]++
	o.memoTestStore.mu.Unlock()
	if offset == o.failOffset {
		o.mu.Lock()
		o.fired = true
		o.mu.Unlock()
		return nil, fmt.Errorf("injected offset-%d fault: %w", offset, errInjected)
	}
	return o.memoTestStore.Store.ListAccessConstraints(ctx, limit, offset)
}

func (o *offsetFaultStore) hasFired() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.fired
}

// TestParity_E4_ListAccessConstraintsFailsByOffset is design row E4: P1 with
// the C3 fixture; ListAccessConstraints fails (i) whenever offset == 0, (ii)
// whenever offset == 500. Both variants must deep-equal deny-all.
func TestParity_E4_ListAccessConstraintsFailsByOffset(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset int
	}{
		{"offset0", 0},
		{"offset500", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := authzTestSetup(t)
			f := newP1Fixture(t, s, "e4-"+tc.name)
			c3Fixture(t, s, "e4-"+tc.name)
			tuples := agentResourceTuples(f.agentRes)

			refStore := newOffsetFaultStore(s, tc.offset)
			candStore := newOffsetFaultStore(s, tc.offset)

			ctx := context.Background()
			refAuthz, refEmit := newRecordingAuthz(refStore)
			var refDecisions []Decision
			for _, tp := range tuples {
				refDecisions = append(refDecisions, refAuthz.CheckAccess(ctx, f.user, tp.resource, tp.action))
			}

			candAuthz, candEmit := newRecordingAuthz(candStore)
			mctx := withAuthzInputMemo(ctx)
			var candDecisions []Decision
			for _, tp := range tuples {
				candDecisions = append(candDecisions, candAuthz.CheckAccess(mctx, f.user, tp.resource, tp.action))
			}

			for i := range refDecisions {
				assertDecisionsEqual(t, refDecisions[i], candDecisions[i], "tuple %d", i)
				assert.False(t, refDecisions[i].Allowed, "tuple %d must deny-all", i)
			}
			assertAuditSequenceEqual(t, refEmit.snapshot(), candEmit.snapshot())
			assert.True(t, refStore.hasFired(), "H2: the offset fault must actually fire in the reference")
			assert.True(t, candStore.hasFired(), "H2: the offset fault must actually fire in the candidate too (deny-all every decision, so no memoization saves it)")
		})
	}
}

// TestParity_E5_EachLoaderFailsOnFirstCallOnly is design row E5: each served
// loader fails on its call #1 only, in each run independently. Decision 1
// denies in both paths; the error is never memoized, so decision 2 retries
// and the rest match.
func TestParity_E5_EachLoaderFailsOnFirstCallOnly(t *testing.T) {
	for _, method := range []string{"GetEffectiveGroups", "ListRoleBindingsForPrincipals", "GetRoleDefinitionsByIDs", "ListAccessConstraints"} {
		t.Run(method, func(t *testing.T) {
			_, s := authzTestSetup(t)
			f := newP1Fixture(t, s, "e5-"+method)
			tuples := agentResourceTuples(f.agentRes)
			require.GreaterOrEqual(t, len(tuples), 2, "need at least 2 decisions to see the retry")

			refStore := newMemoTestStore(s)
			refStore.fault = callNFault(method, 1)
			candStore := newMemoTestStore(s)
			candStore.fault = callNFault(method, 1)

			refDecisions, _, refS, candS := runParityWithStores(t, refStore, candStore, f.user, tuples)
			assert.False(t, refDecisions[0].Allowed, "decision 1 must deny in both runs")
			assert.GreaterOrEqual(t, refS.countOf(method), 1, "H2: the fault must fire in the reference")
			assert.GreaterOrEqual(t, candS.countOf(method), 1, "H2: the fault must fire in the candidate too (it's decision 1, nothing is memoized yet)")
		})
	}
}

// TestParity_E5b_DocumentedDivergenceOnLaterTransient is design row E5b: for
// each served loader and k in {2, 5}, a fault on that method's call #k
// denies decision k in the reference but is never observed by the candidate
// (the memo already holds a success after decision 1). The candidate's
// every decision equals a no-fault baseline; the reference equals the
// baseline everywhere except decision k.
func TestParity_E5b_DocumentedDivergenceOnLaterTransient(t *testing.T) {
	for _, method := range []string{"GetEffectiveGroups", "ListRoleBindingsForPrincipals", "GetRoleDefinitionsByIDs", "ListAccessConstraints"} {
		for _, k := range []int{2, 5} {
			t.Run(fmt.Sprintf("%s/k=%d", method, k), func(t *testing.T) {
				_, s := authzTestSetup(t)
				f := newP1Fixture(t, s, fmt.Sprintf("e5b-%s-%d", method, k))
				tuples := agentResourceTuples(f.agentRes)
				require.GreaterOrEqual(t, len(tuples), k)
				ctx := context.Background()

				baselineStore := newMemoTestStore(s)
				baselineAuthz, _ := newRecordingAuthz(baselineStore)
				mctxBase := withAuthzInputMemo(ctx)
				var baseline []Decision
				for _, tp := range tuples {
					baseline = append(baseline, baselineAuthz.CheckAccess(mctxBase, f.user, tp.resource, tp.action))
				}

				refStore := newMemoTestStore(s)
				refStore.fault = callNFault(method, k)
				refAuthz, _ := newRecordingAuthz(refStore)
				var ref []Decision
				for _, tp := range tuples {
					ref = append(ref, refAuthz.CheckAccess(ctx, f.user, tp.resource, tp.action))
				}

				candStore := newMemoTestStore(s)
				candStore.fault = callNFault(method, k)
				candAuthz, _ := newRecordingAuthz(candStore)
				mctx := withAuthzInputMemo(ctx)
				var cand []Decision
				for _, tp := range tuples {
					cand = append(cand, candAuthz.CheckAccess(mctx, f.user, tp.resource, tp.action))
				}

				for i := range cand {
					assertDecisionsEqual(t, baseline[i], cand[i], "candidate tuple %d vs no-fault baseline", i)
				}
				assert.Equal(t, 1, candStore.countOf(method), "candidate: %s loaded once; call #%d never happens under the memo", method, k)

				for i := range ref {
					if i == k-1 {
						assert.False(t, ref[i].Allowed, "reference decision %d (call #%d) must deny", i+1, k)
						continue
					}
					assertDecisionsEqual(t, baseline[i], ref[i], "reference tuple %d vs no-fault baseline", i)
				}
				assert.GreaterOrEqual(t, refStore.countOf(method), k, "H2: call #%d of %s must actually occur in the reference", k, method)
			})
		}
	}
}

// =============================================================================
// E6/E6b: cancellation (H4)
// =============================================================================

// TestParity_E6_CancellationBothVariants is design row E6: P1 and A1 (raw
// loop), the ctx is cancelled between tuple j and j+1, under both H4 store
// variants. Every decision after cancellation must deep-equal the reference,
// including Reason, because the memo bypasses entirely on a done ctx and
// issues today's store call (rule 2, v3.2). AuthorizeReadBatch, called with
// an already-cancelled ctx, must return ctx.Err() in both runs.
func TestParity_E6_CancellationBothVariants(t *testing.T) {
	for _, honour := range []bool{true, false} {
		variant := "ignores-cancellation"
		if honour {
			variant = "honours-cancellation"
		}
		t.Run(variant, func(t *testing.T) {
			_, s := authzTestSetup(t)
			f := newP1Fixture(t, s, "e6-"+variant)
			a1 := newA1Fixture(t, s, "e6-a1-"+variant)

			makeStore := func() *memoTestStore {
				st := newMemoTestStore(s)
				st.ignoreCancel = !honour
				if honour {
					st.fault = cancelHonouringFault
				}
				return st
			}

			// P1 and A1 are run as two independent cancellation sequences,
			// each with its own cancellation point partway through (since
			// identity is fixed per CheckAccess call, one combined sequence
			// would not let both principals reach step 10's masked ctx).
			for _, ident := range []struct {
				name     string
				identity Identity
				tuples   []rawTuple
			}{
				{"P1", f.user, agentResourceTuples(f.agentRes)},
				{"A1", a1.agent, agentResourceTuples(a1.resource)},
			} {
				t.Run(ident.name, func(t *testing.T) {
					jj := len(ident.tuples) / 2
					require.Greater(t, jj, 0)

					refSt := makeStore()
					refAuthz, _ := newRecordingAuthz(refSt)
					refCtx, refCancel := context.WithCancel(context.Background())
					defer refCancel()
					var refDecisions []Decision
					for i, tp := range ident.tuples {
						if i == jj {
							refCancel()
						}
						refDecisions = append(refDecisions, refAuthz.CheckAccess(refCtx, ident.identity, tp.resource, tp.action))
					}

					candSt := makeStore()
					candAuthz, _ := newRecordingAuthz(candSt)
					candCtx, candCancel := context.WithCancel(withAuthzInputMemo(context.Background()))
					defer candCancel()
					var candDecisions []Decision
					for i, tp := range ident.tuples {
						if i == jj {
							candCancel()
						}
						candDecisions = append(candDecisions, candAuthz.CheckAccess(candCtx, ident.identity, tp.resource, tp.action))
					}

					for i := jj; i < len(ident.tuples); i++ {
						assertDecisionsEqual(t, refDecisions[i], candDecisions[i], "post-cancel tuple %d", i)
					}

					// AuthorizeReadBatch with an already-cancelled ctx
					// returns ctx.Err() in both runs.
					doneCtx, doneCancel := context.WithCancel(context.Background())
					doneCancel()
					_, refErr := refAuthz.AuthorizeReadBatch(doneCtx, ident.identity, []Resource{ident.tuples[0].resource})
					require.Error(t, refErr)
					assert.True(t, errors.Is(refErr, context.Canceled))
					mDoneCtx := withAuthzInputMemo(doneCtx)
					_, candErr := candAuthz.AuthorizeReadBatch(mDoneCtx, ident.identity, []Resource{ident.tuples[0].resource})
					require.Error(t, candErr)
					assert.True(t, errors.Is(candErr, context.Canceled))
				})
			}
		})
	}
}

// =============================================================================
// A rows: agent principal / delegation ceiling (all reuse a1Fixture)
// =============================================================================

// agentScopeTuples builds one tuple per ScopeActions["agent"], against a
// Resource shaped the way ComputeScopeCapabilities builds one: no ID, just
// the parent project.
func agentScopeTuples(projectID string) []rawTuple {
	scopeRes := Resource{Type: "agent", ParentType: "project", ParentID: projectID}
	out := make([]rawTuple, 0, len(ScopeActions["agent"]))
	for _, a := range ScopeActions["agent"] {
		out = append(out, rawTuple{scopeRes, a})
	}
	return out
}

// agentAllTuples is agentResourceTuples plus agentScopeTuples: all 8
// ResourceActions["agent"] and all 4 ScopeActions["agent"], as the design's
// "Agent resources" paragraph and A1-A8 require.
func agentAllTuples(res Resource, projectID string) []rawTuple {
	return append(agentResourceTuples(res), agentScopeTuples(projectID)...)
}

// agentGrantableTuples returns a small, deliberately-chosen tuple set that a
// full-lifecycle agent (AgentRoleFull) actually passes the AK1 kernel for,
// so every decision reaches step 10 and the ceiling result is what
// determines Allowed. This matters because agent.read/agent.list/
// agent.update/agent.port_access/agent.stop_all/agent.message/
// agent.grant_hub_mode have no AgentScopes entry in the permissions
// registry at all (agents cannot pass the kernel for them via JWT scope,
// exactly as TestDelegationCeiling_UserAgentChain's comment documents for
// agent.read), and this fixture's target is owned by the delegator, not the
// agent, so no owner/ancestor relationship grant covers them either.
// project.read (AgentScopes: ["project:read"]) is the read-only permission
// AgentRoleFull actually holds; agent.delete/attach/lifecycle (AgentScopes:
// ["project:agent:lifecycle"]) are the non-read-only ones.
func agentGrantableTuples(f *a1Fixture) []rawTuple {
	projectRes := Resource{Type: "project", ID: f.projectID, OwnerID: f.delegatorID}
	return []rawTuple{
		{projectRes, ActionRead},
		{f.resource, ActionDelete},
		{f.resource, ActionAttach},
		{f.resource, ActionLifecycle},
	}
}

// TestParity_A1_AgentOwnProjectSingleLevelEdge is design row A1: deep-equal;
// GetDelegationEdgesForDelegate loaded once per phase (the edges memo);
// the delegator's own loads stay per decision (the ceiling ctx is masked).
func TestParity_A1_AgentOwnProjectSingleLevelEdge(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "a1")
	tuples := agentAllTuples(f.resource, f.projectID)

	refDecisions, _, refStore, candStore := runParity(t, s, f.agent, tuples)
	var sawAllow bool
	for _, d := range refDecisions {
		if d.Allowed {
			sawAllow = true
		}
	}
	assert.True(t, sawAllow, "H2: the fixture must reach at least one allowed decision so step 10 actually runs")
	assert.Equal(t, 1, candStore.countOf("GetDelegationEdgesForDelegate"), "edges loaded once per phase under the memo")
	assert.GreaterOrEqual(t, refStore.countOf("GetDelegationEdgesForDelegate"), 1, "reference issues at least one edge load")
	// The delegator's own inputs are never memoized (masked ctx): GetUser
	// is on the delegator path only and is not memo-served, so its count
	// must be identical in both runs.
	assert.Equal(t, refStore.countOf("GetUser"), candStore.countOf("GetUser"), "delegator GetUser calls stay per decision in both runs")
}

// TestParity_A2_AgentNoScopes is design row A2: an agent with no JWT scopes
// hits the 7b deny-all restriction for everything; deep-equal.
func TestParity_A2_AgentNoScopes(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "a2")
	noScopeAgent := &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: f.agentID},
		ProjectID: f.projectID,
	}}
	tuples := agentAllTuples(f.resource, f.projectID)
	refDecisions, _, _, _ := runParity(t, s, noScopeAgent, tuples)
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "tuple %d must deny under 7b deny-all", i)
	}
}

// TestParity_A3_AgentCrossProject is design row A3: the target resource is
// in a different project than the agent's own; the ceiling scope switches
// to the resource's project (authz_delegation_ceiling.go:147-150), where the
// agent has no edge. Deep-equal.
func TestParity_A3_AgentCrossProject(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "a3")
	crossRes := Resource{Type: "agent", ID: tid("a3-cross-target"), ParentType: "project", ParentID: f.otherProjID, OwnerID: f.delegatorID}
	tuples := agentAllTuples(crossRes, f.otherProjID)
	refDecisions, _, _, _ := runParity(t, s, f.agent, tuples)
	// H2: at least the JWT-scope-eligible actions must reach the kernel with
	// a cross-project denial (rather than short-circuiting earlier), i.e.
	// some decision must actually deny for a reason distinct from 7b.
	var sawNonScopeDeny bool
	for _, d := range refDecisions {
		if !d.Allowed && d.Reason != "" {
			sawNonScopeDeny = true
		}
	}
	assert.True(t, sawNonScopeDeny, "H2: cross-project fixture must reach a real denial")
}

// TestParity_A4_DelegatorLostPermission is design row A4: the delegator no
// longer holds any permission (no role binding at all), so the ceiling
// denies with DenyCauseCeilingDelegatorLacksPermission. Deep-equal.
func TestParity_A4_DelegatorLostPermission(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	projectID := tid("a4-project")
	delegatorID := tid("a4-delegator")
	agentID := tid("a4-agent")
	targetID := tid("a4-target")

	createDCProject(t, s, projectID, "a4-proj")
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: delegatorID, Email: "a4-delegator@test.com", DisplayName: "a4", Role: "member", Status: "active"}))
	createDCAgent(t, s, agentID, projectID, delegatorID, AgentRoleFull)
	createDCAgent(t, s, targetID, projectID, delegatorID, AgentRoleFull)
	createDCEdge(t, s, store.DelegationPrincipalUser, delegatorID, store.DelegationPrincipalAgent, agentID, store.RoleScopeProject, projectID, string(AgentRoleFull))

	agent := dcAgentIdentity(agentID, projectID, AgentRoleFull)
	res := Resource{Type: "agent", ID: targetID, ParentType: "project", ParentID: projectID, OwnerID: delegatorID}
	tuples := agentAllTuples(res, projectID)

	refDecisions, _, _, _ := runParity(t, s, agent, tuples)
	var sawCeilingDeny bool
	for _, d := range refDecisions {
		if d.DenyCause == DenyCauseCeilingDelegatorLacksPermission {
			sawCeilingDeny = true
		}
	}
	assert.True(t, sawCeilingDeny, "H2: fixture must reach DenyCauseCeilingDelegatorLacksPermission")
}

// TestParity_A6_DelegatorRoleDefinitionFails is design row A6: the
// delegator's GetRoleDefinition (not memo-served) fails (a) always, (b) on
// call #1 only. Deep-equal for every decision in both variants; no
// read-only skip where the reference has none, because the delegator path
// is unmemoized regardless.
func TestParity_A6_DelegatorRoleDefinitionFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int // 0 = always
	}{
		{"always", 0},
		{"call1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := authzTestSetup(t)
			f := newA1Fixture(t, s, "a6-"+tc.name)
			tuples := agentAllTuples(f.resource, f.projectID)

			var fault func(string, int, context.Context) error
			if tc.n == 0 {
				fault = alwaysFault("GetRoleDefinition")
			} else {
				fault = callNFault("GetRoleDefinition", tc.n)
			}
			refStore := newMemoTestStore(s)
			refStore.fault = fault
			candStore := newMemoTestStore(s)
			candStore.fault = fault

			runParityWithStores(t, refStore, candStore, f.agent, tuples)
			assert.GreaterOrEqual(t, refStore.countOf("GetRoleDefinition"), 1, "H2: the fault must actually be reachable in the reference")
		})
	}
}

// TestParity_A7_ExactParityDelegatorConstraintFailure is design row A7
// (R3-B1's fix, re-verified round 4/5): the delegator is the A1 fixture's
// non-admin, in-scope-bound user. ListAccessConstraints fails iff
// getDelegationCeilingCache(ctx) != nil — true only inside step 10 — which
// selects exactly the delegator's calls in both runs, regardless of how the
// memo shifts decide's own call indices (R3-B1). Exact parity is required:
// every decision denies with DenyCauseCeilingDelegatorLacksPermission.
func TestParity_A7_ExactParityDelegatorConstraintFailure(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "a7")
	tuples := agentAllTuples(f.resource, f.projectID)

	predicateFault := func(method string, _ int, ctx context.Context) error {
		if method != "ListAccessConstraints" {
			return nil
		}
		if getDelegationCeilingCache(ctx) != nil {
			return fmt.Errorf("injected delegator-side constraint fault: %w", errInjected)
		}
		return nil
	}
	refStore := newMemoTestStore(s)
	refStore.fault = predicateFault
	candStore := newMemoTestStore(s)
	candStore.fault = predicateFault

	refDecisions, _, refStoreOut, candStoreOut := runParityWithStores(t, refStore, candStore, f.agent, tuples)
	var sawAllowInReference bool
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "tuple %d must deny (delegator constraint load fails, deny-all filters permissions to empty)", i)
		if d.DenyCause == DenyCauseCeilingDelegatorLacksPermission {
			sawAllowInReference = true
		}
	}
	assert.True(t, sawAllowInReference, "H2: at least one tuple must reach DenyCauseCeilingDelegatorLacksPermission, proving the kernel/relationship stage allowed before the ceiling denied")
	// decide's own 7c constraint calls (predicate false): 1 in the
	// candidate (memoized) vs one per decision in the reference.
	require.NotZero(t, refStoreOut.countOf("ListAccessConstraints"))
	require.NotZero(t, candStoreOut.countOf("ListAccessConstraints"))
}

// TestParity_A7Prime_FirstAttemptTransientPlusDeterministicDelegatorFault is
// design row A7': same fixture as A7. decide's own (predicate-false) 7c call
// fails on its 1st call only (decision 1, deny-all, nothing memoized, the
// ceiling is not reached). Every delegator (predicate-true) call fails
// always. Decision 2+ : 7c succeeds and is memoized in the candidate, then a
// ceiling deny with DelegatorLacksPermission in both runs.
func TestParity_A7Prime_FirstAttemptTransientPlusDeterministicFault(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "a7prime")
	tuples := agentGrantableTuples(f)
	require.GreaterOrEqual(t, len(tuples), 2)

	predicateFalseCallCount := 0
	var mu sync.Mutex
	fault := func(method string, _ int, ctx context.Context) error {
		if method != "ListAccessConstraints" {
			return nil
		}
		if getDelegationCeilingCache(ctx) != nil {
			return fmt.Errorf("injected delegator-side constraint fault: %w", errInjected)
		}
		mu.Lock()
		predicateFalseCallCount++
		n := predicateFalseCallCount
		mu.Unlock()
		if n == 1 {
			return fmt.Errorf("injected decide-side first-attempt fault: %w", errInjected)
		}
		return nil
	}
	refStore := newMemoTestStore(s)
	refStore.fault = fault
	// The candidate needs its own independent predicate-false counter.
	predicateFalseCallCount2 := 0
	var mu2 sync.Mutex
	candFault := func(method string, _ int, ctx context.Context) error {
		if method != "ListAccessConstraints" {
			return nil
		}
		if getDelegationCeilingCache(ctx) != nil {
			return fmt.Errorf("injected delegator-side constraint fault: %w", errInjected)
		}
		mu2.Lock()
		predicateFalseCallCount2++
		n := predicateFalseCallCount2
		mu2.Unlock()
		if n == 1 {
			return fmt.Errorf("injected decide-side first-attempt fault: %w", errInjected)
		}
		return nil
	}
	candStore := newMemoTestStore(s)
	candStore.fault = candFault

	refDecisions, candDecisions, _, _ := runParityWithStores(t, refStore, candStore, f.agent, tuples)
	assert.False(t, refDecisions[0].Allowed, "decision 1 must deny at 7c (deny-all)")
	assertDecisionsEqual(t, refDecisions[0], candDecisions[0], "decision 1")
	for i := 1; i < len(refDecisions); i++ {
		assert.False(t, refDecisions[i].Allowed, "decision %d must be a ceiling deny", i+1)
		assert.Equal(t, DenyCauseCeilingDelegatorLacksPermission, refDecisions[i].DenyCause, "decision %d", i+1)
	}
}

// TestParity_A8_DelegationEdgesFail is design row A8: A1's single-delegate
// fixture, so each decision that reaches step 10 makes at most one edge
// lookup. (a) always fails: deep-equal by construction (every decision hits
// the same fault). (b) call #1 only: deep-equal (both runs fail
// independently on their own first attempt, exactly like E5). (c) call #2
// only: the documented 6.2 divergence — it fires on the reference's second
// step-10 decision, never on the candidate's (memo hit). The candidate's
// corresponding decision deep-equals the same decision in a no-fault run.
func TestParity_A8_DelegationEdgesFail(t *testing.T) {
	t.Run("always", func(t *testing.T) {
		_, s := authzTestSetup(t)
		f := newA1Fixture(t, s, "a8-always")
		tuples := agentGrantableTuples(f)
		refStore := newMemoTestStore(s)
		refStore.fault = alwaysFault("GetDelegationEdgesForDelegate")
		candStore := newMemoTestStore(s)
		candStore.fault = alwaysFault("GetDelegationEdgesForDelegate")
		refDecisions, _, _, _ := runParityWithStores(t, refStore, candStore, f.agent, tuples)
		var sawReadSkip, sawWriteDeny bool
		for i, d := range refDecisions {
			action := tuples[i].action
			if isReadOnlyOperation(action) && d.Allowed {
				sawReadSkip = true
			}
			if !isReadOnlyOperation(action) && !d.Allowed && d.DenyCause == DenyCauseCeilingError {
				sawWriteDeny = true
			}
		}
		assert.True(t, sawReadSkip, "H2: a read action must hit the read-only edge-error skip")
		assert.True(t, sawWriteDeny, "H2: a write action must fail closed on the edge error")
	})

	t.Run("call1", func(t *testing.T) {
		_, s := authzTestSetup(t)
		f := newA1Fixture(t, s, "a8-call1")
		tuples := agentGrantableTuples(f)
		refStore := newMemoTestStore(s)
		refStore.fault = callNFault("GetDelegationEdgesForDelegate", 1)
		candStore := newMemoTestStore(s)
		candStore.fault = callNFault("GetDelegationEdgesForDelegate", 1)
		runParityWithStores(t, refStore, candStore, f.agent, tuples)
		assert.GreaterOrEqual(t, refStore.countOf("GetDelegationEdgesForDelegate"), 1)
	})

	t.Run("call2_divergence", func(t *testing.T) {
		_, s := authzTestSetup(t)
		f := newA1Fixture(t, s, "a8-call2")
		tuples := agentGrantableTuples(f)
		ctx := context.Background()

		// No-fault baseline, on a PLAIN ctx (unmemoized, like the reference
		// path), identifies which tuple indices actually reach step 10: the
		// kernel grants delete/attach/lifecycle from the AGENT's own JWT
		// scope regardless of what the delegator (a project-admin, which
		// lacks delete/attach — seed.go's projectAdminPermissionIDs) holds,
		// so step 10 (and its edge lookup) runs for every one of them; only
		// the ceiling's FINAL verdict differs per delegator permission. So
		// "reached step 10" must be measured by an actual increase in the
		// edge-lookup call count, never inferred from final Allowed.
		baselineStore := newMemoTestStore(s)
		baselineAuthz, _ := newRecordingAuthz(baselineStore)
		var baseline []Decision
		var step10Indices []int
		prevEdgeCount := 0
		for i, tp := range tuples {
			d := baselineAuthz.CheckAccess(ctx, f.agent, tp.resource, tp.action)
			baseline = append(baseline, d)
			if n := baselineStore.countOf("GetDelegationEdgesForDelegate"); n > prevEdgeCount {
				step10Indices = append(step10Indices, i)
				prevEdgeCount = n
			}
		}
		require.GreaterOrEqual(t, len(step10Indices), 2, "fixture must reach step 10 on at least 2 decisions")
		k := step10Indices[1] // the SECOND decision to reach step 10 (0-indexed)

		refStore := newMemoTestStore(s)
		reached := 0
		refStore.fault = func(method string, _ int, _ context.Context) error {
			if method != "GetDelegationEdgesForDelegate" {
				return nil
			}
			reached++
			if reached == 2 {
				return fmt.Errorf("injected edge fault on the 2nd step-10 decision: %w", errInjected)
			}
			return nil
		}
		refAuthz, _ := newRecordingAuthz(refStore)
		var ref []Decision
		for _, tp := range tuples {
			ref = append(ref, refAuthz.CheckAccess(ctx, f.agent, tp.resource, tp.action))
		}
		assert.Equal(t, isReadOnlyOperation(tuples[k].action), ref[k].Allowed, "H2 sanity at index %d: read hits the skip (allow), write fails closed (deny)", k)

		candStore := newMemoTestStore(s)
		reachedCand := 0
		candStore.fault = func(method string, _ int, _ context.Context) error {
			if method != "GetDelegationEdgesForDelegate" {
				return nil
			}
			reachedCand++
			if reachedCand == 2 {
				return fmt.Errorf("injected edge fault on the 2nd step-10 decision: %w", errInjected)
			}
			return nil
		}
		candAuthz, _ := newRecordingAuthz(candStore)
		mctx := withAuthzInputMemo(ctx)
		var cand []Decision
		for _, tp := range tuples {
			cand = append(cand, candAuthz.CheckAccess(mctx, f.agent, tp.resource, tp.action))
		}

		// The candidate never observes the fault: edges are memoized after
		// the first step-10 decision, so its second step-10 decision is a
		// memo hit, not a store call.
		assert.Equal(t, 1, candStore.countOf("GetDelegationEdgesForDelegate"), "H2: candidate never reaches call #2 (memo hit)")
		assertDecisionsEqual(t, baseline[k], cand[k], "candidate decision %d vs no-fault baseline", k)
	})
}

// =============================================================================
// E6b(a): REQUIRED non-waivable security gate (design section 14)
// =============================================================================

// TestParity_E6b_DoneCtxEdgesBypass_RequiredGate is design row E6b (a), a
// required unit-level test backing the section 14 acceptance gate: "no new
// caller or error source reaches authz_delegation_ceiling.go:188-192 or
// :314-318; a done ctx bypasses the memo (4.1 rule 2)."
//
// It calls checkDelegationCeiling directly (package-internal), exactly as
// decide's step 10 does: first on a live memo ctx to populate the edges
// slot, then on a pre-cancelled child of that SAME memo ctx (run 1), and
// compares the outcome to the identical call with no memo at all on an
// equally cancelled ctx (run 2). A fresh per-decision delegation-ceiling
// cache on every call ensures only the memo could have served the edges.
//
// The load-bearing assertion is non-vacuity, not the outcome tuple: run 1
// must still issue GetDelegationEdgesForDelegate on the done ctx. Per
// R5-Nit1, a broken memo that incorrectly served edges from a done ctx would
// still produce an identical (allowed, reason, err) tuple to run 2 for
// variant (ii) and for variant (i) reads (the store's own cancellation
// error resurfaces through the delegator's unmemoized getEffectivePermissions
// and reaches the same read-only skip with the same Reason) — only the call
// count catches that regression.
func TestParity_E6b_DoneCtxEdgesBypass_RequiredGate(t *testing.T) {
	for _, variant := range []string{"honours", "ignores"} {
		for _, ac := range []struct {
			name   string
			action Action
		}{{"read", ActionRead}, {"write", ActionUpdate}} {
			t.Run(variant+"/"+ac.name, func(t *testing.T) {
				_, s := authzTestSetup(t)
				f := newA1Fixture(t, s, "e6b-"+variant+"-"+ac.name)

				req := AuthzRequest{
					Principal:  PrincipalContext{Identity: f.agent},
					Credential: credentialContextForIdentity(f.agent),
					Resource:   f.resource,
					Action:     ac.action,
				}

				makeStore := func() *memoTestStore {
					st := newMemoTestStore(s)
					if variant == "honours" {
						st.fault = cancelHonouringFault
					} else {
						st.ignoreCancel = true
					}
					return st
				}

				// --- Run 1: memo populated on a live ctx, then a
				// pre-cancelled child of the SAME memo ctx. ---
				run1Store := makeStore()
				authz1 := NewAuthzService(run1Store, slog.Default())
				bg := context.Background()
				mctx := withAuthzInputMemo(bg)

				var primeCause DenyCause
				liveCtx := maskAuthzInputs(contextWithDelegationCeilingCache(mctx))
				_, _, primeErr := authz1.checkDelegationCeiling(liveCtx, req, f.agentID, nil, &primeCause)
				require.NoError(t, primeErr, "priming call on a live ctx must succeed")
				require.Equal(t, 1, run1Store.countOf("GetDelegationEdgesForDelegate"), "priming call must populate the edges memo")

				cctx, cancel := context.WithCancel(mctx)
				cancel()
				var cause1 DenyCause
				doneCtx := maskAuthzInputs(contextWithDelegationCeilingCache(cctx))
				allowed1, reason1, err1 := authz1.checkDelegationCeiling(doneCtx, req, f.agentID, nil, &cause1)

				// Non-vacuity (R5-Nit1, load-bearing): the second call must
				// still reach the store on the done ctx.
				require.Equal(t, 2, run1Store.countOf("GetDelegationEdgesForDelegate"),
					"the done-ctx call must bypass the memo and reach the store, not be served from it")

				// --- Run 2: the same call, no memo at all, on an
				// independently cancelled ctx with a fresh cache. ---
				run2Store := makeStore()
				authz2 := NewAuthzService(run2Store, slog.Default())
				cctx2, cancel2 := context.WithCancel(context.Background())
				cancel2()
				var cause2 DenyCause
				allowed2, reason2, err2 := authz2.checkDelegationCeiling(maskAuthzInputs(contextWithDelegationCeilingCache(cctx2)), req, f.agentID, nil, &cause2)

				assert.Equal(t, allowed1, allowed2, "Allowed must match")
				assert.Equal(t, reason1, reason2, "Reason must match")
				assert.Equal(t, cause1, cause2, "DenyCause must match")
				switch {
				case err1 == nil && err2 == nil:
					// both nil, fine
				case err1 != nil && err2 != nil:
					assert.Equal(t, err1.Error(), err2.Error(), "error text must match")
					if variant == "honours" {
						assert.True(t, errors.Is(err1, context.Canceled), "run 1 error must be context.Canceled")
						assert.True(t, errors.Is(err2, context.Canceled), "run 2 error must be context.Canceled")
					}
				default:
					t.Fatalf("error-ness mismatch: err1=%v err2=%v", err1, err2)
				}

				if variant == "honours" {
					if ac.name == "read" {
						assert.True(t, allowed1, "read must hit the read-only store-error skip")
						assert.Equal(t, "delegation ceiling check skipped (store error, read-only)", reason1)
						assert.NoError(t, err1)
					} else {
						assert.False(t, allowed1, "write must fail closed on the store error")
						assert.Contains(t, reason1, "delegation ceiling check failed (fail-closed):")
						assert.Error(t, err1)
					}
				}
			})
		}
	}
}

// =============================================================================
// X7: REQUIRED non-waivable security gate (design section 14)
// =============================================================================

// x7Fixture builds the three delegator-visibility branches X7's fixture
// requires (R3-N3, R4-N1):
//  1. a1: the A7/A1 delegator — a non-admin user with an in-scope active
//     project binding, so getEffectivePermissions actually loads constraints;
//  2. chainAgent: a depth-1 agent-to-agent chain (chainAgent -> midAgent ->
//     the same user), covering checkAgentHoldsPermission (GetAgent);
//  3. noEdgeAgent: an agent with no delegation edge at all, covering
//     backfillCompleted (GetHubSetting). authzTestSetup's testServer removes
//     the backfill marker, so this is the pre-backfill temporary-allow path.
type x7Fixture struct {
	a1         *a1Fixture
	chainAgent AgentIdentity
	chainRes   Resource
	noEdgeAg   AgentIdentity
	noEdgeRes  Resource
}

func newX7Fixture(t *testing.T, s store.Store, name string) *x7Fixture {
	t.Helper()
	a1 := newA1Fixture(t, s, name+"-a1")

	midAgentID := tid(name + "-mid-agent")
	chainAgentID := tid(name + "-chain-agent")
	chainTargetID := tid(name + "-chain-target")
	createDCAgent(t, s, midAgentID, a1.projectID, a1.delegatorID, AgentRoleFull)
	createDCAgent(t, s, chainAgentID, a1.projectID, a1.delegatorID, AgentRoleFull)
	createDCAgent(t, s, chainTargetID, a1.projectID, a1.delegatorID, AgentRoleFull)
	createDCEdge(t, s, store.DelegationPrincipalUser, a1.delegatorID, store.DelegationPrincipalAgent, midAgentID,
		store.RoleScopeProject, a1.projectID, string(AgentRoleFull))
	createDCEdge(t, s, store.DelegationPrincipalAgent, midAgentID, store.DelegationPrincipalAgent, chainAgentID,
		store.RoleScopeProject, a1.projectID, string(AgentRoleFull))

	noEdgeAgentID := tid(name + "-no-edge-agent")
	noEdgeTargetID := tid(name + "-no-edge-target")
	createDCAgent(t, s, noEdgeAgentID, a1.projectID, a1.delegatorID, AgentRoleFull)
	createDCAgent(t, s, noEdgeTargetID, a1.projectID, a1.delegatorID, AgentRoleFull)

	return &x7Fixture{
		a1:         a1,
		chainAgent: dcAgentIdentity(chainAgentID, a1.projectID, AgentRoleFull),
		chainRes:   Resource{Type: "agent", ID: chainTargetID, ParentType: "project", ParentID: a1.projectID, OwnerID: a1.delegatorID},
		noEdgeAg:   dcAgentIdentity(noEdgeAgentID, a1.projectID, AgentRoleFull),
		noEdgeRes:  Resource{Type: "agent", ID: noEdgeTargetID, ParentType: "project", ParentID: a1.projectID, OwnerID: a1.delegatorID},
	}
}

// x7DecisionTuple pairs a tuple with the identity that must evaluate it, so
// the three fixture branches can be walked in one ordered sequence.
type x7DecisionTuple struct {
	identity Identity
	resource Resource
	action   Action
}

// tuples returns one read tuple and one write tuple per delegate. The read
// tuple targets the shared project (project.read, AgentScopes:
// ["project:read"]) rather than the agent resource, because agent.read has
// no AgentScopes entry at all — an agent can never pass the AK1 kernel for
// it via JWT scope, and none of these targets is owned by or ancestor-linked
// to its agent, so no relationship grant covers it either (see
// agentGrantableTuples). The write tuple uses agent.delete (AgentScopes:
// ["project:agent:lifecycle"]), which AgentRoleFull does hold.
func (f *x7Fixture) tuples() []x7DecisionTuple {
	projectRes := Resource{Type: "project", ID: f.a1.projectID, OwnerID: f.a1.delegatorID}
	var out []x7DecisionTuple
	for _, d := range []struct {
		identity Identity
		writeRes Resource
	}{
		{f.a1.agent, f.a1.resource},
		{f.chainAgent, f.chainRes},
		{f.noEdgeAg, f.noEdgeRes},
	} {
		out = append(out, x7DecisionTuple{d.identity, projectRes, ActionRead})
		out = append(out, x7DecisionTuple{d.identity, d.writeRes, ActionDelete})
	}
	return out
}

// x7NonEdgeServedMethods are the delegator-side methods X7's count clause
// covers: every predicate-true (ceiling-active) call except the edges load,
// which is checked separately by (c).
var x7NonEdgeServedMethods = map[string]bool{
	"GetEffectiveGroups":            true,
	"GetEffectiveGroupsForAgent":    true,
	"ListRoleBindingsForPrincipals": true,
	"GetRoleDefinition":             true,
	"ListAccessConstraints":         true,
	"GetUser":                       true,
	"GetAgent":                      true,
	"GetHubSetting":                 true,
}

// perDecisionCeilingCounts runs identity/resource/action tuples one at a
// time through CheckAccess on a fresh recording AuthzService, and returns,
// for each decision, the count of predicate-true (ceiling-active) calls to
// each x7NonEdgeServedMethods method, plus the total GetDelegationEdgesForDelegate
// count for the whole phase.
func perDecisionCeilingCounts(t *testing.T, st *memoTestStore, ctx context.Context, tuples []x7DecisionTuple) (perDecision []map[string]int, edgesTotal int, allNilMemoUnderCeiling bool) {
	t.Helper()
	st.recordCalls = true
	authz, _ := newRecordingAuthz(st)
	prevLen := 0
	allNilMemoUnderCeiling = true
	for _, tp := range tuples {
		authz.CheckAccess(ctx, tp.identity, tp.resource, tp.action)
		all := st.recordedCalls()
		decisionCalls := all[prevLen:]
		prevLen = len(all)

		counts := map[string]int{}
		for _, rec := range decisionCalls {
			if !rec.ceilingActive {
				continue
			}
			// Only the INPUT memo (inputsKey) is masked under the ceiling
			// (design 4.1(b)); the edges key is deliberately left visible to
			// every ceiling call (maskAuthzInputs only masks inputsKey), so
			// hasEdgesMemo == true is expected everywhere under the ceiling
			// and is not itself evidence of a leak. Only GetDelegationEdgesForDelegate
			// actually reads it; every other method never consults it even
			// though it is technically visible in the ctx.
			if rec.hasInputMemo {
				allNilMemoUnderCeiling = false
			}
			if rec.method == "GetDelegationEdgesForDelegate" {
				continue
			}
			if x7NonEdgeServedMethods[rec.method] {
				counts[rec.method]++
			}
		}
		perDecision = append(perDecision, counts)
	}
	edgesTotal = st.countOf("GetDelegationEdgesForDelegate")
	return
}

// TestParity_X7_CeilingSeesNoMemoExceptEdges is design row X7, a required
// non-waivable security gate (section 14): the delegation ceiling's own
// loads (excluding edges) must never see the principal/constraint memo, and
// their per-decision call counts must be identical to the reference's — the
// proof that loadAllAccessConstraints changing does not leak into the
// delegator path, because the ceiling ctx is masked.
func TestParity_X7_CeilingSeesNoMemoExceptEdges(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newX7Fixture(t, s, "x7")
	tuples := f.tuples()
	ctx := context.Background()

	refStore := newMemoTestStore(s)
	refCounts, _, refNilOK := perDecisionCeilingCounts(t, refStore, ctx, tuples)

	candStore := newMemoTestStore(s)
	candCounts, candEdgesTotal, candNilOK := perDecisionCeilingCounts(t, candStore, withAuthzInputMemo(ctx), tuples)

	assert.True(t, refNilOK, "sanity: the reference (no memo present at all) trivially sees no memo under the ceiling")
	assert.True(t, candNilOK, "(b) every ceiling call except edges must observe a nil input memo, and the edges call must not observe the input memo either")

	require.Equal(t, len(refCounts), len(candCounts))
	for i := range refCounts {
		assert.Equal(t, refCounts[i], candCounts[i], "(a) decision %d: predicate-true call counts (excluding edges) must match the reference", i)
	}

	// (c) GetDelegationEdgesForDelegate issued once per delegate per phase:
	// four distinct delegates are ever looked up — a1's agent, chainAgent,
	// the depth-1 midAgent the chain recurses into, and noEdgeAgent — each
	// evaluated across 2 actions, so a working memo issues exactly 4 calls
	// total, not 8. The no-edge agent's empty result is still memoized
	// (presence tracked separately from value, design 4.1 rule 6), so it
	// contributes exactly one lookup, not one per action.
	assert.Equal(t, 4, candEdgesTotal, "edges loaded once per delegate per phase: 4 distinct delegates (a1 agent, chain agent, mid agent, no-edge agent)")

	// H2: each fixture branch must actually be reached in the reference —
	// GetUser (a1's direct user delegator), GetAgent (the agent-to-agent
	// chain) and GetHubSetting (the no-edge, pre-backfill path) must all
	// appear somewhere in the reference's per-decision counts.
	var sawGetUser, sawGetAgent, sawGetHubSetting bool
	for _, c := range refCounts {
		if c["GetUser"] > 0 {
			sawGetUser = true
		}
		if c["GetAgent"] > 0 {
			sawGetAgent = true
		}
		if c["GetHubSetting"] > 0 {
			sawGetHubSetting = true
		}
	}
	assert.True(t, sawGetUser, "H2: the direct user-delegator branch must be reached")
	assert.True(t, sawGetAgent, "H2: the agent-to-agent chain branch must be reached")
	assert.True(t, sawGetHubSetting, "H2: the no-edge/backfill branch must be reached")
}

// =============================================================================
// X rows: memo mechanics (mask stickiness, isolation, immutability, freshness)
// =============================================================================

// TestParity_X6_MaskStickinessUnit is design row X6(1): a nested
// withAuthzInputMemo under a mask stays masked; both accessors return nil.
func TestParity_X6_MaskStickinessUnit(t *testing.T) {
	bg := context.Background()
	ctx := maskAllAuthzMemo(withAuthzInputMemo(bg))
	ctx = withAuthzInputMemo(ctx) // must be a no-op: the mask is sticky
	assert.Nil(t, authzInputMemoFromContext(ctx), "inputsKey must stay masked")
	assert.Nil(t, delegationEdgesMemoFromContext(ctx), "edgesKey must stay masked")
}

// TestParity_X6_CanMintSelectorSeesNoOuterMemo is design row X6(2):
// CanMintSelector results deep-equal with and without an outer memo, and
// every store call made inside it observes a nil input memo and a nil edges
// memo, with at least one call actually recorded (H2 non-vacuity).
func TestParity_X6_CanMintSelectorSeesNoOuterMemo(t *testing.T) {
	_, s := authzTestSetup(t)
	userID := tid("x6-user")
	projectID := tid("x6-proj")
	createDelegateTestProject(t, s, projectID, "x6-proj", "test")
	createTestUserWithProjectRole(t, s, userID, "x6@test.com", projectID, store.ProjectRoleMember)
	selectors := []string{"project:read", "agent:read"}
	boundary := TokenBoundary{Kind: BoundaryKindProject, ProjectID: projectID}

	plainStore := newMemoTestStore(s)
	plainAuthz := NewAuthzService(plainStore, slog.Default())
	plainResults, plainErr := plainAuthz.CanMintSelector(context.Background(), activeUserPrincipal(userID), boundary, selectors)
	require.NoError(t, plainErr)

	outerStore := newMemoTestStore(s)
	outerStore.recordCalls = true
	outerAuthz := NewAuthzService(outerStore, slog.Default())
	outerCtx := withAuthzInputMemo(context.Background())
	outerResults, outerErr := outerAuthz.CanMintSelector(outerCtx, activeUserPrincipal(userID), boundary, selectors)
	require.NoError(t, outerErr)

	assert.Equal(t, plainResults, outerResults, "CanMintSelector results must deep-equal with and without an outer memo")

	recorded := outerStore.recordedCalls()
	require.NotEmpty(t, recorded, "H2: at least one store call must be recorded inside CanMintSelector")
	for _, rec := range recorded {
		assert.False(t, rec.hasInputMemo, "call %s must not observe the outer input memo", rec.method)
		assert.False(t, rec.hasEdgesMemo, "call %s must not observe the outer edges memo", rec.method)
	}
}

// TestParity_X1_TwoPrincipalsInterleaved is design row X1: two principals
// interleaved in one memo ctx must not leak entries between them.
func TestParity_X1_TwoPrincipalsInterleaved(t *testing.T) {
	_, s := authzTestSetup(t)
	p1 := newP1Fixture(t, s, "x1-p1")
	p2 := newP1Fixture(t, s, "x1-p2")
	ctx := withAuthzInputMemo(context.Background())

	authz, _ := newRecordingAuthz(newMemoTestStore(s))
	// Interleave: p1 action, p2 action, p1 action, p2 action...
	var p1Decisions, p2Decisions []Decision
	for _, a := range ResourceActions["agent"] {
		p1Decisions = append(p1Decisions, authz.CheckAccess(ctx, p1.user, p1.agentRes, a))
		p2Decisions = append(p2Decisions, authz.CheckAccess(ctx, p2.user, p2.agentRes, a))
	}

	// No leakage: re-run each principal alone on a fresh memo and compare.
	freshCtx1 := withAuthzInputMemo(context.Background())
	authzFresh1, _ := newRecordingAuthz(newMemoTestStore(s))
	for i, a := range ResourceActions["agent"] {
		d := authzFresh1.CheckAccess(freshCtx1, p1.user, p1.agentRes, a)
		assertDecisionsEqual(t, d, p1Decisions[i], "p1 tuple %d must be unaffected by interleaving with p2", i)
	}
	freshCtx2 := withAuthzInputMemo(context.Background())
	authzFresh2, _ := newRecordingAuthz(newMemoTestStore(s))
	for i, a := range ResourceActions["agent"] {
		d := authzFresh2.CheckAccess(freshCtx2, p2.user, p2.agentRes, a)
		assertDecisionsEqual(t, d, p2Decisions[i], "p2 tuple %d must be unaffected by interleaving with p1", i)
	}
}

// TestParity_C1_ConstraintDenyOnProject is design row C1: an access
// constraint denying the member group's permission on the project excludes
// it in both paths, including on relationship grants; deep-equal.
func TestParity_C1_ConstraintDenyOnProject(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	f := newP1Fixture(t, s, "c1")

	_, err := s.CreateAccessConstraint(ctx, &store.AccessConstraint{
		Name:               "c1-deny-agent-read",
		SubjectKind:        store.ConstraintSubjectGroupClosure,
		SubjectGroupID:     &f.groupID,
		ScopeType:          ScopeTypeProject,
		ScopeID:            f.projectID,
		MaximumPermissions: []string{}, // empty allowlist excludes every permission
		CreatedBy:          "test",
	})
	require.NoError(t, err)

	refDecisions, _, _, _ := runParity(t, s, f.user, agentResourceTuples(f.agentRes))
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "tuple %d must be denied by the access constraint", i)
	}
}

// TestParity_R1_OwnerOnlyNoBinding is design row R1: a user with no role
// binding at all, granted only through the resource-owner relationship.
// Deep-equal.
func TestParity_R1_OwnerOnlyNoBinding(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	ownerID := tid("r1-owner")
	agentID := tid("r1-agent")
	projectID := tid("r1-project")
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: "r1-proj", Name: "r1"}))
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: ownerID, Email: "r1@test.com", DisplayName: "r1", Role: "member", Status: "active"}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: "r1-agent", Name: "r1-agent", ProjectID: projectID, Phase: "running", OwnerID: ownerID}))
	owner := NewAuthenticatedUser(ownerID, "r1@test.com", "r1", "member", "api")
	res := Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID}

	refDecisions, _, _, _ := runParity(t, s, owner, agentResourceTuples(res))
	var sawOwnerGrant bool
	for _, d := range refDecisions {
		if d.Allowed && d.Reason == "relationship grant: resource owner" {
			sawOwnerGrant = true
		}
	}
	assert.True(t, sawOwnerGrant, "H2: at least one action must be granted through the owner relationship")
}

// TestParity_X3_Immutability is design row X3: pinned to the A1 agent
// principal over its own agent resource (step 5b synthetic role) and a skill
// resource (step 5b2 synthetic catalog role). The stored roleDefs must lack
// synthetic roles, and the map handed back to decide is never the stored
// map (checked on both a miss handle, decision 1, and a hit handle,
// decision 2, per R4-N2/R5-N2).
func TestParity_X3_Immutability(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "x3")
	ctx := withAuthzInputMemo(context.Background())
	authz, _ := newRecordingAuthz(newMemoTestStore(s))

	// H2 precondition: the agent's own-project resource must show a
	// synthetic-role grant (step 5b) with Explain on, before the immutability
	// assertions below (which run without Explain, matching production).
	explainDecision := authz.Decide(context.Background(), AuthzRequest{
		Principal:  PrincipalContext{Identity: f.agent},
		Credential: credentialContextForIdentity(f.agent),
		Resource:   f.resource,
		Action:     ActionRead,
		Explain:    true,
	})
	require.NotNil(t, explainDecision.Provenance)
	require.NotEmpty(t, explainDecision.Provenance.Grants, "H2: step 5b's synthetic role must appear as a grant in the reference provenance")

	// Decision 1: a miss handle stores roleDefs. Decision 2: a hit handle
	// reads them. Both must clone rather than hand back the stored map.
	authz.CheckAccess(ctx, f.agent, f.resource, ActionRead) // decision 1: miss
	in := authz.inputsFor(ctx, f.agent)
	_, _ = in.Principals()
	_, _ = in.Bindings()
	rd1, err := in.RoleDefs() // decision 2 (conceptually): hit
	require.NoError(t, err)

	memo := authzInputMemoFromContext(ctx)
	require.NotNil(t, memo)
	key := principalKey{normType: "agent", id: f.agentID}
	entry := memo.entryFor(key)
	require.True(t, entry.roleDefsOK)

	// The handle must never hand back the stored map itself: mutating the
	// returned copy must not affect the stored entry.
	before := len(entry.roleDefs)
	for id := range rd1 {
		delete(rd1, id)
		break
	}
	assert.Equal(t, before, len(entry.roleDefs), "RoleDefs() must return a clone; mutating it must not affect the stored map")

	// The synthetic role (step 5b) must not have leaked into the stored map:
	// decide only writes it into its own local clone.
	for id, rp := range entry.roleDefs {
		assert.NotContains(t, id, "synthetic", "stored roleDefs must not contain a synthetic role id %q: %+v", id, rp)
	}
}

// =============================================================================
// T rows: resource-type coverage (B7), a representative subset
// =============================================================================

// resourceTypeTuples builds one tuple per ResourceActions[res.Type].
func resourceTypeTuples(res Resource) []rawTuple {
	out := make([]rawTuple, 0, len(ResourceActions[res.Type]))
	for _, a := range ResourceActions[res.Type] {
		out = append(out, rawTuple{res, a})
	}
	return out
}

// TestParity_T1_Project is design row T1: a parentless project resource,
// exercising the UAT gate's project-resource shape. Deep-equal.
func TestParity_T1_Project(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "t1")
	res := Resource{Type: "project", ID: f.projectID}
	runParity(t, s, f.user, resourceTypeTuples(res))
}

// TestParity_T4_SkillProgeny is design row T4: an agent reads its origin
// user's personal (user-scoped) skill through the progeny relationship. The
// skill fact itself is pure; the store call is stage-4 relationshipSourceActive
// GetUser, which stays per decision even under the memo (R5-Nit5).
func TestParity_T4_SkillProgeny(t *testing.T) {
	gf := newGoldenFixture(t)
	agent := &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: tid("t4-skill-agent")},
		ProjectID: gf.projectBeta.ID,
		Ancestry:  []string{gf.projectOwnerID},
		Scopes:    allRegisteredAgentScopes(),
	}}
	skillRes := Resource{Type: "skill", ID: gf.skillInjectionID, ScopeKind: store.SkillScopeUser, ScopeUserID: gf.projectOwnerID}

	ctx := context.Background()
	refStore := newMemoTestStore(gf.store)
	refAuthz, refEmit := newRecordingAuthz(refStore)
	refDecision := refAuthz.CheckAccess(ctx, agent, skillRes, ActionRead)
	require.True(t, refDecision.Allowed, "reference progeny skill read must be allowed: %q", refDecision.Reason)

	candStore := newMemoTestStore(gf.store)
	candAuthz, candEmit := newRecordingAuthz(candStore)
	candDecision := candAuthz.CheckAccess(withAuthzInputMemo(ctx), agent, skillRes, ActionRead)

	assertDecisionsEqual(t, refDecision, candDecision)
	assertAuditSequenceEqual(t, refEmit.snapshot(), candEmit.snapshot())
}

// TestParity_T5_SecretProgenyRawLoop is design row T5: raw Decide with an
// explicit Permission ("project.secret_read"), the shape httpdispatcher
// uses. secret has no capability action, so it is unreachable through the
// batch. Progeny facts make store calls and stay per decision.
func TestParity_T5_SecretProgenyRawLoop(t *testing.T) {
	gf := newGoldenFixture(t)
	agent := &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: tid("t5-secret-agent")},
		ProjectID: gf.projectBeta.ID,
		Ancestry:  []string{gf.projectOwnerID},
		Scopes:    allRegisteredAgentScopes(),
	}}
	secretRes := Resource{Type: "secret", ID: gf.secretID}
	p := permissions.Permission{ID: "project.secret_read", Action: string(ActionRead)}

	ctx := context.Background()
	refStore := newMemoTestStore(gf.store)
	refAuthz, refEmit := newRecordingAuthz(refStore)
	refDecision := decideExplicit(t, refAuthz, agent, secretRes, p)
	require.True(t, refDecision.Allowed, "reference progeny secret read must be allowed: %q", refDecision.Reason)

	candStore := newMemoTestStore(gf.store)
	candAuthz, candEmit := newRecordingAuthz(candStore)
	candDecision := candAuthz.Decide(withAuthzInputMemo(ctx), AuthzRequest{
		Principal:  principalContextForIdentity(agent),
		Credential: credentialContextForIdentity(agent),
		Resource:   secretRes,
		Action:     ActionRead,
		Permission: p.ID,
	})

	assertDecisionsEqual(t, refDecision, candDecision, "memo-ctx candidate must match the reference")
	assertAuditSequenceEqual(t, refEmit.snapshot(), candEmit.snapshot())
}

// TestParity_T8_SecretUseRuntimeRow is design row T8 (ba762d56): raw Decide,
// secret.use (ActionUse), agent with scope project:secret:read. secret.use
// has no CapabilityKind, so it is unreachable through the batch. use is not
// a read-only action, so an edge error on this permission fails closed
// exactly like a write.
func TestParity_T8_SecretUseRuntimeRow(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "t8")
	secretID := tid("t8-secret")
	require.NoError(t, s.CreateSecret(context.Background(), &store.Secret{
		ID: secretID, Key: "t8-secret", Scope: "user", ScopeID: f.delegatorID, AllowProgeny: true, CreatedBy: f.delegatorID,
	}))
	secretRes := Resource{Type: "secret", ID: secretID}
	p := permissions.Permission{ID: "secret.use", Action: string(ActionUse)}

	// The agent's ancestry must include the secret owner for the progeny
	// grant to admit secret.use.
	useAgent := &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: tid("t8-use-agent")},
		ProjectID: f.projectID,
		Ancestry:  []string{f.delegatorID},
		Scopes:    allRegisteredAgentScopes(),
	}}

	ctx := context.Background()
	refStore := newMemoTestStore(s)
	refAuthz, _ := newRecordingAuthz(refStore)
	refDecision := decideExplicit(t, refAuthz, useAgent, secretRes, p)

	candStore := newMemoTestStore(s)
	candAuthz, _ := newRecordingAuthz(candStore)
	mctx := withAuthzInputMemo(ctx)
	candDecision := candAuthz.Decide(mctx, AuthzRequest{
		Principal:  principalContextForIdentity(useAgent),
		Credential: credentialContextForIdentity(useAgent),
		Resource:   secretRes,
		Action:     ActionUse,
		Permission: p.ID,
	})

	assertDecisionsEqual(t, refDecision, candDecision)
}

// =============================================================================
// X2/X2b/X2c: concurrency and the deterministic lost race (-race)
// =============================================================================

// TestParity_X2_ConcurrentRequestsIsolated is design row X2: concurrent
// requests with separate contexts (and separate memos) never leak into each
// other. Run with -race.
func TestParity_X2_ConcurrentRequestsIsolated(t *testing.T) {
	_, s := authzTestSetup(t)
	f1 := newP1Fixture(t, s, "x2-1")
	f2 := newP1Fixture(t, s, "x2-2")
	authz, _ := newRecordingAuthz(newMemoTestStore(s))

	var wg sync.WaitGroup
	run := func(f *p1Fixture) {
		defer wg.Done()
		ctx := withAuthzInputMemo(context.Background())
		for i := 0; i < 20; i++ {
			for _, a := range ResourceActions["agent"] {
				authz.CheckAccess(ctx, f.user, f.agentRes, a)
			}
		}
	}
	wg.Add(2)
	go run(f1)
	go run(f2)
	wg.Wait()
}

// TestParity_X2b_ConcurrentGoroutinesOneMemo is design row X2b: N goroutines
// share ONE memo ctx, calling CheckAccess for mixed principals against a
// stable store. No race, and every decision deep-equals the reference. Run
// with -race.
func TestParity_X2b_ConcurrentGoroutinesOneMemo(t *testing.T) {
	_, s := authzTestSetup(t)
	principals := []*p1Fixture{
		newP1Fixture(t, s, "x2b-1"),
		newP1Fixture(t, s, "x2b-2"),
		newP1Fixture(t, s, "x2b-3"),
	}

	// Reference decisions, computed sequentially with no memo.
	refAuthz, _ := newRecordingAuthz(newMemoTestStore(s))
	refCtx := context.Background()
	want := make(map[string]Decision)
	keyFor := func(fi int, action Action) string { return fmt.Sprintf("%d/%s", fi, action) }
	for fi, f := range principals {
		for _, a := range ResourceActions["agent"] {
			want[keyFor(fi, a)] = refAuthz.CheckAccess(refCtx, f.user, f.agentRes, a)
		}
	}

	candAuthz, _ := newRecordingAuthz(newMemoTestStore(s))
	mctx := withAuthzInputMemo(context.Background())
	var mu sync.Mutex
	got := make(map[string]Decision)
	var wg sync.WaitGroup
	for fi, f := range principals {
		fi, f := fi, f
		for _, a := range ResourceActions["agent"] {
			a := a
			wg.Add(1)
			go func() {
				defer wg.Done()
				d := candAuthz.CheckAccess(mctx, f.user, f.agentRes, a)
				mu.Lock()
				got[keyFor(fi, a)] = d
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	for k, w := range want {
		assertDecisionsEqual(t, w, got[k], "key %s", k)
	}
}

// blockOnceStore blocks the first GetEffectiveGroups call for a specific
// user until release is closed, then answers with overrideGroups instead of
// delegating to the real store — used by X2c to force a deterministic lost
// race between two handles for the same principal.
type blockOnceStore struct {
	*memoTestStore
	blockUserID    string
	release        chan struct{}
	overrideGroups []string
	blocked        chan struct{} // closed once the blocked call is parked, waiting on release
}

func (b *blockOnceStore) GetEffectiveGroups(ctx context.Context, userID string) ([]string, error) {
	if userID == b.blockUserID && b.release != nil {
		release := b.release
		b.release = nil // only the first call blocks
		if b.blocked != nil {
			close(b.blocked)
		}
		<-release
		if err := b.memoTestStore.call(ctx, "GetEffectiveGroups"); err != nil {
			return nil, err
		}
		return b.overrideGroups, nil
	}
	if err := b.memoTestStore.call(ctx, "GetEffectiveGroups"); err != nil {
		return nil, err
	}
	return b.memoTestStore.Store.GetEffectiveGroups(ctx, userID)
}

// TestParity_X2c_DeterministicLostRace is design row X2c: two handles, hA
// and hB, for the same principal on one memo. hB's GetEffectiveGroups is
// blocked on a channel; hA loads closure A and stores it first; hB is then
// released with closure B != A. Both orderings (o1: hB first, o2: hA first)
// must show hB detached: it queries bindings/roleDefs with its OWN refs B
// and stores nothing, while the entry ends holding A's values throughout.
func TestParity_X2c_DeterministicLostRace(t *testing.T) {
	for _, order := range []string{"o1_detached_first", "o2_attached_first"} {
		t.Run(order, func(t *testing.T) {
			_, s := authzTestSetup(t)
			f := newP1Fixture(t, s, "x2c-"+order)

			// A second, disjoint group for the SAME user, so a closure built
			// from it ("closure B") differs from the real one ("closure A").
			groupB := &store.Group{ID: tid("x2c-" + order + "-groupB"), Name: "B", Slug: "x2c-" + order + "-groupB", GroupType: store.GroupTypeExplicit}
			require.NoError(t, s.CreateGroup(context.Background(), groupB))

			release := make(chan struct{})
			blocked := make(chan struct{})
			bs := &blockOnceStore{
				memoTestStore:  newMemoTestStore(s),
				blockUserID:    f.userID,
				release:        release,
				overrideGroups: []string{groupB.ID},
				blocked:        blocked,
			}
			authz := NewAuthzService(bs, slog.Default())
			mctx := withAuthzInputMemo(context.Background())

			hA := authz.inputsFor(mctx, f.user)
			hB := authz.inputsFor(mctx, f.user)

			var hBDone = make(chan struct{})
			go func() {
				defer close(hBDone)
				<-blocked // wait until hB's GetEffectiveGroups call has parked
				// hA proceeds and stores closure A while hB is still blocked.
				_, err := hA.Principals()
				require.NoError(t, err)
				if order == "o2_attached_first" {
					_, err = hA.Bindings()
					require.NoError(t, err)
					_, err = hA.RoleDefs()
					require.NoError(t, err)
				}
				close(release) // release hB with closure B
			}()

			_, errB := hB.Principals()
			require.NoError(t, errB)
			<-hBDone

			// hB is detached: it must not consume the entry's bindings slot.
			bindingsB, err := hB.Bindings()
			require.NoError(t, err)
			_, err = hB.RoleDefs()
			require.NoError(t, err)

			memo := authzInputMemoFromContext(mctx)
			key := principalKey{normType: "user", id: f.userID}
			entry := memo.entryFor(key)
			require.True(t, entry.refsOK)
			// The entry must hold A's refs (the real group), not B's.
			var entryHasGroupB bool
			for _, r := range entry.refs {
				if r.ID == groupB.ID {
					entryHasGroupB = true
				}
			}
			assert.False(t, entryHasGroupB, "the entry must hold closure A, not the detached handle's closure B")

			// hB's own Bindings() call must have queried with refs B (user +
			// groupB), which has no role binding at all — unlike refs A's
			// real group, which does (via p1Fixture's setup). An empty
			// result is therefore proof hB queried with B, not a silent
			// reuse of the entry's bindings(A).
			assert.Empty(t, bindingsB, "hB must query with its own refs B (no binding), not consume the entry's bindings(A)")
		})
	}
}

// =============================================================================
// X4/X5: freshness across separately-installed memos
// =============================================================================

// TestParity_X4_CrossRequestFreshness is design row X4: a binding removed
// between two separate memo ctxs (two independent "requests") must be
// reflected in the second one — the memo never outlives its ctx.
func TestParity_X4_CrossRequestFreshness(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newProjectPrincipalFixture(t, s, "x4", store.ProjectRoleOwner)
	authz, _ := newRecordingAuthz(newMemoTestStore(s))

	ctx1 := withAuthzInputMemo(context.Background())
	d1 := authz.CheckAccess(ctx1, f.user, f.agentRes, ActionDelete)
	require.True(t, d1.Allowed, "H2: project admin must initially be allowed to delete")

	bg := context.Background()
	bindings, err := s.ListRoleBindingsForPrincipals(bg, []store.PrincipalRef{{Type: "user", ID: f.userID}}, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, bindings)
	for _, b := range bindings {
		require.NoError(t, s.DeleteRoleBinding(bg, b.ID))
	}

	ctx2 := withAuthzInputMemo(context.Background()) // a separate memo/request
	d2 := authz.CheckAccess(ctx2, f.user, f.agentRes, ActionDelete)
	assert.False(t, d2.Allowed, "request 2's own fresh memo must reflect the removed binding")
}

// TestParity_X5_BatchLevelInstallIsPerCall is design row X5: each
// installation is scoped to one call; a binding added between two calls is
// visible to the second call's own fresh memo.
func TestParity_X5_BatchLevelInstallIsPerCall(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "x5")
	authz, _ := newRecordingAuthz(newMemoTestStore(s))

	caps1 := authz.ComputeCapabilities(withAuthzInputMemo(context.Background()), f.user, f.agentRes)
	require.NotContains(t, caps1.Actions, string(ActionDelete), "H2: an ordinary member must not start with delete")

	createDCUser(t, s, f.userID, "x5@test.com", f.projectID, store.ProjectRoleOwner)

	caps2 := authz.ComputeCapabilities(withAuthzInputMemo(context.Background()), f.user, f.agentRes)
	assert.Contains(t, caps2.Actions, string(ActionDelete), "the second call's own fresh memo must see the new admin binding")
}

// =============================================================================
// Remaining P/U/C/R/T rows (completing the section 8 matrix for branch 1)
// =============================================================================

// TestParity_P2_DirectBindingMember is design row P2 ("direct-binding
// member | deep-equal"): a direct (non-group) role binding, the shape
// newProjectPrincipalFixture already builds.
func TestParity_P2_DirectBindingMember(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newProjectPrincipalFixture(t, s, "p2", store.ProjectRoleMember)
	_, _, _, candStore := runParity(t, s, f.user, agentResourceTuples(f.agentRes))
	assert.Equal(t, 1, candStore.countOf("GetEffectiveGroups"), "groups loaded once under the memo")
}

// TestParity_P3_ProjectOwnerWithOwnedAgent is design row P3 ("project owner
// (binding + OwnerID on some agents) | deep-equal, including the owner
// relationship candidate"): the owner has both a role binding AND is the
// OwnerID of one of the two agent resources tested, so both the
// binding-granted path and the owner-relationship path are exercised.
func TestParity_P3_ProjectOwnerWithOwnedAgent(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	projectID := tid("p3-project")
	ownerID := tid("p3-owner")
	otherOwnerID := tid("p3-other-owner")
	ownedAgentID := tid("p3-owned-agent")
	otherAgentID := tid("p3-other-agent")

	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: "p3-proj", Name: "p3", OwnerID: ownerID}))
	createDCUser(t, s, ownerID, "p3-owner@test.com", projectID, store.ProjectRoleOwner)
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: ownedAgentID, Slug: "p3-owned", Name: "p3-owned", ProjectID: projectID, Phase: "running", OwnerID: ownerID, Ancestry: []string{ownerID}}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: otherAgentID, Slug: "p3-other", Name: "p3-other", ProjectID: projectID, Phase: "running", OwnerID: otherOwnerID, Ancestry: []string{otherOwnerID}}))

	owner := NewAuthenticatedUser(ownerID, "p3-owner@test.com", "p3", "member", "api")
	ownedRes := Resource{Type: "agent", ID: ownedAgentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID}
	otherRes := Resource{Type: "agent", ID: otherAgentID, ParentType: "project", ParentID: projectID, OwnerID: otherOwnerID}

	tuples := append(agentResourceTuples(ownedRes), agentResourceTuples(otherRes)...)
	refDecisions, _, _, _ := runParity(t, s, owner, tuples)
	var sawOwnerGrant bool
	for _, d := range refDecisions {
		if d.Allowed && d.Reason == "relationship grant: resource owner" {
			sawOwnerGrant = true
		}
	}
	assert.True(t, sawOwnerGrant, "H2: the owner-relationship candidate must actually be exercised on the owned agent")
}

// TestParity_P5_HubAdmin is design row P5 ("hub-admin | deep-equal").
func TestParity_P5_HubAdmin(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	userID := tid("p5-hubadmin")
	createTestUserWithRole(t, s, userID, "p5@test.com", "admin", store.SystemRoleHubAdmin)
	admin := NewAuthenticatedUser(userID, "p5@test.com", "p5", "admin", "api")

	projectID := tid("p5-project")
	ownerID := tid("p5-owner")
	agentID := tid("p5-agent")
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: "p5-proj", Name: "p5", OwnerID: ownerID}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: "p5-agent", Name: "p5-agent", ProjectID: projectID, Phase: "running", OwnerID: ownerID, Ancestry: []string{ownerID}}))
	res := Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID}

	runParity(t, s, admin, agentResourceTuples(res))
}

// TestParity_P6_SuperAdmin is design row P6 ("super-admin | deep-equal").
func TestParity_P6_SuperAdmin(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	userID := tid("p6-superadmin")
	createTestUserWithRole(t, s, userID, "p6@test.com", "admin", store.SystemRoleSuperAdmin)
	admin := NewAuthenticatedUser(userID, "p6@test.com", "p6", "admin", "api")

	projectID := tid("p6-project")
	ownerID := tid("p6-owner")
	agentID := tid("p6-agent")
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: "p6-proj", Name: "p6", OwnerID: ownerID}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: "p6-agent", Name: "p6-agent", ProjectID: projectID, Phase: "running", OwnerID: ownerID, Ancestry: []string{ownerID}}))
	res := Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID}

	refDecisions, _, _, _ := runParity(t, s, admin, agentResourceTuples(res))
	for i, d := range refDecisions {
		assert.True(t, d.Allowed, "super-admin must be allowed on tuple %d", i)
	}
}

// TestParity_P7_NonMemberHubUser is design row P7 ("non-member hub user |
// deep-equal denies"): a hub member with no binding on this project at all.
func TestParity_P7_NonMemberHubUser(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	userID := tid("p7-nonmember")
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: userID, Email: "p7@test.com", DisplayName: "p7", Role: "member", Status: "active"}))
	user := NewAuthenticatedUser(userID, "p7@test.com", "p7", "member", "api")

	projectID := tid("p7-project")
	ownerID := tid("p7-owner")
	agentID := tid("p7-agent")
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: "p7-proj", Name: "p7", OwnerID: ownerID}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: "p7-agent", Name: "p7-agent", ProjectID: projectID, Phase: "running", OwnerID: ownerID, Ancestry: []string{ownerID}}))
	res := Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID}

	refDecisions, _, _, _ := runParity(t, s, user, agentResourceTuples(res))
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "non-member must be denied on tuple %d", i)
	}
}

// TestParity_U2_ScopedAndUnscopedSameUserNoLeakage is design row U2 ("scoped
// UAT and unscoped session for the same user in one ctx | no credential
// leakage either way"). A ScopedUserIdentity wrapping a UserIdentity has the
// SAME principalKey as the unscoped identity (Type()/ID() are promoted
// through the embedded field), so both share one memo entry by
// construction. This row proves that sharing is safe: each decision still
// applies its OWN credential restriction (7a) independently, so the
// broader unscoped grant never leaks into the scoped identity's decisions,
// and vice versa.
func TestParity_U2_ScopedAndUnscopedSameUserNoLeakage(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "u2")
	ceiling, ok := permissions.BuildCeilingFromSelectors([]string{"agent:read"})
	require.True(t, ok)
	scoped := NewScopedUserIdentityWithCeiling(f.user, f.projectID, []string{"agent:read"}, "u2-uat", ceiling)

	// References: each identity evaluated alone, no memo, no interleaving.
	refUnscoped, _, _, _ := runParity(t, s, f.user, agentResourceTuples(f.agentRes))
	refScoped, _, _, _ := runParity(t, s, scoped, agentResourceTuples(f.agentRes))

	// Candidate: BOTH identities interleaved in ONE memo ctx.
	candStore := newMemoTestStore(s)
	candAuthz, _ := newRecordingAuthz(candStore)
	mctx := withAuthzInputMemo(context.Background())
	actions := ResourceActions["agent"]
	var candUnscoped, candScoped []Decision
	for _, a := range actions {
		candUnscoped = append(candUnscoped, candAuthz.CheckAccess(mctx, f.user, f.agentRes, a))
		candScoped = append(candScoped, candAuthz.CheckAccess(mctx, scoped, f.agentRes, a))
	}

	for i := range actions {
		assertDecisionsEqual(t, refUnscoped[i], candUnscoped[i], "unscoped tuple %d: the scoped identity sharing the memo entry must not narrow it", i)
		assertDecisionsEqual(t, refScoped[i], candScoped[i], "scoped tuple %d: the unscoped identity sharing the memo entry must not widen it", i)
	}
	assert.Equal(t, 1, candStore.countOf("GetEffectiveGroups"), "both identities share one memo entry (same principalKey)")

	var sawScopedDeny, sawUnscopedAllow bool
	for i := range actions {
		if !candScoped[i].Allowed {
			sawScopedDeny = true
		}
		if candUnscoped[i].Allowed {
			sawUnscopedAllow = true
		}
	}
	assert.True(t, sawScopedDeny, "H2: the scoped identity must actually be restricted by its own ceiling")
	assert.True(t, sawUnscopedAllow, "H2: the unscoped identity must actually retain its full access")
}

// TestParity_U3_ScopedUATAnotherProject is design row U3 ("scoped UAT for
// another project | deep-equal step-1 denies; zero closure loads"). The
// ceiling may be real (R6-Nit4: U3 does not depend on the ceiling), but the
// resource's project differs from the UAT's scoped project, so
// enforceUATConstraints denies at step 1, before step 2 ever runs.
func TestParity_U3_ScopedUATAnotherProject(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "u3")
	otherProjectID := tid("u3-other-project")
	ctx := context.Background()
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: otherProjectID, Slug: "u3-other-proj", Name: "u3-other"}))
	otherAgentID := tid("u3-other-agent")
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: otherAgentID, Slug: "u3-other-agent", Name: "u3-other-agent", ProjectID: otherProjectID, Phase: "running"}))
	otherRes := Resource{Type: "agent", ID: otherAgentID, ParentType: "project", ParentID: otherProjectID}

	ceiling, ok := permissions.BuildCeilingFromSelectors([]string{"agent:read"})
	require.True(t, ok)
	scoped := NewScopedUserIdentityWithCeiling(f.user, f.projectID, []string{"agent:read"}, "u3-uat", ceiling)

	refDecisions, _, refStore, candStore := runParity(t, s, scoped, agentResourceTuples(otherRes))
	for i, d := range refDecisions {
		assert.False(t, d.Allowed, "tuple %d must deny at step 1 (wrong project)", i)
		assert.Equal(t, "token not scoped for this project", d.Reason, "tuple %d", i)
	}
	assert.Equal(t, 0, refStore.countOf("GetEffectiveGroups"), "reference: zero closure loads")
	assert.Equal(t, 0, candStore.countOf("GetEffectiveGroups"), "candidate: zero closure loads")
}

// TestParity_C2_ConstraintTimeWindow is design row C2 ("constraint time
// window inactive / active | deep-equal").
func TestParity_C2_ConstraintTimeWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
	}{
		{"inactive_not_yet", false},
		{"active", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := authzTestSetup(t)
			ctx := context.Background()
			f := newP1Fixture(t, s, "c2-"+tc.name)

			notBefore := time.Now().Add(-time.Hour)
			if !tc.active {
				notBefore = time.Now().Add(time.Hour) // not active yet
			}
			_, err := s.CreateAccessConstraint(ctx, &store.AccessConstraint{
				Name:               "c2-" + tc.name,
				SubjectKind:        store.ConstraintSubjectGroupClosure,
				SubjectGroupID:     &f.groupID,
				ScopeType:          ScopeTypeProject,
				ScopeID:            f.projectID,
				MaximumPermissions: []string{},
				NotBefore:          &notBefore,
				CreatedBy:          "test",
			})
			require.NoError(t, err)

			refDecisions, _, _, _ := runParity(t, s, f.user, agentResourceTuples(f.agentRes))
			var sawAllow bool
			for _, d := range refDecisions {
				if d.Allowed {
					sawAllow = true
				}
			}
			if tc.active {
				assert.False(t, sawAllow, "H2: an active empty-allowlist constraint must deny everything")
			} else {
				assert.True(t, sawAllow, "H2: an inactive (not-yet) constraint must not restrict anything")
			}
		})
	}
}

// TestParity_C4_SystemVsProjectScopedConstraint is design row C4
// ("system-scoped vs project-scoped constraint | deep-equal").
func TestParity_C4_SystemVsProjectScopedConstraint(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scopeType string
	}{
		{"system", ScopeTypeSystem},
		{"project", ScopeTypeProject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := authzTestSetup(t)
			ctx := context.Background()
			f := newP1Fixture(t, s, "c4-"+tc.name)

			scopeID := ""
			if tc.scopeType == ScopeTypeProject {
				scopeID = f.projectID
			}
			_, err := s.CreateAccessConstraint(ctx, &store.AccessConstraint{
				Name:               "c4-" + tc.name,
				SubjectKind:        store.ConstraintSubjectGroupClosure,
				SubjectGroupID:     &f.groupID,
				ScopeType:          tc.scopeType,
				ScopeID:            scopeID,
				MaximumPermissions: []string{},
				CreatedBy:          "test",
			})
			require.NoError(t, err)

			refDecisions, _, _, _ := runParity(t, s, f.user, agentResourceTuples(f.agentRes))
			for i, d := range refDecisions {
				assert.False(t, d.Allowed, "tuple %d must be denied by the %s-scoped constraint", i, tc.scopeType)
			}
		})
	}
}

// TestParity_R2_AncestorOnly is design row R2 ("ancestor only | deep-equal").
func TestParity_R2_AncestorOnly(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	ancestorID := tid("r2-ancestor")
	outsiderID := tid("r2-outsider")
	projectID := tid("r2-project")
	agentID := tid("r2-agent")
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: ancestorID, Email: "r2-ancestor@test.com", DisplayName: "r2a", Role: "member", Status: "active"}))
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: outsiderID, Email: "r2-outsider@test.com", DisplayName: "r2o", Role: "member", Status: "active"}))
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: "r2-proj", Name: "r2"}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: "r2-agent", Name: "r2-agent", ProjectID: projectID, Phase: "running", Ancestry: []string{ancestorID}}))
	res := Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, Ancestry: []string{ancestorID}}

	ancestor := NewAuthenticatedUser(ancestorID, "r2-ancestor@test.com", "r2a", "member", "api")
	refDecisions, _, _, _ := runParity(t, s, ancestor, agentResourceTuples(res))
	var sawAncestorGrant bool
	for _, d := range refDecisions {
		if d.Allowed && d.Reason == "relationship grant: ancestor access" {
			sawAncestorGrant = true
		}
	}
	assert.True(t, sawAncestorGrant, "H2: the ancestor relationship must actually be exercised")

	outsider := NewAuthenticatedUser(outsiderID, "r2-outsider@test.com", "r2o", "member", "api")
	outsiderDecisions, _, _, _ := runParity(t, s, outsider, agentResourceTuples(res))
	for i, d := range outsiderDecisions {
		assert.False(t, d.Allowed, "outsider tuple %d must deny", i)
	}
}

// TestParity_R3_RelationshipGrantRestrictedByUATScope is design row R3
// ("relationship grant restricted by UAT scope | deep-equal 'relationship
// grant restricted by ...'"). The UAT owner's ceiling does not cover
// agent.delete, so the owner relationship would grant it but the 7a
// restriction blocks it, producing the "relationship grant restricted by"
// Reason prefix (decide step 9).
func TestParity_R3_RelationshipGrantRestrictedByUATScope(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	ownerID := tid("r3-owner")
	projectID := tid("r3-project")
	agentID := tid("r3-agent")
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: ownerID, Email: "r3-owner@test.com", DisplayName: "r3", Role: "member", Status: "active"}))
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projectID, Slug: "r3-proj", Name: "r3", OwnerID: ownerID}))
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: agentID, Slug: "r3-agent", Name: "r3-agent", ProjectID: projectID, Phase: "running", OwnerID: ownerID, Ancestry: []string{ownerID}}))
	res := Resource{Type: "agent", ID: agentID, ParentType: "project", ParentID: projectID, OwnerID: ownerID}

	owner := NewAuthenticatedUser(ownerID, "r3-owner@test.com", "r3", "member", "api")
	ceiling, ok := permissions.BuildCeilingFromSelectors([]string{"agent:read"}) // deliberately NOT agent:delete
	require.True(t, ok)
	scoped := NewScopedUserIdentityWithCeiling(owner, projectID, []string{"agent:read"}, "r3-uat", ceiling)

	refDecisions, _, _, _ := runParity(t, s, scoped, []rawTuple{{res, ActionDelete}})
	require.Len(t, refDecisions, 1)
	assert.False(t, refDecisions[0].Allowed)
	assert.Contains(t, refDecisions[0].Reason, "relationship grant restricted by", "H2: must hit the restricted-relationship-grant branch, not a plain deny")
}

// =============================================================================
// Remaining T rows (resource-type coverage, B7): batch + AuthorizeReadBatch
// =============================================================================

// runBatchParity proves deep-equal for one resource through both
// ComputeCapabilitiesBatch and AuthorizeReadBatch — the two paths design
// section 8's B7 note specifies for resource-type coverage — reference (no
// memo) vs candidate (one memo installed for both batch calls). It returns
// the reference capability actions and AuthorizeReadBatch result for
// row-specific H2 assertions.
func runBatchParity(t *testing.T, s store.Store, identity Identity, resourceType string, res Resource) (refActions []string, refAllowed []bool) {
	t.Helper()
	ctx := context.Background()

	refStore := newMemoTestStore(s)
	refAuthz, _ := newRecordingAuthz(refStore)
	refCaps := refAuthz.ComputeCapabilitiesBatch(ctx, identity, []Resource{res}, resourceType)
	refAllowed, refErr := refAuthz.AuthorizeReadBatch(ctx, identity, []Resource{res})
	require.NoError(t, refErr)

	candStore := newMemoTestStore(s)
	candAuthz, _ := newRecordingAuthz(candStore)
	mctx := withAuthzInputMemo(ctx)
	candCaps := candAuthz.ComputeCapabilitiesBatch(mctx, identity, []Resource{res}, resourceType)
	candAllowed, candErr := candAuthz.AuthorizeReadBatch(mctx, identity, []Resource{res})
	require.NoError(t, candErr)

	require.Len(t, refCaps, 1)
	require.Len(t, candCaps, 1)
	assert.Equal(t, refCaps[0].Actions, candCaps[0].Actions, "ComputeCapabilitiesBatch actions for %s %s", resourceType, res.ID)
	assert.Equal(t, refAllowed, candAllowed, "AuthorizeReadBatch for %s %s", resourceType, res.ID)
	return refCaps[0].Actions, refAllowed
}

// TestParity_T2_TemplateGlobalAndProjectScoped is design row T2 ("template,
// global + project-scoped | batch + AuthorizeReadBatch | hub-wide filter
// reads roleDefs (authz_template_scope.go:72)").
func TestParity_T2_TemplateGlobalAndProjectScoped(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "t2")
	ctx := context.Background()
	globalTplID := tid("t2-global-tpl")
	projectTplID := tid("t2-project-tpl")
	require.NoError(t, s.CreateTemplate(ctx, &store.Template{ID: globalTplID, Name: "t2g", Slug: "t2g", Harness: "claude", Image: "img", Scope: store.TemplateScopeGlobal}))
	require.NoError(t, s.CreateTemplate(ctx, &store.Template{ID: projectTplID, Name: "t2p", Slug: "t2p", Harness: "claude", Image: "img", Scope: store.TemplateScopeProject, ScopeID: f.projectID}))

	t.Run("global", func(t *testing.T) {
		runBatchParity(t, s, f.user, "template", templateResource(&store.Template{ID: globalTplID, Scope: store.TemplateScopeGlobal}))
	})
	t.Run("project", func(t *testing.T) {
		runBatchParity(t, s, f.user, "template", templateResource(&store.Template{ID: projectTplID, Scope: store.TemplateScopeProject, ScopeID: f.projectID}))
	})
}

// TestParity_T3_HarnessConfigGlobalAndProjectScoped is design row T3
// ("harness_config, global + project-scoped | batch + AuthorizeReadBatch |
// hub-wide filter (authz_harness_config_scope.go:72)").
func TestParity_T3_HarnessConfigGlobalAndProjectScoped(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "t3")
	ctx := context.Background()
	globalHCID := tid("t3-global-hc")
	projectHCID := tid("t3-project-hc")
	require.NoError(t, s.CreateHarnessConfig(ctx, &store.HarnessConfig{ID: globalHCID, Name: "t3g", Slug: "t3g", Harness: "claude", Scope: store.HarnessConfigScopeGlobal}))
	require.NoError(t, s.CreateHarnessConfig(ctx, &store.HarnessConfig{ID: projectHCID, Name: "t3p", Slug: "t3p", Harness: "claude", Scope: store.HarnessConfigScopeProject, ScopeID: f.projectID}))

	t.Run("global", func(t *testing.T) {
		runBatchParity(t, s, f.user, "harness_config", harnessConfigResource(&store.HarnessConfig{ID: globalHCID, Scope: store.HarnessConfigScopeGlobal}))
	})
	t.Run("project", func(t *testing.T) {
		runBatchParity(t, s, f.user, "harness_config", harnessConfigResource(&store.HarnessConfig{ID: projectHCID, Scope: store.HarnessConfigScopeProject, ScopeID: f.projectID}))
	})
}

// TestParity_T6_GCPServiceAccountHubScopedAssign is design row T6
// ("gcp_service_account, hub-scoped, assign | batch | hub-member fact
// (:176-194)"): a current hub member (group membership, not just a role
// binding) may assign a hub-scoped service account they did not create,
// through the hub_member_sa_assign relationship.
func TestParity_T6_GCPServiceAccountHubScopedAssign(t *testing.T) {
	_, s := authzTestSetup(t)
	ctx := context.Background()
	memberID := tid("t6-member")
	creatorID := tid("t6-creator")
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: memberID, Email: "t6@test.com", DisplayName: "t6", Role: "member", Status: "active"}))
	hubGroup, err := s.GetGroupBySlug(ctx, "hub-members")
	require.NoError(t, err)
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{GroupID: hubGroup.ID, MemberID: memberID, MemberType: store.GroupMemberTypeUser, Role: store.GroupMemberRoleMember}))
	member := NewAuthenticatedUser(memberID, "t6@test.com", "t6", "member", "api")

	hubSA := gcpServiceAccountResource(&store.GCPServiceAccount{ID: tid("t6-sa"), CreatedBy: creatorID, Scope: store.ScopeHub, ScopeID: "hub"})
	actions, _ := runBatchParity(t, s, member, "gcp_service_account", hubSA)
	assert.Contains(t, actions, "assign", "H2: the hub-member fact must actually grant assign on a hub-scoped SA the member did not create")
}

// TestParity_T7_ParentlessDenyBaselineForAgent is design row T7 ("broker,
// group, user for the agent principal | batch + AuthorizeReadBatch |
// parentless deny baseline").
func TestParity_T7_ParentlessDenyBaselineForAgent(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newA1Fixture(t, s, "t7")
	ctx := context.Background()
	brokerID := tid("t7-broker")
	groupID := tid("t7-group")
	userID := tid("t7-user")
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{ID: brokerID, Name: "t7-broker", Slug: "t7-broker"}))
	require.NoError(t, s.CreateGroup(ctx, &store.Group{ID: groupID, Name: "t7-group", Slug: "t7-group", GroupType: store.GroupTypeExplicit}))
	require.NoError(t, s.CreateUser(ctx, &store.User{ID: userID, Email: "t7@test.com", DisplayName: "t7", Role: "member", Status: "active"}))

	for _, res := range []Resource{
		brokerResource(&store.RuntimeBroker{ID: brokerID}),
		groupResource(&store.Group{ID: groupID}),
		userResource(&store.User{ID: userID}),
	} {
		t.Run(res.Type, func(t *testing.T) {
			actions, allowed := runBatchParity(t, s, f.agent, res.Type, res)
			assert.Empty(t, actions, "H2: the agent must get an empty capability set (parentless deny baseline) for %s", res.Type)
			for _, a := range allowed {
				assert.False(t, a, "H2: AuthorizeReadBatch must deny %s", res.Type)
			}
		})
	}
}

// =============================================================================
// #2155 check: identity re-classification adds no new decide path or
// principal type the memo must handle
// =============================================================================

// TestParity_BrokerOnBehalfOf_SharesMemoEntryWithPlainUser confirms the
// slow-list-lead's #2155 question directly rather than by analysis alone:
// ptone/scion#2155 (d79b358) rewrote decide's early classification into a
// type-assertion switch (principalContextForIdentity) plus a new
// suppliedCredentialCompatible/brokerOnBehalfOfAuthorizes gate, entirely
// upstream of step 2 (a.inputsFor). It does not add a new PrincipalKind
// (principalContextForIdentity's switch still produces only the kinds the
// const block already named) and does not touch NormalizePrincipalType or
// authorizationPrincipals, which principalKey is keyed from. A
// broker-on-behalf-of request for a local user is still, from step 2
// onward, exactly a request from that *AuthenticatedUser — only
// Credential.Kind differs, and credential data is never a memoized input
// (design 4.1 rule 4). This test proves it: the broker-on-behalf-of
// decision and a plain interactive decision for the SAME user, run in one
// memo phase, are deep-equal apart from the credential-identifying fields,
// and share exactly one memo entry (one GetEffectiveGroups call for both).
func TestParity_BrokerOnBehalfOf_SharesMemoEntryWithPlainUser(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "obo")
	brokerID := tid("obo-broker")
	broker := NewBrokerIdentity(brokerID)

	// Build the ctx BrokerAuthMiddleware would install after HMAC
	// verification and a successful on-behalf-of resolution: the broker
	// identity marker, the dedicated BrokerOnBehalfOf marker naming this
	// principal, and the effective identity set to the local user.
	oboCtx := contextWithBrokerIdentity(context.Background(), broker)
	oboCtx = contextWithBrokerOnBehalfOf(oboCtx, BrokerOnBehalfOf{Broker: broker, BrokerID: brokerID})
	oboCtx = contextWithIdentity(oboCtx, f.user)

	oboRequest := func(ctx context.Context, action Action) AuthzRequest {
		return AuthzRequest{
			Principal:  PrincipalContext{Identity: f.user},
			Credential: CredentialContext{Kind: CredentialKindBroker, ID: brokerID, Type: "broker"},
			Resource:   f.agentRes,
			Action:     action,
		}
	}

	actions := ResourceActions["agent"]

	refStore := newMemoTestStore(s)
	refAuthz, _ := newRecordingAuthz(refStore)
	var refPlain, refObo []Decision
	for _, a := range actions {
		refPlain = append(refPlain, refAuthz.CheckAccess(context.Background(), f.user, f.agentRes, a))
		refObo = append(refObo, refAuthz.Decide(oboCtx, oboRequest(oboCtx, a)))
	}

	candStore := newMemoTestStore(s)
	candAuthz, _ := newRecordingAuthz(candStore)
	mctx := withAuthzInputMemo(context.Background())
	moboCtx := withAuthzInputMemo(oboCtx)
	var candPlain, candObo []Decision
	for _, a := range actions {
		candPlain = append(candPlain, candAuthz.CheckAccess(mctx, f.user, f.agentRes, a))
		candObo = append(candObo, candAuthz.Decide(moboCtx, oboRequest(moboCtx, a)))
	}

	for i := range actions {
		assertDecisionsEqual(t, refPlain[i], candPlain[i], "plain tuple %d", i)

		// The on-behalf-of decision differs from plain only in the
		// credential-identifying fields; zero those before comparing, since
		// they are expected to differ (broker credential vs interactive).
		refObo[i].CredentialKind, candObo[i].CredentialKind = "", ""
		refObo[i].CredentialID, candObo[i].CredentialID = "", ""
		refObo[i].CredentialType, candObo[i].CredentialType = "", ""
		assertDecisionsEqual(t, refObo[i], candObo[i], "on-behalf-of tuple %d", i)

		// And the on-behalf-of decision's AUTHORIZATION outcome (Allowed,
		// Reason, DenyCause — not the credential metadata) must match the
		// plain user's, since the principal and its memoized inputs are
		// identical; only the credential bookkeeping differs.
		assert.Equal(t, refPlain[i].Allowed, refObo[i].Allowed, "tuple %d: on-behalf-of must authorize exactly like the plain user", i)
		assert.Equal(t, refPlain[i].Reason, refObo[i].Reason, "tuple %d", i)
	}

	// mctx and moboCtx are two SEPARATE memo installs in this test (one per
	// withAuthzInputMemo call), which is deliberate: it proves plain vs
	// on-behalf-of parity independent of memo sharing. The count assertion
	// below uses one shared memo to prove the sharing claim itself.
	sharedStore := newMemoTestStore(s)
	sharedAuthz, _ := newRecordingAuthz(sharedStore)
	sharedCtx := withAuthzInputMemo(context.Background())
	sharedAuthz.CheckAccess(sharedCtx, f.user, f.agentRes, ActionRead)
	// Reuse the SAME installed memo ctx for the on-behalf-of call by
	// deriving it from sharedCtx (which already carries the memo), not from
	// a fresh background ctx, so both calls land in the same entry.
	sharedOboOnSameMemo := contextWithBrokerIdentity(sharedCtx, broker)
	sharedOboOnSameMemo = contextWithBrokerOnBehalfOf(sharedOboOnSameMemo, BrokerOnBehalfOf{Broker: broker, BrokerID: brokerID})
	sharedOboOnSameMemo = contextWithIdentity(sharedOboOnSameMemo, f.user)
	sharedAuthz.Decide(sharedOboOnSameMemo, oboRequest(sharedOboOnSameMemo, ActionRead))
	assert.Equal(t, 1, sharedStore.countOf("GetEffectiveGroups"), "plain and on-behalf-of decisions for the same user must share one memo entry (same principalKey, credential-independent)")
}
