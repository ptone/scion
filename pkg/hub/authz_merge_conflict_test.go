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
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/auditevent"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the finite merge compatibility contract. Their fixtures are
// not admission evidence, a production target census, or approval of any T entry.
func TestAuthzConflictResolution_OperationFreeBearerInputs(t *testing.T) {
	for _, field := range []string{"OperationID", "AlwaysAudit"} {
		_, present := reflect.TypeOf(authorizationEvaluationRequest{}).FieldByName(field)
		require.False(t, present, "evaluation cannot carry ordinary audit ownership: %s", field)
	}

	f := newBearerFixture(t, "merge-inputs")
	ctx := context.Background()
	counting := &rpaStore{Store: f.store, getUserCalls: map[string]int{}}
	authz := NewAuthzService(counting, slog.Default())
	emitter := &recordingDecisionAuditEmitter{}
	authz.SetDecisionAuditEmitter(emitter)
	memo := NewProjectAdmissionCache()
	run := &bearerGateRun{memo: memo}
	scoped := NewScopedUserIdentityWithBoundaryAndDecoration(bearerUser(f.ownerA), projectBoundary(f.projectA), []string{"agent:list"}, tid("merge-inputs-cred"), bearerCeiling(t, "agent:list"), nil)
	request := AuthzRequest{
		Principal: PrincipalContext{Identity: scoped},
		Resource:  Resource{Type: "agent", ParentType: "project", ParentID: f.projectA},
		Action:    ActionList, Permission: "agent.list",
		TargetEvidence: projectCollectionEvidence(f.projectA, "agent.list"),
		bearerRun:      run, AlwaysAudit: true,
	}
	input := authorizationEvaluationFromRequest(request)
	require.Equal(t, request.TargetEvidence, input.TargetEvidence)
	require.Same(t, run, input.bearerRun)
	require.Same(t, memo, input.bearerRun.memoOrNil())

	decision := authz.decide(ctx, input)
	require.True(t, decision.Allowed, decision.Reason)
	require.Equal(t, bearerStagePassed, run.trace.Stage)
	reads := counting.calls(f.ownerA)
	require.Positive(t, reads, "the first admission must observe the store")
	require.NotEmpty(t, memo.cache, "the supplied memo must be populated")
	second := authz.decide(ctx, input)
	require.Equal(t, decision.Allowed, second.Allowed)
	require.Equal(t, decision.Reason, second.Reason)
	assert.Equal(t, reads, counting.calls(f.ownerA), "the same successful admission uses the supplied memo")
	assert.Empty(t, emitter.records, "direct evaluation has no ordinary audit owner")

	request.TargetEvidence = projectCollectionEvidence(f.projectA, "agent.create")
	denied := authz.decide(ctx, authorizationEvaluationFromRequest(request))
	require.False(t, denied.Allowed)
	require.Equal(t, bearerReasonTargetUnknown, denied.Reason)
	assert.Empty(t, emitter.records)
	ordinary := authz.Decide(ctx, request)
	require.Equal(t, denied.Allowed, ordinary.Allowed)
	require.Equal(t, denied.Reason, ordinary.Reason)
	assert.Len(t, emitter.records, 1, "only the ordinary wrapper owns one record")

	emitter.records = nil
	eval := authz.EvaluateBearerCeiling(ctx, PrincipalContext{Identity: bearerUser(f.ownerA)}, projectBoundary(f.projectA), bearerCeiling(t, "agent:list"), "agent.list", input.Resource, BearerOptions{Evidence: input.TargetEvidence, Memo: memo})
	require.True(t, eval.Decision.Allowed, eval.Decision.Reason)
	assert.Empty(t, emitter.records, "the bearer callable remains non-emitting")
	var nilRun *bearerGateRun
	assert.Nil(t, nilRun.memoOrNil())
	assert.Nil(t, nilRun.traceOrNil())
}

func TestAuthzConflictResolution_FaultAuditReasons(t *testing.T) {
	ctx := context.Background()
	t.Run("constraint fault", func(t *testing.T) {
		_, s := authzTestSetup(t)
		id := tid("merge-constraint-user")
		require.NoError(t, s.CreateUser(ctx, &store.User{ID: id, Email: id + "@test.com", DisplayName: "User", Role: "member", Status: "active"}))
		authz := NewAuthzService(&failConstraintsStore{Store: s, failErr: errors.New("test-local constraint fault")}, slog.Default())
		d := authz.CheckAccess(ctx, bearerUser(id), Resource{Type: "agent", ID: tid("merge-constraint-agent"), OwnerID: id}, ActionAttach)
		require.False(t, d.Allowed)
		require.Equal(t, "relationship grant restricted by access_constraint_error", d.Reason)
		require.Equal(t, DenyCauseResolutionError, d.DenyCause)
		assert.Equal(t, auditevent.ReasonDependencyUnavailable, d.AuditReason)
	})
	t.Run("relationship project lookup fault", func(t *testing.T) {
		f := newRPAFixture(t, "merge-rel-fault")
		id := tid("merge-rel-member")
		uatpMember(t, f.store, f.projectID, id)
		agent := uatpAgent(t, f.store, f.projectID, id, "merge-rel-agent", f.ownerID)
		before := f.evaluate(t, rpaInteractive, id, agentResource(agent), "agent.attach", RelationshipRuleOwner)
		require.True(t, before.decision.Allowed, before.decision.Reason)
		f.counting.setFault(id)
		after := f.evaluate(t, rpaInteractive, id, agentResource(agent), "agent.attach", RelationshipRuleOwner)
		assertProjectAccessDeny(t, rpaInteractive, after, RelationshipRuleOwner, RelationshipRejectProjectAccessError)
		require.False(t, after.decision.Allowed)
		require.Equal(t, DenyCauseResolutionError, after.decision.DenyCause)
		assert.Equal(t, auditevent.ReasonDependencyUnavailable, after.decision.AuditReason)
	})
	for _, fault := range []bool{false, true} {
		name := "bearer policy denial"
		if fault {
			name = "bearer lookup fault"
		}
		t.Run(name, func(t *testing.T) {
			f := newBearerFixture(t, "merge-"+name)
			counting := &rpaStore{Store: f.store, getUserCalls: map[string]int{}}
			authz := NewAuthzService(counting, slog.Default())
			boundary := projectBoundary(f.projectA)
			target := agentResource(f.agentB)
			wantReason := bearerReasonOutsideProject
			wantAudit := auditevent.ReasonPolicyDenied
			if fault {
				boundary = hubBoundary()
				target = agentResource(f.agentA)
				counting.setFault(f.ownerA)
				wantReason = bearerReasonProjectAccessDenied
				wantAudit = auditevent.ReasonDependencyUnavailable
			}
			scoped := NewScopedUserIdentityWithBoundaryAndDecoration(bearerUser(f.ownerA), boundary, []string{"agent:delete"}, tid("merge-bearer-cred"), bearerCeiling(t, "agent:delete"), nil)
			principal := principalContextForIdentity(scoped)
			helper := authz.enforceUATConstraints(ctx, principal, scoped, target, ActionDelete, "agent.delete")
			require.NotNil(t, helper)
			require.False(t, helper.Allowed)
			require.Equal(t, wantReason, helper.Reason)
			assert.Equal(t, wantAudit, helper.AuditReason)
			ordinary := authz.Decide(ctx, AuthzRequest{Principal: PrincipalContext{Identity: scoped}, Resource: target, Action: ActionDelete, Permission: "agent.delete"})
			require.False(t, ordinary.Allowed)
			require.Equal(t, helper.Reason, ordinary.Reason)
			require.Equal(t, helper.DenyCause, ordinary.DenyCause)
			assert.Equal(t, fault, ordinary.IsIndeterminate())
			assert.Equal(t, wantAudit, ordinary.AuditReason)
		})
	}
}

func TestAuthzConflictResolution_UATDelegationBoundaryAndFault(t *testing.T) {
	f := newBearerFixture(t, "merge-delegation")
	ctx := context.Background()
	project := NewScopedUserIdentityWithBoundaryAndDecoration(bearerUser(f.ownerA), projectBoundary(f.projectA), []string{"project:manage"}, tid("merge-project-cred"), bearerCeiling(t, "project:manage"), nil)
	hub := NewScopedUserIdentityWithBoundaryAndDecoration(bearerUser(f.ownerA), hubBoundary(), []string{"project:manage"}, tid("merge-hub-cred"), bearerCeiling(t, "project:manage"), nil)
	grant := GrantDescriptor{Type: GrantTypeRoleBinding, ScopeType: store.RoleScopeProject, ScopeID: f.projectA}
	cases := []struct {
		name       string
		scoped     *ScopedUserIdentity
		grant      GrantDescriptor
		fault      bool
		wantReason string
		wantAudit  auditevent.ReasonCode
	}{
		{"missing credential", nil, grant, false, "scoped credential is missing", auditevent.ReasonPolicyDenied},
		{"system grant", project, GrantDescriptor{ScopeType: store.RoleScopeSystem}, false, "scoped credential cannot create system-scoped grants", auditevent.ReasonPolicyDenied},
		{"unknown scope", project, GrantDescriptor{ScopeType: "test-unknown"}, false, "scoped credential cannot delegate a grant with an unknown scope type", auditevent.ReasonPolicyDenied},
		{"missing project", project, GrantDescriptor{ScopeType: store.RoleScopeProject}, false, "scoped credential cannot delegate a project grant that names no project", auditevent.ReasonPolicyDenied},
		{"two projects", hub, GrantDescriptor{Type: GrantTypeAgentDelegation, ScopeType: store.RoleScopeProject, ScopeID: f.projectA, ProjectID: f.projectB}, false, "scoped credential cannot delegate an agent grant that names two projects", auditevent.ReasonPolicyDenied},
		{"outside project", project, GrantDescriptor{Type: GrantTypeRoleBinding, ScopeType: store.RoleScopeProject, ScopeID: f.projectB}, false, "scoped credential cannot delegate outside its project", auditevent.ReasonPolicyDenied},
		{"unmapped project grant", hub, GrantDescriptor{Type: GrantTypeCustomRole, ScopeType: store.RoleScopeProject, ScopeID: f.projectA}, false, "scoped credential cannot delegate this grant type into a project", auditevent.ReasonPolicyDenied},
		{"holder has no access", hub, GrantDescriptor{Type: GrantTypeRoleBinding, ScopeType: store.RoleScopeProject, ScopeID: f.projectB}, false, "scoped credential requires current access to the grant's project", auditevent.ReasonPolicyDenied},
		{"lookup fault", hub, grant, true, "scoped credential requires current access to the grant's project", auditevent.ReasonDependencyUnavailable},
		{"project boundary passes", project, grant, false, "", ""},
		{"hub boundary with live access passes", hub, grant, false, "", ""},
		{"no-scope group proceeds to later checks", project, GrantDescriptor{Type: GrantTypeGroupMembership}, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counting := &rpaStore{Store: f.store, getUserCalls: map[string]int{}}
			if tc.fault {
				counting.setFault(f.ownerA)
			}
			authz := NewAuthzService(counting, slog.Default())
			d := authz.enforceUATDelegation(ctx, tc.scoped, tc.grant)
			if tc.wantReason == "" {
				require.Nil(t, d)
				return
			}
			require.NotNil(t, d)
			require.False(t, d.Allowed)
			require.Equal(t, tc.wantReason, d.Reason)
			assert.Equal(t, tc.wantAudit, d.AuditReason)
		})
	}
}
