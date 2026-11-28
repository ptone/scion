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
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/durablequeue"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

type memoryAnalyticsSink struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (m *memoryAnalyticsSink) Emit(_ context.Context, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	m.payloads = append(m.payloads, cp)
	return nil
}

func (m *memoryAnalyticsSink) Events(t *testing.T) []telemetrycontract.UsageBudgetEvent {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]telemetrycontract.UsageBudgetEvent, 0, len(m.payloads))
	for _, raw := range m.payloads {
		var ev telemetrycontract.UsageBudgetEvent
		require.NoError(t, json.Unmarshal(raw, &ev))
		out = append(out, ev)
	}
	return out
}

type budgetTestDispatcher struct {
	mockDispatcher
	mu          sync.Mutex
	stoppedIDs  []string
	messagedIDs []string
	messages    map[string][]string
}

func newBudgetTestDispatcher() *budgetTestDispatcher {
	return &budgetTestDispatcher{
		messages: make(map[string][]string),
	}
}

func (d *budgetTestDispatcher) DispatchAgentStop(_ context.Context, agent *store.Agent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stoppedIDs = append(d.stoppedIDs, agent.ID)
	return nil
}

func (d *budgetTestDispatcher) DispatchAgentMessage(_ context.Context, agent *store.Agent, msg string, _ bool, _ *messages.StructuredMessage) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.messagedIDs = append(d.messagedIDs, agent.ID)
	d.messages[agent.ID] = append(d.messages[agent.ID], msg)
	return nil
}

func (d *budgetTestDispatcher) StoppedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.stoppedIDs))
	copy(out, d.stoppedIDs)
	return out
}

func (d *budgetTestDispatcher) MessagedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.messagedIDs))
	copy(out, d.messagedIDs)
	return out
}

func setBudgetBreachTestExperiments(t *testing.T, srv *Server, ledgerOn, budgetsOn, neverSuspendOn bool) {
	t.Helper()
	var defs []experiments.Experiment
	for _, e := range srv.experimentRegistry().All() {
		switch e.Name {
		case experiments.TokenomicsUsageLedger:
			e.Default = ledgerOn
		case experiments.TokenomicsTokenBudgets:
			e.Default = budgetsOn
		case experiments.NeverSuspend:
			e.Default = neverSuspendOn
		}
		defs = append(defs, e)
	}
	reg, err := experiments.NewRegistry(defs, nil)
	require.NoError(t, err)
	srv.experiments = reg
}

func setupBudgetBreachServer(t *testing.T, now time.Time) (*Server, store.Store, *BudgetBreachHandler, *budgetTestDispatcher, *memoryAnalyticsSink) {
	t.Helper()
	srv, s := testServer(t)
	setBudgetBreachTestExperiments(t, srv, true, true, true)

	ctx := context.Background()
	seedTokenBudgetLimitDefinitions(ctx, s)

	disp := newBudgetTestDispatcher()
	srv.SetDispatcher(disp)

	sink := &memoryAnalyticsSink{}
	srv.SetLifecycleEventsSink(sink, nil)

	eval := srv.TokenBudgetEvaluator()
	require.NotNil(t, eval)
	eval.nowFn = func() time.Time { return now }

	h := srv.BudgetBreachHandler()
	require.NotNil(t, h)
	h.nowFn = func() time.Time { return now }
	h.gracePeriod = 10 * time.Millisecond

	return srv, s, h, disp, sink
}

func TestTokenBudgetEvaluator_50_80_100_AndConcurrentCrossings(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	s, _, eval := setupBudgetEvalTest(t, now)

	projID := uuid.NewString()
	dayDef, err := s.GetLimitDefinitionByName(ctx, store.LimitLLMTokensPerProjectDay)
	require.NoError(t, err)
	_, err = s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
		LimitDefinitionID: dayDef.ID,
		SubjectType:       store.EntitlementSubjectSystemDefault,
		ScopeType:         store.QuotaScopeProject,
		ScopeID:           projID,
		Value:             1000,
	})
	require.NoError(t, err)

	var mu sync.Mutex
	var crossings []BudgetThresholdCrossing
	eval.SetThresholdHook(func(_ context.Context, c BudgetThresholdCrossing) {
		mu.Lock()
		crossings = append(crossings, c)
		mu.Unlock()
	})

	// 1. 40% -> ok, no crossing.
	ev1 := ingestTestTokens(t, s, projID, nil, now, 400, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev1})
	require.Empty(t, crossings)

	// 2. Concurrent evaluators racing at 55% -> exactly ONE 50% crossing.
	ev2 := ingestTestTokens(t, s, projID, nil, now, 150, false)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev2})
		}()
	}
	wg.Wait()
	require.Len(t, crossings, 1)
	assert.Equal(t, store.UsageBudgetStateWarn, crossings[0].NewState)
	assert.Equal(t, store.UsageBudgetThresholdBit50, crossings[0].CrossedBits)

	// 3. Concurrent evaluators racing at 85% (warn -> warn) -> exactly ONE 80% crossing.
	ev3 := ingestTestTokens(t, s, projID, nil, now, 300, false)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev3})
		}()
	}
	wg.Wait()
	require.Len(t, crossings, 2)
	assert.Equal(t, store.UsageBudgetStateWarn, crossings[1].NewState)
	assert.Equal(t, store.UsageBudgetThresholdBit80, crossings[1].CrossedBits)

	// 4. Concurrent evaluators racing at 105% (warn -> exhausted) -> exactly ONE 100% crossing.
	ev4 := ingestTestTokens(t, s, projID, nil, now, 200, false)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev4})
		}()
	}
	wg.Wait()
	require.Len(t, crossings, 3)
	assert.Equal(t, store.UsageBudgetStateExhausted, crossings[2].NewState)
	assert.Equal(t, store.UsageBudgetThresholdBit100, crossings[2].CrossedBits)

	// 5. 0 -> 100% single burst on a fresh project produces ONE hook carrying all 3 bits.
	projBurst := uuid.NewString()
	_, err = s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
		LimitDefinitionID: dayDef.ID,
		SubjectType:       store.EntitlementSubjectSystemDefault,
		ScopeType:         store.QuotaScopeProject,
		ScopeID:           projBurst,
		Value:             1000,
	})
	require.NoError(t, err)
	crossings = nil
	evBurst := ingestTestTokens(t, s, projBurst, nil, now, 1200, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{evBurst})
	require.Len(t, crossings, 1)
	assert.Equal(t, store.UsageBudgetStateExhausted, crossings[0].NewState)
	assert.Equal(t,
		store.UsageBudgetThresholdBit50|store.UsageBudgetThresholdBit80|store.UsageBudgetThresholdBit100,
		crossings[0].CrossedBits,
	)
}

func TestBudgetBreach_NotificationsDedupCapRaiseAndOverride(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	srv, s, h, _, _ := setupBudgetBreachServer(t, now)
	eval := srv.TokenBudgetEvaluator()

	userID := uuid.NewString()
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "billing@example.com", DisplayName: "Billing User", Role: store.UserRoleMember, Status: store.UserStatusActive,
	}))
	projID := uuid.NewString()
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projID, Name: "p-dedup", Slug: "p-dedup"}))

	userDayDef, err := s.GetLimitDefinitionByName(ctx, store.LimitLLMTokensPerUserDay)
	require.NoError(t, err)
	binding, err := s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
		LimitDefinitionID: userDayDef.ID,
		SubjectType:       store.EntitlementSubjectUser,
		SubjectID:         userID,
		ScopeType:         store.QuotaScopeSystem,
		Value:             1000,
	})
	require.NoError(t, err)

	// Cross 50% (550/1000) -> 1 notification to billing user.
	ev1 := ingestTestTokens(t, s, projID, tbStrPtr(userID), now, 550, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev1})
	h.Wait()

	notifs, err := s.GetNotifications(ctx, store.SubscriberTypeUser, userID, false)
	require.NoError(t, err)
	require.Len(t, notifs, 1)
	assert.Equal(t, store.NotificationKindBudgetThreshold, notifs[0].Kind)
	assert.Equal(t, store.NotificationStatusBudgetWarn50, notifs[0].Status)

	// Concurrent duplicate hook invocations for 80% -> CAS dedup ensures exactly 1 notification.
	ev2 := ingestTestTokens(t, s, projID, tbStrPtr(userID), now, 300, false) // 850/1000
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev2})
	h.Wait()

	// Also race 6 direct OnThresholdCrossing calls for the same 80% crossing -> still 0 extra sends.
	dayStart, dayEnd := utcDayWindow(now)
	stRow, err := s.GetUsageBudgetState(ctx, store.UsageBudgetStateKey{
		LimitDefinitionID: userDayDef.ID,
		ScopeKind:         store.UsageScopeKindUser,
		ScopeID:           userID,
		WindowStartUTC:    dayStart,
	})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.OnThresholdCrossing(ctx, BudgetThresholdCrossing{
				StateID:        stRow.ID,
				LimitName:      store.LimitLLMTokensPerUserDay,
				LimitDefID:     userDayDef.ID,
				ScopeKind:      store.UsageScopeKindUser,
				ScopeID:        userID,
				WindowStartUTC: dayStart,
				WindowEndUTC:   dayEnd,
				PrevState:      store.UsageBudgetStateWarn,
				NewState:       store.UsageBudgetStateWarn,
				CrossedBits:    store.UsageBudgetThresholdBit80,
				Consumed:       850,
				Cap:            1000,
				TokenAction:    store.TokenActionPauseNew,
				StateVersion:   stRow.StateVersion,
			})
		}()
	}
	wg.Wait()
	h.Wait()

	notifs, err = s.GetNotifications(ctx, store.SubscriberTypeUser, userID, false)
	require.NoError(t, err)
	require.Len(t, notifs, 2)

	// Cap raise (1000 -> 2000): 850/2000 = 42.5% -> A2 re-eval clears 50% and 80% bits.
	binding.Value = 2000
	binding, err = s.UpdateEntitlementBinding(ctx, binding)
	require.NoError(t, err)
	eval.ReevaluateAfterEntitlementChange(ctx, userDayDef, binding)
	h.Wait()

	// Re-cross 50% under new cap (850 + 200 = 1050/2000) -> notifies again!
	ev3 := ingestTestTokens(t, s, projID, tbStrPtr(userID), now, 200, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev3})
	h.Wait()

	notifs, err = s.GetNotifications(ctx, store.SubscriberTypeUser, userID, false)
	require.NoError(t, err)
	require.Len(t, notifs, 3, "re-crossing after cap raise must notify again")

	// Cross 100% (1050 + 1000 = 2050/2000) -> 4th notification.
	ev4 := ingestTestTokens(t, s, projID, tbStrPtr(userID), now, 1000, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev4})
	h.Wait()

	notifs, err = s.GetNotifications(ctx, store.SubscriberTypeUser, userID, false)
	require.NoError(t, err)
	require.Len(t, notifs, 4)

	// Admin override does NOT clear thresholds_notified; after override expires, no re-notification!
	stRow, err = s.GetUsageBudgetState(ctx, store.UsageBudgetStateKey{
		LimitDefinitionID: userDayDef.ID,
		ScopeKind:         store.UsageScopeKindUser,
		ScopeID:           userID,
		WindowStartUTC:    dayStart,
	})
	require.NoError(t, err)
	overrideUntil := now.Add(1 * time.Hour)
	_, err = eval.OverrideBudgetState(ctx, stRow, &overrideUntil, stRow.StateVersion)
	require.NoError(t, err)

	// Advance clock past override expiry and ingest 10 more tokens -> state returns to exhausted, NO new notification.
	later := now.Add(2 * time.Hour)
	eval.nowFn = func() time.Time { return later }
	h.nowFn = func() time.Time { return later }
	ev5 := ingestTestTokens(t, s, projID, tbStrPtr(userID), later, 10, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev5})
	h.Wait()

	notifs, err = s.GetNotifications(ctx, store.SubscriberTypeUser, userID, false)
	require.NoError(t, err)
	assert.Len(t, notifs, 4, "override expiry must not re-notify when thresholds_notified bit is already set")
}

func TestBudgetBreach_RecipientsPerScopeKind(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	srv, s, h, _, _ := setupBudgetBreachServer(t, now)
	eval := srv.TokenBudgetEvaluator()

	// Create users.
	mkUser := func(email string) string {
		u := &store.User{
			ID: uuid.NewString(), Email: email, DisplayName: email,
			Role: store.UserRoleMember, Status: store.UserStatusActive,
		}
		require.NoError(t, s.CreateUser(ctx, u))
		return u.ID
	}
	projOwner := mkUser("powner@example.com")
	projAdmin := mkUser("padmin@example.com")
	projMember := mkUser("pmember@example.com")
	grpOwner := mkUser("gowner@example.com")
	grpAdmin := mkUser("gadmin@example.com")
	grpPlain := mkUser("gplain@example.com")
	grpNestedUser := mkUser("gnested@example.com")

	// Project with owner, admin, member.
	projID := uuid.NewString()
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projID, Name: "p-recip", Slug: "p-recip"}))
	bindProjRole := func(uid, roleName string) {
		roleDef, err := s.GetRoleDefinitionByName(ctx, roleName, store.RoleScopeProject)
		require.NoError(t, err)
		_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
			ID:               uuid.NewString(),
			RoleDefinitionID: roleDef.ID,
			PrincipalType:    store.PolicyPrincipalTypeUser,
			PrincipalID:      uid,
			ScopeType:        store.RoleScopeProject,
			ScopeID:          projID,
		})
		require.NoError(t, err)
	}
	bindProjRole(projOwner, store.ProjectRoleOwner)
	bindProjRole(projAdmin, store.ProjectRoleAdmin)
	bindProjRole(projMember, store.ProjectRoleMember)

	projDayDef, err := s.GetLimitDefinitionByName(ctx, store.LimitLLMTokensPerProjectDay)
	require.NoError(t, err)
	_, err = s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
		LimitDefinitionID: projDayDef.ID,
		SubjectType:       store.EntitlementSubjectSystemDefault,
		ScopeType:         store.QuotaScopeProject,
		ScopeID:           projID,
		Value:             1000,
	})
	require.NoError(t, err)

	// Project 50% crossing -> owner + admin notified, plain member NOT notified.
	evProj := ingestTestTokens(t, s, projID, nil, now, 600, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{evProj})
	h.Wait()

	nOwner, err := s.GetNotifications(ctx, store.SubscriberTypeUser, projOwner, false)
	require.NoError(t, err)
	assert.Len(t, nOwner, 1)
	nAdmin, err := s.GetNotifications(ctx, store.SubscriberTypeUser, projAdmin, false)
	require.NoError(t, err)
	assert.Len(t, nAdmin, 1)
	nMember, err := s.GetNotifications(ctx, store.SubscriberTypeUser, projMember, false)
	require.NoError(t, err)
	assert.Empty(t, nMember)

	// Pooled group with owner, admin, plain member, and nested child group user.
	parentGrp := &store.Group{ID: uuid.NewString(), Name: "Parent Pool", Slug: "parent-pool", GroupType: store.GroupTypeExplicit}
	childGrp := &store.Group{ID: uuid.NewString(), Name: "Child Pool", Slug: "child-pool", GroupType: store.GroupTypeExplicit}
	require.NoError(t, s.CreateGroup(ctx, parentGrp))
	require.NoError(t, s.CreateGroup(ctx, childGrp))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{GroupID: parentGrp.ID, MemberType: store.GroupMemberTypeUser, MemberID: grpOwner, Role: store.GroupMemberRoleOwner}))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{GroupID: parentGrp.ID, MemberType: store.GroupMemberTypeUser, MemberID: grpAdmin, Role: store.GroupMemberRoleAdmin}))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{GroupID: parentGrp.ID, MemberType: store.GroupMemberTypeUser, MemberID: grpPlain, Role: store.GroupMemberRoleMember}))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{GroupID: parentGrp.ID, MemberType: store.GroupMemberTypeGroup, MemberID: childGrp.ID, Role: store.GroupMemberRoleMember}))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{GroupID: childGrp.ID, MemberType: store.GroupMemberTypeUser, MemberID: grpNestedUser, Role: store.GroupMemberRoleMember}))

	// Verify recipient resolution directly for group soft (50%, 80%) vs hard (100%) + cap truncation.
	softRecip, err := h.resolveNotificationRecipients(ctx, BudgetThresholdCrossing{
		ScopeKind: store.UsageScopeKindGroup,
		ScopeID:   parentGrp.ID,
	}, 80)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{grpOwner, grpAdmin}, softRecip, "soft threshold notifies only group owner + admins")

	hardRecip, err := h.resolveNotificationRecipients(ctx, BudgetThresholdCrossing{
		ScopeKind: store.UsageScopeKindGroup,
		ScopeID:   parentGrp.ID,
	}, 100)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{grpOwner, grpAdmin, grpPlain, grpNestedUser}, hardRecip, "100% notifies owner, admin, plain and nested effective members")

	// Fan-out cap preserves owners + admins first and truncates excess.
	h.maxGroupNotifyRecipients = 3
	capped, err := h.resolveNotificationRecipients(ctx, BudgetThresholdCrossing{
		ScopeKind: store.UsageScopeKindGroup,
		ScopeID:   parentGrp.ID,
	}, 100)
	require.NoError(t, err)
	assert.Len(t, capped, 3)
	assert.Contains(t, capped, grpOwner)
	assert.Contains(t, capped, grpAdmin)
}

func createBudgetTestRunningAgent(
	t *testing.T,
	s store.Store,
	projID string,
	slug string,
	billingUserID *string,
	runID string,
	generation int,
	isShell bool,
	neverSuspend bool,
) *store.Agent {
	t.Helper()
	ctx := context.Background()
	a := &store.Agent{
		ID:              uuid.NewString(),
		Slug:            slug,
		Name:            slug,
		ProjectID:       projID,
		RuntimeBrokerID: "broker-1",
		Phase:           string(state.PhaseRunning),
		RunIntent:       store.RunIntentRunning,
		RunID:           runID,
		StateVersion:    1,
		AppliedConfig:   &store.AgentAppliedConfig{HarnessConfig: "claude"},
	}
	if isShell {
		a.HarnessConfig = "shell"
		a.AppliedConfig.HarnessConfig = "shell"
	}
	if neverSuspend {
		a.SuspendPolicy = store.SuspendPolicyNever
	}
	require.NoError(t, s.CreateAgent(ctx, a))
	_, err := s.SetAgentRunID(ctx, a.ID, runID, nil)
	require.NoError(t, err)
	_, err = s.EnsureRunAttribution(ctx, &store.RunAttribution{
		RunID:         runID,
		Generation:    generation,
		HubID:         "hub-test",
		ProjectID:     projID,
		AgentID:       a.ID,
		AgentSlug:     a.Slug,
		BillingUserID: billingUserID,
		Harness:       "claude",
	})
	require.NoError(t, err)
	return a
}

func TestBudgetBreach_SuspendAndPauseSweepAndReverify(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	srv, s, h, disp, _ := setupBudgetBreachServer(t, now)
	eval := srv.TokenBudgetEvaluator()

	// Ensure durableQueue is wired.
	require.NotNil(t, srv.durableQueue)

	adminUID := uuid.NewString()
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: adminUID, Email: "padmin-sweep@example.com", DisplayName: "Admin", Role: store.UserRoleMember, Status: store.UserStatusActive,
	}))
	projID := uuid.NewString()
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projID, Name: "p-sweep", Slug: "p-sweep"}))
	adminRole, err := s.GetRoleDefinitionByName(ctx, store.ProjectRoleAdmin, store.RoleScopeProject)
	require.NoError(t, err)
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		ID:               uuid.NewString(),
		RoleDefinitionID: adminRole.ID,
		PrincipalType:    store.PolicyPrincipalTypeUser,
		PrincipalID:      adminUID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          projID,
	})
	require.NoError(t, err)

	// Create 4 running agents in the project:
	// 1. normal LLM agent (should be noticed + suspended)
	// 2. principal-less LLM agent (in scope for project budget -> should be noticed + suspended)
	// 3. shell agent (exempt per D8 -> untouched)
	// 4. never_suspend LLM agent (skipped per provisional D10, listed in exhausted notification)
	agentNormal := createBudgetTestRunningAgent(t, s, projID, "llm-normal", tbStrPtr(adminUID), "run-norm-1", 1, false, false)
	agentAnon := createBudgetTestRunningAgent(t, s, projID, "llm-anon", nil, "run-anon-1", 1, false, false)
	agentShell := createBudgetTestRunningAgent(t, s, projID, "shell-exempt", tbStrPtr(adminUID), "run-shell-1", 1, true, false)
	agentNever := createBudgetTestRunningAgent(t, s, projID, "llm-never", tbStrPtr(adminUID), "run-never-1", 1, false, true)

	projDayDef, err := s.GetLimitDefinitionByName(ctx, store.LimitLLMTokensPerProjectDay)
	require.NoError(t, err)
	_, err = s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
		LimitDefinitionID: projDayDef.ID,
		SubjectType:       store.EntitlementSubjectSystemDefault,
		ScopeType:         store.QuotaScopeProject,
		ScopeID:           projID,
		Value:             1000,
		TokenAction:       tbStrPtr(store.TokenActionSuspendAndPause),
	})
	require.NoError(t, err)

	// Trigger 100% exhaustion.
	ev := ingestTestTokens(t, s, projID, tbStrPtr(adminUID), now, 1200, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev})
	h.Wait()

	// Running-agent notices sent only to normal + anon (not shell, not never_suspend).
	assert.ElementsMatch(t, []string{agentNormal.ID, agentAnon.ID}, disp.MessagedIDs())

	// D10: never_suspend agent counted and listed in exhausted notification.
	assert.Equal(t, int64(1), h.NeverSuspendSkipped())
	notifs, err := s.GetNotifications(ctx, store.SubscriberTypeUser, adminUID, false)
	require.NoError(t, err)
	require.Len(t, notifs, 1)
	assert.Contains(t, notifs[0].Message, "not suspended (never_suspend): llm-never")

	// Advance clock past grace period and claim/process queued durablequeue items.
	afterGrace := now.Add(h.gracePeriod + time.Second)
	h.nowFn = func() time.Time { return afterGrace }
	eval.nowFn = func() time.Time { return afterGrace }

	dqs, ok := s.(store.DurableQueueStore)
	require.True(t, ok)
	items, err := dqs.ClaimDurableItems(ctx, usageBudgetSuspendQueueKind, "test-hub", afterGrace, afterGrace.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, items, 2)

	for _, it := range items {
		dqItem := durablequeue.Item{
			Seq:       it.Seq,
			Kind:      it.Kind,
			DedupKey:  it.DedupKey,
			Payload:   it.Payload,
			NotBefore: it.NotBefore,
		}
		require.NoError(t, h.HandleSuspendQueueItem(ctx, dqItem))
		// Processing the same item a second time is idempotent (agent already suspended).
		require.NoError(t, h.HandleSuspendQueueItem(ctx, dqItem))
	}

	assert.ElementsMatch(t, []string{agentNormal.ID, agentAnon.ID}, disp.StoppedIDs())

	gotNormal, err := s.GetAgent(ctx, agentNormal.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.PhaseSuspended), gotNormal.Phase)
	assert.Equal(t, string(state.SuspendReasonBudgetExhausted), gotNormal.SuspendReason)

	gotAnon, err := s.GetAgent(ctx, agentAnon.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.PhaseSuspended), gotAnon.Phase)
	assert.Equal(t, string(state.SuspendReasonBudgetExhausted), gotAnon.SuspendReason)

	gotShell, err := s.GetAgent(ctx, agentShell.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.PhaseRunning), gotShell.Phase)

	gotNever, err := s.GetAgent(ctx, agentNever.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.PhaseRunning), gotNever.Phase)
}

func TestBudgetBreach_SuspendReverifySkips(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	dayStart, dayEnd := utcDayWindow(now)

	cases := []struct {
		name  string
		setup func(t *testing.T, srv *Server, s store.Store, h *BudgetBreachHandler, binding *store.EntitlementBinding, stRow *store.UsageBudgetState, agent *store.Agent)
	}{
		{
			name: "override_added_during_grace",
			setup: func(t *testing.T, srv *Server, _ store.Store, _ *BudgetBreachHandler, _ *store.EntitlementBinding, stRow *store.UsageBudgetState, _ *store.Agent) {
				until := now.Add(2 * time.Hour)
				_, err := srv.TokenBudgetEvaluator().OverrideBudgetState(ctx, stRow, &until, stRow.StateVersion)
				require.NoError(t, err)
			},
		},
		{
			name: "cap_raised_during_grace",
			setup: func(t *testing.T, _ *Server, s store.Store, _ *BudgetBreachHandler, binding *store.EntitlementBinding, _ *store.UsageBudgetState, _ *store.Agent) {
				binding.Value = 5000
				_, err := s.UpdateEntitlementBinding(ctx, binding)
				require.NoError(t, err)
			},
		},
		{
			name: "action_changed_to_pause_new",
			setup: func(t *testing.T, _ *Server, s store.Store, _ *BudgetBreachHandler, binding *store.EntitlementBinding, _ *store.UsageBudgetState, _ *store.Agent) {
				binding.TokenAction = tbStrPtr(store.TokenActionPauseNew)
				_, err := s.UpdateEntitlementBinding(ctx, binding)
				require.NoError(t, err)
			},
		},
		{
			name: "window_rolled_over",
			setup: func(t *testing.T, _ *Server, _ store.Store, h *BudgetBreachHandler, _ *store.EntitlementBinding, _ *store.UsageBudgetState, _ *store.Agent) {
				h.nowFn = func() time.Time { return dayEnd.Add(time.Minute) }
			},
		},
		{
			name: "agent_restarted_new_run_id",
			setup: func(t *testing.T, _ *Server, s store.Store, _ *BudgetBreachHandler, _ *store.EntitlementBinding, _ *store.UsageBudgetState, agent *store.Agent) {
				_, err := s.SetAgentRunID(ctx, agent.ID, "run-replacement-2", nil)
				require.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, s, h, disp, _ := setupBudgetBreachServer(t, now)
			projID := uuid.NewString()
			require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projID, Name: "p-" + tc.name, Slug: "p-" + tc.name}))
			agent := createBudgetTestRunningAgent(t, s, projID, "worker-"+tc.name, nil, "run-orig-1", 1, false, false)

			dayDef, err := s.GetLimitDefinitionByName(ctx, store.LimitLLMTokensPerProjectDay)
			require.NoError(t, err)
			binding, err := s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
				LimitDefinitionID: dayDef.ID,
				SubjectType:       store.EntitlementSubjectSystemDefault,
				ScopeType:         store.QuotaScopeProject,
				ScopeID:           projID,
				Value:             1000,
				TokenAction:       tbStrPtr(store.TokenActionSuspendAndPause),
			})
			require.NoError(t, err)

			stRow, err := s.CreateUsageBudgetState(ctx, &store.UsageBudgetState{
				LimitDefinitionID: dayDef.ID,
				ScopeKind:         store.UsageScopeKindProject,
				ScopeID:           projID,
				WindowStartUTC:    dayStart,
				WindowEndUTC:      dayEnd,
				State:             store.UsageBudgetStateExhausted,
				ConsumedAtEval:    1200,
				CapAtEval:         1000,
				StateVersion:      1,
			})
			require.NoError(t, err)

			payload := BudgetSuspendPayload{
				CrossingID:     fmt.Sprintf("%s:%d", stRow.ID, stRow.StateVersion),
				StateID:        stRow.ID,
				LimitName:      store.LimitLLMTokensPerProjectDay,
				LimitDefID:     dayDef.ID,
				ScopeKind:      store.UsageScopeKindProject,
				ScopeID:        projID,
				WindowStartUTC: dayStart,
				WindowEndUTC:   dayEnd,
				Cap:            1000,
				TokenAction:    store.TokenActionSuspendAndPause,
				AgentID:        agent.ID,
				RunID:          "run-orig-1",
				Generation:     1,
			}
			raw, err := json.Marshal(payload)
			require.NoError(t, err)

			tc.setup(t, srv, s, h, binding, stRow, agent)

			require.NoError(t, h.HandleSuspendQueueItem(ctx, durablequeue.Item{
				Kind:    usageBudgetSuspendQueueKind,
				Payload: raw,
			}))
			assert.Empty(t, disp.StoppedIDs(), "agent must not be suspended when %s", tc.name)
			got, err := s.GetAgent(ctx, agent.ID)
			require.NoError(t, err)
			assert.Equal(t, string(state.PhaseRunning), got.Phase)
		})
	}
}

func TestBudgetBreach_AnalyticsEventAndOTelGauge(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	srv, s, h, _, sink := setupBudgetBreachServer(t, now)
	eval := srv.TokenBudgetEvaluator()

	projID := uuid.NewString()
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projID, Name: "p-obs", Slug: "p-obs"}))
	dayDef, err := s.GetLimitDefinitionByName(ctx, store.LimitLLMTokensPerProjectDay)
	require.NoError(t, err)
	_, err = s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
		LimitDefinitionID: dayDef.ID,
		SubjectType:       store.EntitlementSubjectSystemDefault,
		ScopeType:         store.QuotaScopeProject,
		ScopeID:           projID,
		Value:             1000,
		TokenAction:       tbStrPtr(store.TokenActionPauseNew),
	})
	require.NoError(t, err)

	// Cross 100% -> usage.budget analytics event emitted.
	ev := ingestTestTokens(t, s, projID, nil, now, 1100, false)
	eval.EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev})
	h.Wait()

	events := sink.Events(t)
	require.Len(t, events, 1)
	assert.Equal(t, telemetrycontract.EventTypeUsageBudget, events[0].EventType)
	assert.Equal(t, store.LimitLLMTokensPerProjectDay, events[0].LimitName)
	assert.Equal(t, store.UsageScopeKindProject, events[0].ScopeKind)
	assert.Equal(t, projID, events[0].ScopeID)
	assert.Equal(t, 100, events[0].ThresholdPct)
	assert.Equal(t, store.UsageBudgetStateExhausted, events[0].NewState)

	// Register UsageOTelMetrics and collect scion_hub_budget_state gauge (D11).
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	require.NoError(t, srv.RegisterUsageOTelMetrics(mp, withUsageOTelClock(func() time.Time { return now }), withUsageOTelCacheTTL(0)))
	t.Cleanup(func() { _ = srv.UsageOTelMetrics().Close() })

	rm := collectUsageOTelMetrics(t, reader)
	m := findUsageOTelMetric(rm, MetricHubBudgetState)
	require.NotNil(t, m, "expected %s gauge metric", MetricHubBudgetState)
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	require.True(t, ok)
	require.Len(t, gauge.DataPoints, 1)
	assert.Equal(t, int64(1), gauge.DataPoints[0].Value)
	attrs := usageOTelAttrMap(gauge.DataPoints[0].Attributes)
	assert.Equal(t, store.UsageScopeKindProject, attrs["scope_kind"])
	assert.Equal(t, store.LimitLLMTokensPerProjectDay, attrs["limit"])
	assert.Equal(t, store.UsageBudgetStateExhausted, attrs["state"])

	// When tokenomics.token_budgets is disabled, gauge emits no data points.
	setBudgetBreachTestExperiments(t, srv, true, false, true)
	rmOff := collectUsageOTelMetrics(t, reader)
	assert.Nil(t, findUsageOTelMetric(rmOff, MetricHubBudgetState))
}

func TestBudgetBreach_ExperimentOffNoOp(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	srv, s, h, disp, sink := setupBudgetBreachServer(t, now)
	setBudgetBreachTestExperiments(t, srv, true, false, true)

	projID := uuid.NewString()
	require.NoError(t, s.CreateProject(ctx, &store.Project{ID: projID, Name: "p-off", Slug: "p-off"}))
	_ = createBudgetTestRunningAgent(t, s, projID, "worker-off", nil, "run-off-1", 1, false, false)

	dayDef, err := s.GetLimitDefinitionByName(ctx, store.LimitLLMTokensPerProjectDay)
	require.NoError(t, err)
	_, err = s.CreateEntitlementBinding(ctx, &store.EntitlementBinding{
		LimitDefinitionID: dayDef.ID,
		SubjectType:       store.EntitlementSubjectSystemDefault,
		ScopeType:         store.QuotaScopeProject,
		ScopeID:           projID,
		Value:             1000,
		TokenAction:       tbStrPtr(store.TokenActionSuspendAndPause),
	})
	require.NoError(t, err)

	ev := ingestTestTokens(t, s, projID, nil, now, 1500, false)
	srv.TokenBudgetEvaluator().EvaluateIngestedEvents(ctx, []*store.UsageEvent{ev})
	h.Wait()

	assert.Empty(t, sink.Events(t))
	assert.Empty(t, disp.MessagedIDs())
	assert.Empty(t, disp.StoppedIDs())
}
