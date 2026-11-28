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

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/durablequeue"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetryemit"
	"github.com/GoogleCloudPlatform/scion/pkg/version"
)

const (
	// usageBudgetSuspendQueueKind is the durablequeue kind for idempotent
	// graceful suspend_and_pause actions keyed by (crossing_id, run_id).
	usageBudgetSuspendQueueKind = "usage.budget_suspend"

	// defaultBudgetSuspendGrace is the grace period between sending the
	// running-agent notice and dispatching the graceful broker suspend.
	defaultBudgetSuspendGrace = 30 * time.Second

	// defaultMaxBudgetGroupNotifyRecipients caps pooled-group 100% fan-out to
	// avoid unbounded notification writes on very large groups (owners + admins
	// are always included first).
	defaultMaxBudgetGroupNotifyRecipients = 1000

	// budgetBreachAsyncConcurrency bounds concurrent async threshold-crossing
	// handlers spawned from BudgetThresholdHook so ingest bursts cannot spawn
	// unbounded goroutines.
	budgetBreachAsyncConcurrency = 32

	// budgetBreachTaskTimeout bounds one async threshold-crossing handler.
	budgetBreachTaskTimeout = 30 * time.Second

	// budgetNoticeDispatchTimeout bounds one best-effort running-agent notice.
	budgetNoticeDispatchTimeout = 5 * time.Second

	// budgetAnalyticsEmitTimeout bounds one best-effort usage.budget analytics
	// sink emit.
	budgetAnalyticsEmitTimeout = 5 * time.Second

	// budgetAgentListPageSize bounds each ListAgents page during sweep discovery.
	budgetAgentListPageSize = 200
)

// BudgetSuspendPayload is the JSON payload persisted in durable_queue_items for
// usage.budget_suspend. Keyed by DedupKey = "budget-suspend:<crossing_id>:<run_id>"
// so retries and multi-replica hubs remain idempotent.
type BudgetSuspendPayload struct {
	CrossingID     string    `json:"crossing_id"`
	StateID        string    `json:"state_id"`
	LimitName      string    `json:"limit_name"`
	LimitDefID     string    `json:"limit_def_id"`
	ScopeKind      string    `json:"scope_kind"`
	ScopeID        string    `json:"scope_id"`
	ProjectScopeID string    `json:"project_scope_id,omitempty"`
	WindowStartUTC time.Time `json:"window_start_utc"`
	WindowEndUTC   time.Time `json:"window_end_utc"`
	Cap            int64     `json:"cap"`
	TokenAction    string    `json:"token_action"`
	AgentID        string    `json:"agent_id"`
	RunID          string    `json:"run_id"`
	Generation     int       `json:"generation"`
}

// BudgetBreachHandler consumes TokenBudgetEvaluator's BudgetThresholdHook to
// execute P3.2 breach side effects outside the ingest hot path:
//  1. Deduplicated soft (50%, 80%) and hard (100%) user notifications via
//     CAS on usage_budget_state.thresholds_notified.
//  2. Idempotent suspend_and_pause sweep via durablequeue (with running-agent
//     notice, ~30s grace period, and full pre-dispatch re-verification).
//  3. usage.budget analytics event emission.
type BudgetBreachHandler struct {
	server       *Server
	store        store.Store
	quota        *QuotaService
	pooledGroups *PooledGroupResolver
	queue        durablequeue.Queue
	logger       *slog.Logger
	enabledFn    func() bool
	nowFn        func() time.Time

	// gracePeriod is the delay between running-agent notice and suspend
	// dispatch (defaults to 30s; configurable in tests).
	gracePeriod time.Duration

	// respectNeverSuspend implements provisional decision D10: when true,
	// agents with an active never_suspend policy are skipped during
	// suspend_and_pause sweeps, logged+counted, and listed as
	// "not suspended (never_suspend)" in the exhausted notification.
	// Kept as a single check so it can be flipped cleanly if D10 changes.
	respectNeverSuspend bool

	// maxGroupNotifyRecipients caps pooled-group 100% notification fan-out.
	maxGroupNotifyRecipients int

	// analyticsSinkFn returns the active scion-analytics telemetryemit.Sink,
	// if configured.
	analyticsSinkFn func() telemetryemit.Sink

	neverSuspendSkipped atomic.Int64
	analyticsErrors     atomic.Int64
	notifyErrors        atomic.Int64

	sem chan struct{}
	wg  sync.WaitGroup
}

// NewBudgetBreachHandler constructs a BudgetBreachHandler wired to srv.
func NewBudgetBreachHandler(srv *Server, q durablequeue.Queue, logger *slog.Logger) *BudgetBreachHandler {
	if logger == nil {
		logger = slog.Default()
	}
	var st store.Store
	var qs *QuotaService
	var pg *PooledGroupResolver
	if srv != nil {
		st = srv.store
		qs = srv.quotaService
		pg = srv.pooledGroupResolver
	}
	return &BudgetBreachHandler{
		server:                   srv,
		store:                    st,
		quota:                    qs,
		pooledGroups:             pg,
		queue:                    q,
		logger:                   logger,
		gracePeriod:              defaultBudgetSuspendGrace,
		respectNeverSuspend:      true,
		maxGroupNotifyRecipients: defaultMaxBudgetGroupNotifyRecipients,
		enabledFn: func() bool {
			return srv != nil &&
				srv.experimentEnabled(experiments.TokenomicsUsageLedger) &&
				srv.experimentEnabled(experiments.TokenomicsTokenBudgets)
		},
		nowFn: func() time.Time { return time.Now().UTC() },
		analyticsSinkFn: func() telemetryemit.Sink {
			if srv == nil {
				return nil
			}
			srv.lifecycleEvents.mu.Lock()
			sink := srv.lifecycleEvents.sink
			srv.lifecycleEvents.mu.Unlock()
			return sink
		},
		sem: make(chan struct{}, budgetBreachAsyncConcurrency),
	}
}

func (h *BudgetBreachHandler) enabled() bool {
	if h == nil || h.store == nil || h.server == nil {
		return false
	}
	if h.enabledFn == nil {
		return true
	}
	return h.enabledFn()
}

func (h *BudgetBreachHandler) now() time.Time {
	if h != nil && h.nowFn != nil {
		return h.nowFn().UTC()
	}
	return time.Now().UTC()
}

func (h *BudgetBreachHandler) shouldSkipNeverSuspend(agent *store.Agent) bool {
	return h != nil && h.respectNeverSuspend && h.server != nil && h.server.neverSuspendActive(agent)
}

// NeverSuspendSkipped returns the cumulative count of running agents skipped
// during suspend_and_pause sweeps due to an active never_suspend policy (D10).
func (h *BudgetBreachHandler) NeverSuspendSkipped() int64 {
	if h == nil {
		return 0
	}
	return h.neverSuspendSkipped.Load()
}

// Wait blocks until all in-flight asynchronous threshold-crossing tasks finish.
func (h *BudgetBreachHandler) Wait() {
	if h == nil {
		return
	}
	h.wg.Wait()
}

// WaitContext blocks until all in-flight asynchronous threshold-crossing tasks
// finish or ctx is cancelled.
func (h *BudgetBreachHandler) WaitContext(ctx context.Context) error {
	if h == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OnThresholdCrossing is the BudgetThresholdHook callback. It never blocks the
// ingest request: it hands off asynchronously to a bounded background task
// detached from the caller's request cancellation.
func (h *BudgetBreachHandler) OnThresholdCrossing(ctx context.Context, crossing BudgetThresholdCrossing) {
	if !h.enabled() {
		return
	}
	baseCtx := context.Background()
	if h.server != nil {
		baseCtx = h.server.lifetimeCtx()
	}
	if baseCtx.Err() != nil {
		return
	}

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		select {
		case h.sem <- struct{}{}:
			defer func() { <-h.sem }()
		case <-baseCtx.Done():
			return
		}
		taskCtx, cancel := context.WithTimeout(baseCtx, budgetBreachTaskTimeout)
		defer cancel()
		h.handleCrossing(taskCtx, crossing)
	}()
}

func crossingIDFor(c BudgetThresholdCrossing) string {
	if c.StateID != "" {
		return fmt.Sprintf("%s:%d", c.StateID, c.StateVersion)
	}
	return fmt.Sprintf("%s:%s:%s:%s:%d:%d",
		c.LimitDefID, c.ScopeKind, c.ScopeID, c.ProjectScopeID,
		c.WindowStartUTC.UTC().Unix(), c.StateVersion)
}

func highestThresholdPct(bits int64) int {
	switch {
	case bits&store.UsageBudgetThresholdBit100 != 0:
		return 100
	case bits&store.UsageBudgetThresholdBit80 != 0:
		return 80
	case bits&store.UsageBudgetThresholdBit50 != 0:
		return 50
	default:
		return 0
	}
}

func budgetNotificationStatusForPct(pct int) string {
	switch pct {
	case 100:
		return store.NotificationStatusBudgetExhausted
	case 80:
		return store.NotificationStatusBudgetWarn80
	default:
		return store.NotificationStatusBudgetWarn50
	}
}

func (h *BudgetBreachHandler) handleCrossing(ctx context.Context, crossing BudgetThresholdCrossing) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("token budget breach handler panicked; recovered",
				"limit", crossing.LimitName,
				"scope_kind", crossing.ScopeKind,
				"scope_id", crossing.ScopeID,
				"panic", r,
				"stack", string(debug.Stack()))
		}
	}()

	if !h.enabled() {
		return
	}

	crossedBits := crossing.CrossedBits
	if crossedBits == 0 {
		crossedBits = budgetThresholdBitsForRatio(crossing.Consumed, crossing.Cap)
	}
	if crossedBits == 0 {
		return
	}
	highestPct := highestThresholdPct(crossedBits)
	if highestPct == 0 {
		return
	}

	// 1. Emit usage.budget analytics event once per crossing.
	h.emitUsageBudgetEvent(ctx, crossing, highestPct)

	// 2. On exhaustion with suspend_and_pause, discover running agents in scope,
	// send running-agent notices, and enqueue idempotent durablequeue suspend
	// items (recording any never_suspend-skipped slugs so the exhausted
	// notification can list them per provisional D10).
	var skippedNeverSuspendSlugs []string
	if crossing.NewState == store.UsageBudgetStateExhausted &&
		crossing.TokenAction == store.TokenActionSuspendAndPause {
		skippedNeverSuspendSlugs = h.enqueueSuspendSweep(ctx, crossing)
	}

	// 3. Deduplicated threshold notifications via CAS on thresholds_notified.
	// Bit claim happens in the same step immediately before sending; a process
	// crash between CAS-claim and notification write is the only loss mode.
	h.claimAndSendNotifications(ctx, crossing, crossedBits, skippedNeverSuspendSlugs)
}

func (h *BudgetBreachHandler) emitUsageBudgetEvent(ctx context.Context, crossing BudgetThresholdCrossing, thresholdPct int) {
	if h.analyticsSinkFn == nil {
		return
	}
	sink := h.analyticsSinkFn()
	if sink == nil {
		return
	}

	now := h.now()
	hubID := ""
	if h.server != nil {
		hubID = h.server.HubID()
	}
	producerNS := hubID
	if producerNS == "" {
		producerNS = "scion-hub"
	}
	cid := crossingIDFor(crossing)
	env := telemetrycontract.NewEnvelope(telemetrycontract.EventTypeUsageBudget, cid, producerNS, now, now)
	env.HubID = hubID
	env.ProducerBuild = version.Short()
	switch crossing.ScopeKind {
	case store.UsageScopeKindProject:
		env.ProjectID = crossing.ScopeID
	case store.UsageScopeKindUserProject, store.UsageScopeKindGroup:
		if crossing.ProjectScopeID != "" {
			env.ProjectID = crossing.ProjectScopeID
		}
	}

	ev := &telemetrycontract.UsageBudgetEvent{
		Envelope:       env,
		CrossingID:     cid,
		StateID:        crossing.StateID,
		LimitName:      crossing.LimitName,
		LimitDefID:     crossing.LimitDefID,
		ScopeKind:      crossing.ScopeKind,
		ScopeID:        crossing.ScopeID,
		ProjectScopeID: crossing.ProjectScopeID,
		WindowStartUTC: telemetrycontract.FormatTS(crossing.WindowStartUTC),
		WindowEndUTC:   telemetrycontract.FormatTS(crossing.WindowEndUTC),
		PrevState:      crossing.PrevState,
		NewState:       crossing.NewState,
		ThresholdPct:   thresholdPct,
		Consumed:       crossing.Consumed,
		Cap:            crossing.Cap,
		TokenAction:    crossing.TokenAction,
		StateVersion:   crossing.StateVersion,
	}

	payload, err := telemetrycontract.Marshal(ev)
	if err != nil {
		h.analyticsErrors.Add(1)
		h.logger.Warn("failed to marshal usage.budget analytics event",
			"crossing_id", cid, "error", err)
		return
	}

	emitCtx, cancel := context.WithTimeout(ctx, budgetAnalyticsEmitTimeout)
	defer cancel()
	if err := sink.Emit(emitCtx, payload); err != nil {
		h.analyticsErrors.Add(1)
		h.logger.Warn("failed to emit usage.budget analytics event",
			"crossing_id", cid, "error", err)
	}
}

func (h *BudgetBreachHandler) claimAndSendNotifications(
	ctx context.Context,
	crossing BudgetThresholdCrossing,
	crossedBits int64,
	skippedNeverSuspendSlugs []string,
) {
	var (
		claimedRow  *store.UsageBudgetState
		bitsToClaim int64
	)

	for attempt := 0; attempt < maxBudgetEvalCASRetries; attempt++ {
		var (
			row *store.UsageBudgetState
			err error
		)
		if crossing.StateID != "" {
			row, err = h.store.GetUsageBudgetStateByID(ctx, crossing.StateID)
		} else {
			row, err = h.store.GetUsageBudgetState(ctx, store.UsageBudgetStateKey{
				LimitDefinitionID: crossing.LimitDefID,
				ScopeKind:         crossing.ScopeKind,
				ScopeID:           crossing.ScopeID,
				ProjectScopeID:    crossing.ProjectScopeID,
				WindowStartUTC:    crossing.WindowStartUTC,
			})
		}
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				h.notifyErrors.Add(1)
				h.logger.Warn("failed to load usage_budget_state for notification claim",
					"limit", crossing.LimitName, "scope_kind", crossing.ScopeKind, "scope_id", crossing.ScopeID, "error", err)
			}
			return
		}
		now := h.now()
		if !row.WindowEndUTC.After(now) {
			return
		}
		if row.OverrideUntil != nil && row.OverrideUntil.After(now) {
			return
		}

		bitsToClaim = crossedBits &^ row.ThresholdsNotified
		if bitsToClaim == 0 {
			// Already notified (another concurrent crossing won, or an override
			// preserved thresholds_notified without clearing bits).
			return
		}

		candidate := *row
		candidate.ThresholdsNotified = row.ThresholdsNotified | bitsToClaim
		swapped, swapErr := h.store.CompareAndSwapUsageBudgetState(ctx, row.StateVersion, &candidate)
		if swapErr != nil {
			if errors.Is(swapErr, store.ErrBudgetStateConflict) {
				continue
			}
			h.notifyErrors.Add(1)
			h.logger.Warn("failed to CAS thresholds_notified before sending budget notification",
				"state_id", row.ID, "error", swapErr)
			return
		}
		claimedRow = swapped
		break
	}
	if claimedRow == nil || bitsToClaim == 0 {
		return
	}

	pct := highestThresholdPct(bitsToClaim)
	if pct == 0 {
		return
	}

	recipients, err := h.resolveNotificationRecipients(ctx, crossing, pct)
	if err != nil {
		h.notifyErrors.Add(1)
		h.logger.Warn("failed to resolve budget notification recipients",
			"limit", crossing.LimitName, "scope_kind", crossing.ScopeKind, "scope_id", crossing.ScopeID, "error", err)
		return
	}
	if len(recipients) == 0 {
		return
	}

	msg := formatBudgetThresholdNotificationMessage(crossing, pct, skippedNeverSuspendSlugs)
	status := budgetNotificationStatusForPct(pct)
	projectID := ""
	switch crossing.ScopeKind {
	case store.UsageScopeKindProject:
		projectID = crossing.ScopeID
	case store.UsageScopeKindUserProject, store.UsageScopeKindGroup:
		projectID = crossing.ProjectScopeID
	}

	for _, uid := range recipients {
		if err := h.server.writeUserNotification(ctx, userNotice{
			Kind:             store.NotificationKindBudgetThreshold,
			ResourceType:     store.NotificationResourceBudget,
			ResourceID:       claimedRow.ID,
			Status:           status,
			ProjectID:        projectID,
			SubscriberUserID: uid,
			Message:          msg,
		}); err != nil {
			h.notifyErrors.Add(1)
			h.logger.Warn("failed to write budget threshold notification",
				"state_id", claimedRow.ID, "subscriber_id", uid, "threshold_pct", pct, "error", err)
		}
	}
}

func (h *BudgetBreachHandler) resolveNotificationRecipients(
	ctx context.Context,
	crossing BudgetThresholdCrossing,
	thresholdPct int,
) ([]string, error) {
	switch crossing.ScopeKind {
	case store.UsageScopeKindUser, store.UsageScopeKindUserProject:
		if crossing.ScopeID == "" {
			return nil, nil
		}
		return []string{crossing.ScopeID}, nil

	case store.UsageScopeKindProject:
		members, err := h.store.ListProjectMembers(ctx, crossing.ScopeID)
		if err != nil {
			return nil, fmt.Errorf("list project members: %w", err)
		}
		seen := make(map[string]struct{}, len(members))
		out := make([]string, 0, len(members))
		for _, m := range members {
			if m == nil || m.UserID == "" {
				continue
			}
			if m.Role != store.ProjectRoleOwner && m.Role != store.ProjectRoleAdmin {
				continue
			}
			if _, dup := seen[m.UserID]; dup {
				continue
			}
			seen[m.UserID] = struct{}{}
			out = append(out, m.UserID)
		}
		sort.Strings(out)
		return out, nil

	case store.UsageScopeKindGroup:
		return h.resolveGroupNotificationRecipients(ctx, crossing.ScopeID, thresholdPct)

	default:
		return nil, nil
	}
}

func (h *BudgetBreachHandler) resolveGroupNotificationRecipients(
	ctx context.Context,
	groupID string,
	thresholdPct int,
) ([]string, error) {
	if groupID == "" {
		return nil, nil
	}
	now := h.now()
	capLimit := h.maxGroupNotifyRecipients
	if capLimit <= 0 {
		capLimit = defaultMaxBudgetGroupNotifyRecipients
	}

	directMembers, err := h.store.GetGroupMembers(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("get group members: %w", err)
	}

	// Always include group owner + admins first (for both soft and hard thresholds).
	seen := make(map[string]struct{}, len(directMembers))
	admins := make([]string, 0, len(directMembers))
	plainUsers := make([]string, 0, len(directMembers))
	var childGroups []string

	for _, m := range directMembers {
		if m.ExpiresAt != nil && !m.ExpiresAt.After(now) {
			continue
		}
		switch m.MemberType {
		case store.GroupMemberTypeUser:
			if m.MemberID == "" {
				continue
			}
			if m.Role == store.GroupMemberRoleOwner || m.Role == store.GroupMemberRoleAdmin {
				if _, dup := seen[m.MemberID]; !dup {
					seen[m.MemberID] = struct{}{}
					admins = append(admins, m.MemberID)
				}
			} else if thresholdPct >= 100 {
				plainUsers = append(plainUsers, m.MemberID)
			}
		case store.GroupMemberTypeGroup:
			if thresholdPct >= 100 && m.MemberID != "" {
				childGroups = append(childGroups, m.MemberID)
			}
		}
	}

	sort.Strings(admins)
	if thresholdPct < 100 {
		if len(admins) > capLimit {
			h.logger.Warn("budget group notification recipients truncated at cap",
				"group_id", groupID, "threshold_pct", thresholdPct, "total", len(admins), "cap", capLimit)
			return admins[:capLimit], nil
		}
		return admins, nil
	}

	// At 100%, also include plain direct members and effective user members from
	// nested child groups (cycle-safe BFS, skipping expired memberships).
	visitedGroups := map[string]struct{}{groupID: {}}
	for len(childGroups) > 0 {
		gid := childGroups[0]
		childGroups = childGroups[1:]
		if _, visited := visitedGroups[gid]; visited {
			continue
		}
		visitedGroups[gid] = struct{}{}

		subMembers, subErr := h.store.GetGroupMembers(ctx, gid)
		if subErr != nil {
			h.logger.Warn("failed to load nested group members for budget notification",
				"parent_group_id", groupID, "child_group_id", gid, "error", subErr)
			continue
		}
		for _, sm := range subMembers {
			if sm.ExpiresAt != nil && !sm.ExpiresAt.After(now) {
				continue
			}
			switch sm.MemberType {
			case store.GroupMemberTypeUser:
				if sm.MemberID != "" {
					plainUsers = append(plainUsers, sm.MemberID)
				}
			case store.GroupMemberTypeGroup:
				if sm.MemberID != "" {
					if _, visited := visitedGroups[sm.MemberID]; !visited {
						childGroups = append(childGroups, sm.MemberID)
					}
				}
			}
		}
	}

	dedupPlain := make([]string, 0, len(plainUsers))
	for _, uid := range plainUsers {
		if _, dup := seen[uid]; dup {
			continue
		}
		seen[uid] = struct{}{}
		dedupPlain = append(dedupPlain, uid)
	}
	sort.Strings(dedupPlain)

	out := make([]string, 0, len(admins)+len(dedupPlain))
	out = append(out, admins...)
	out = append(out, dedupPlain...)
	if len(out) > capLimit {
		h.logger.Warn("budget group notification recipients truncated at cap",
			"group_id", groupID, "threshold_pct", thresholdPct, "total", len(out), "cap", capLimit)
		out = out[:capLimit]
	}
	return out, nil
}

func formatBudgetThresholdNotificationMessage(
	crossing BudgetThresholdCrossing,
	thresholdPct int,
	skippedNeverSuspendSlugs []string,
) string {
	scopeLabel := fmt.Sprintf("%s %s", crossing.ScopeKind, crossing.ScopeID)
	if crossing.ProjectScopeID != "" {
		scopeLabel = fmt.Sprintf("%s %s (project %s)", crossing.ScopeKind, crossing.ScopeID, crossing.ProjectScopeID)
	}
	var msg string
	if thresholdPct >= 100 {
		msg = fmt.Sprintf(
			"Token budget exhausted (100%%): %s for %s reached %d/%d tokens (action: %s, resets %s)",
			crossing.LimitName, scopeLabel, crossing.Consumed, crossing.Cap,
			crossing.TokenAction, crossing.WindowEndUTC.UTC().Format(time.RFC3339),
		)
		if len(skippedNeverSuspendSlugs) > 0 {
			msg += fmt.Sprintf("; not suspended (never_suspend): %s", strings.Join(skippedNeverSuspendSlugs, ", "))
		}
	} else {
		msg = fmt.Sprintf(
			"Token budget warning (%d%%): %s for %s reached %d/%d tokens (resets %s)",
			thresholdPct, crossing.LimitName, scopeLabel, crossing.Consumed, crossing.Cap,
			crossing.WindowEndUTC.UTC().Format(time.RFC3339),
		)
	}
	return msg
}

type budgetSuspendCandidate struct {
	agent      store.Agent
	generation int
}

// enqueueSuspendSweep enumerates running agents in scope of an exhausted
// suspend_and_pause budget, sends a best-effort running-agent notice, and
// enqueues an idempotent durablequeue item keyed by (crossing_id, run_id)
// delayed by h.gracePeriod (~30s). Returns the sorted slugs of any running
// agents skipped due to never_suspend (provisional D10).
func (h *BudgetBreachHandler) enqueueSuspendSweep(ctx context.Context, crossing BudgetThresholdCrossing) []string {
	now := h.now()
	if !crossing.WindowEndUTC.After(now) {
		return nil
	}

	// Verify the state row is still exhausted and not under an active override
	// before sending notices or enqueuing suspends.
	if crossing.StateID != "" {
		row, err := h.store.GetUsageBudgetStateByID(ctx, crossing.StateID)
		if err != nil {
			return nil
		}
		if row.State != store.UsageBudgetStateExhausted ||
			(row.OverrideUntil != nil && row.OverrideUntil.After(now)) {
			return nil
		}
	}

	candidates, err := h.findRunningAgentsInScope(ctx, crossing)
	if err != nil {
		h.logger.Warn("token budget suspend sweep failed to enumerate agents in scope",
			"limit", crossing.LimitName, "scope_kind", crossing.ScopeKind, "scope_id", crossing.ScopeID, "error", err)
		return nil
	}
	if len(candidates) == 0 {
		return nil
	}

	cid := crossingIDFor(crossing)
	dispatcher := h.server.GetDispatcher()
	notBefore := now.Add(h.gracePeriod)
	noticeMsg := fmt.Sprintf(
		"Token budget exhausted (%s: %d/%d tokens). This agent will be gracefully suspended in %s; please wrap up and save your work.",
		crossing.LimitName, crossing.Consumed, crossing.Cap, h.gracePeriod.Truncate(time.Second),
	)

	var skippedNeverSuspend []string
	enqueuedAny := false
	for _, cand := range candidates {
		a := cand.agent
		if h.shouldSkipNeverSuspend(&a) {
			h.neverSuspendSkipped.Add(1)
			h.logger.Info("token budget suspend sweep: skipping agent with never_suspend policy",
				"agent_id", a.ID, "agent_slug", a.Slug, "run_id", a.RunID,
				"limit", crossing.LimitName, "crossing_id", cid)
			label := a.Slug
			if label == "" {
				label = a.ID
			}
			skippedNeverSuspend = append(skippedNeverSuspend, label)
			continue
		}

		// Best-effort running-agent notice before the grace period elapses.
		if dispatcher != nil && a.RuntimeBrokerID != "" {
			noticeCtx, cancel := context.WithTimeout(ctx, budgetNoticeDispatchTimeout)
			if err := dispatcher.DispatchAgentMessage(noticeCtx, &a, noticeMsg, false, nil); err != nil {
				h.logger.Warn("token budget suspend sweep: failed to send running-agent notice",
					"agent_id", a.ID, "run_id", a.RunID, "error", err)
			}
			cancel()
		}

		if h.queue == nil {
			continue
		}
		payload := BudgetSuspendPayload{
			CrossingID:     cid,
			StateID:        crossing.StateID,
			LimitName:      crossing.LimitName,
			LimitDefID:     crossing.LimitDefID,
			ScopeKind:      crossing.ScopeKind,
			ScopeID:        crossing.ScopeID,
			ProjectScopeID: crossing.ProjectScopeID,
			WindowStartUTC: crossing.WindowStartUTC.UTC(),
			WindowEndUTC:   crossing.WindowEndUTC.UTC(),
			Cap:            crossing.Cap,
			TokenAction:    crossing.TokenAction,
			AgentID:        a.ID,
			RunID:          a.RunID,
			Generation:     cand.generation,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			h.logger.Warn("token budget suspend sweep: marshal payload failed",
				"agent_id", a.ID, "error", err)
			continue
		}
		dedupKey := fmt.Sprintf("budget-suspend:%s:%s", cid, a.RunID)
		_, err = h.queue.Enqueue(ctx, nil, durablequeue.Item{
			Kind:      usageBudgetSuspendQueueKind,
			DedupKey:  dedupKey,
			Payload:   raw,
			NotBefore: notBefore,
		})
		if err != nil && !errors.Is(err, durablequeue.ErrDuplicate) {
			h.logger.Warn("token budget suspend sweep: enqueue failed",
				"agent_id", a.ID, "run_id", a.RunID, "dedup_key", dedupKey, "error", err)
			continue
		}
		enqueuedAny = true
	}

	if enqueuedAny && h.queue != nil {
		h.queue.Wake(usageBudgetSuspendQueueKind)
	}
	sort.Strings(skippedNeverSuspend)
	return skippedNeverSuspend
}

func (h *BudgetBreachHandler) findRunningAgentsInScope(
	ctx context.Context,
	crossing BudgetThresholdCrossing,
) ([]budgetSuspendCandidate, error) {
	filter := store.AgentFilter{
		Phase: string(state.PhaseRunning),
	}
	switch crossing.ScopeKind {
	case store.UsageScopeKindProject:
		filter.ProjectID = crossing.ScopeID
	case store.UsageScopeKindUserProject:
		filter.ProjectID = crossing.ProjectScopeID
	case store.UsageScopeKindGroup:
		if crossing.ProjectScopeID != "" {
			filter.ProjectID = crossing.ProjectScopeID
		}
	}

	// Cache effective group membership checks per billing user for group scopes.
	groupMatchCache := map[string]bool{}
	var out []budgetSuspendCandidate
	cursor := ""
	for {
		res, err := h.store.ListAgents(ctx, filter, store.ListOptions{
			Limit:          budgetAgentListPageSize,
			Cursor:         cursor,
			SkipTotalCount: true,
		})
		if err != nil {
			return nil, fmt.Errorf("list running agents: %w", err)
		}
		for i := range res.Items {
			a := res.Items[i]
			if !a.DeletedAt.IsZero() || a.Phase != string(state.PhaseRunning) {
				continue
			}
			// Shell agents are exempt from token budgets (D8).
			if isShellAgent(&a) {
				continue
			}
			if a.RunID == "" {
				continue
			}

			var billingUserID string
			var generation int
			if attr, attrErr := h.store.GetRunAttribution(ctx, a.RunID); attrErr == nil && attr != nil {
				if attr.BillingUserID != nil {
					billingUserID = *attr.BillingUserID
				}
				generation = attr.Generation
			}

			switch crossing.ScopeKind {
			case store.UsageScopeKindProject:
				// Principal-less agents (billingUserID == "") are in scope ONLY for project budgets.
				if a.ProjectID != crossing.ScopeID {
					continue
				}
			case store.UsageScopeKindUser:
				if billingUserID == "" || billingUserID != crossing.ScopeID {
					continue
				}
			case store.UsageScopeKindUserProject:
				if billingUserID == "" || billingUserID != crossing.ScopeID || a.ProjectID != crossing.ProjectScopeID {
					continue
				}
			case store.UsageScopeKindGroup:
				if billingUserID == "" {
					continue
				}
				if crossing.ProjectScopeID != "" && a.ProjectID != crossing.ProjectScopeID {
					continue
				}
				matched, cached := groupMatchCache[billingUserID]
				if !cached {
					gids, gErr := h.store.GetEffectiveGroups(ctx, billingUserID)
					if gErr != nil {
						h.logger.Warn("token budget suspend sweep: failed to resolve effective groups for user",
							"user_id", billingUserID, "error", gErr)
						continue
					}
					for _, gid := range gids {
						if gid == crossing.ScopeID {
							matched = true
							break
						}
					}
					groupMatchCache[billingUserID] = matched
				}
				if !matched {
					continue
				}
			default:
				continue
			}

			out = append(out, budgetSuspendCandidate{
				agent:      a,
				generation: generation,
			})
		}

		if res.NextCursor == "" || len(res.Items) == 0 {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// HandleSuspendQueueItem processes one usage.budget_suspend durablequeue item.
// It re-verifies every precondition (experiment, window, policy cap/action,
// budget state & override, agent phase/run_id/generation/shell/never_suspend)
// before dispatching a graceful broker suspend with suspend_reason=budget_exhausted.
func (h *BudgetBreachHandler) HandleSuspendQueueItem(ctx context.Context, item durablequeue.Item) error {
	if !h.enabled() {
		return nil
	}
	var payload BudgetSuspendPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		h.logger.Warn("invalid usage.budget_suspend payload; dropping", "seq", item.Seq, "dedup_key", item.DedupKey, "error", err)
		return nil
	}

	now := h.now()
	// 1. Window still open.
	if !payload.WindowEndUTC.After(now) {
		h.logger.Info("token budget suspend skipped: window closed",
			"agent_id", payload.AgentID, "run_id", payload.RunID, "limit", payload.LimitName)
		return nil
	}

	// 2. Policy revision unchanged: re-resolve effective (cap, action) strictly
	// and confirm cap is unchanged (> 0) and action is still suspend_and_pause.
	if h.quota == nil {
		return nil
	}
	strictCache := newStrictTokenLimitEvalCache(h.quota)
	bindingScope := store.QuotaScopeSystem
	bindingScopeID := ""
	bindingSubject := ""
	switch payload.ScopeKind {
	case store.UsageScopeKindProject:
		bindingScope = store.QuotaScopeProject
		bindingScopeID = payload.ScopeID
	case store.UsageScopeKindUser:
		bindingSubject = payload.ScopeID
		bindingScope = store.QuotaScopeSystem
	case store.UsageScopeKindUserProject:
		bindingSubject = payload.ScopeID
		bindingScope = store.QuotaScopeProject
		bindingScopeID = payload.ProjectScopeID
	case store.UsageScopeKindGroup:
		if payload.ProjectScopeID != "" {
			bindingScope = store.QuotaScopeProject
			bindingScopeID = payload.ProjectScopeID
		}
	default:
		return nil
	}

	var (
		capNow    int64
		actionNow string
		err       error
	)
	if payload.ScopeKind == store.UsageScopeKindGroup {
		capNow, actionNow, err = strictCache.resolveGroupByID(
			ctx, payload.LimitDefID, payload.ScopeID, bindingScope, bindingScopeID,
		)
	} else {
		capNow, actionNow, err = strictCache.resolveByID(
			ctx, payload.LimitDefID, bindingSubject, bindingScope, bindingScopeID,
		)
	}
	if err != nil {
		return fmt.Errorf("re-verify budget policy: %w", err)
	}
	if capNow <= 0 || capNow != payload.Cap || actionNow != store.TokenActionSuspendAndPause {
		h.logger.Info("token budget suspend skipped: policy changed during grace period",
			"agent_id", payload.AgentID, "run_id", payload.RunID,
			"limit", payload.LimitName, "old_cap", payload.Cap, "new_cap", capNow, "new_action", actionNow)
		return nil
	}

	// 3. Budget state still exhausted and no active override.
	var row *store.UsageBudgetState
	if payload.StateID != "" {
		row, err = h.store.GetUsageBudgetStateByID(ctx, payload.StateID)
	} else {
		row, err = h.store.GetUsageBudgetState(ctx, store.UsageBudgetStateKey{
			LimitDefinitionID: payload.LimitDefID,
			ScopeKind:         payload.ScopeKind,
			ScopeID:           payload.ScopeID,
			ProjectScopeID:    payload.ProjectScopeID,
			WindowStartUTC:    payload.WindowStartUTC,
		})
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("re-verify budget state: %w", err)
	}
	if row.State != store.UsageBudgetStateExhausted ||
		row.ConsumedAtEval < capNow ||
		(row.OverrideUntil != nil && row.OverrideUntil.After(now)) {
		h.logger.Info("token budget suspend skipped: budget no longer exhausted or overridden",
			"agent_id", payload.AgentID, "run_id", payload.RunID, "state", row.State)
		return nil
	}

	// 4. Agent still exists, running, not shell, not never_suspend, and same run_id/generation.
	agent, err := h.store.GetAgent(ctx, payload.AgentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("re-verify agent: %w", err)
	}
	if !agent.DeletedAt.IsZero() || agent.Phase != string(state.PhaseRunning) {
		return nil
	}
	if isShellAgent(agent) {
		return nil
	}
	if h.shouldSkipNeverSuspend(agent) {
		h.neverSuspendSkipped.Add(1)
		h.logger.Info("token budget suspend skipped at dispatch: agent has never_suspend policy",
			"agent_id", agent.ID, "agent_slug", agent.Slug, "run_id", agent.RunID)
		return nil
	}
	if agent.RunID != payload.RunID {
		h.logger.Info("token budget suspend skipped: agent run_id changed (replacement run preserved)",
			"agent_id", agent.ID, "expected_run_id", payload.RunID, "current_run_id", agent.RunID)
		return nil
	}
	if payload.Generation > 0 {
		if attr, attrErr := h.store.GetRunAttribution(ctx, agent.RunID); attrErr == nil && attr != nil && attr.Generation != payload.Generation {
			h.logger.Info("token budget suspend skipped: agent generation changed",
				"agent_id", agent.ID, "expected_gen", payload.Generation, "current_gen", attr.Generation)
			return nil
		}
	}

	// 5. Dispatch graceful broker suspend via Hub's existing broker suspend lifecycle.
	return h.server.budgetSuspendAgent(ctx, agent, h.respectNeverSuspend)
}

// budgetSuspendAgent performs a graceful broker suspend with
// suspend_reason = "budget_exhausted" through the Hub's existing broker suspend
// lifecycle (mirroring Server.autoSuspendAgent in pkg/hub/server.go).
func (s *Server) budgetSuspendAgent(ctx context.Context, agent *store.Agent, respectNeverSuspend bool) error {
	if s == nil || agent == nil {
		return nil
	}
	ctx, cancel := detachStopFromClient(ctx, stopWriteBudget())
	defer cancel()

	if deletionActive(agent) || reincarnationInFlight(agent) {
		slog.Info("Budget suspend: skipping agent with active deletion or reincarnation",
			"agent_id", agent.ID, "agent_name", agent.Name)
		return nil
	}
	endLifecycleOp, ok := s.tryBeginLifecycleOp(agent.ID)
	if !ok {
		return fmt.Errorf("lifecycle operation already in flight for agent %s", agent.ID)
	}
	defer endLifecycleOp()

	dispatcher := s.GetDispatcher()
	var captured *capturedScreen
	if agent.RuntimeBrokerID != "" {
		if dispatcher == nil {
			return fmt.Errorf("cannot budget-suspend agent %s: dispatcher is nil", agent.ID)
		}
		captured = s.preSuspendProbes(ctx, dispatcher, agent, false)
	}

	supersedes := agent.StartClaimID
	preStopRetained := string(state.RetainedSuspendActivity(
		state.Activity(agent.Activity),
		state.Activity(agent.StalledFromActivity),
	))

	priorIntent, intentAt, err := s.swapAutoSuspendRunIntent(ctx, agent, store.RunIntentStopped, respectNeverSuspend)
	if errors.Is(err, store.ErrAgentNeverSuspend) {
		s.clearWorkspaceAtStop(ctx, agent)
		slog.Info("Budget suspend: skipping agent with never_suspend policy",
			"agent_id", agent.ID, "agent_name", agent.Name)
		return nil
	}
	if errors.Is(err, store.ErrIdleSuspendPrecondition) {
		s.clearWorkspaceAtStop(ctx, agent)
		return fmt.Errorf("active idle-suspend claim holds agent %s", agent.ID)
	}
	if err != nil {
		s.clearWorkspaceAtStop(ctx, agent)
		return fmt.Errorf("budget suspend intent write failed: %w", err)
	}

	reason := state.SuspendReasonBudgetExhausted
	endAutoSuspend := s.autoSuspending.begin(agent.ID, reason)
	defer endAutoSuspend()
	stopRunID := agent.RunID

	if agent.RuntimeBrokerID != "" {
		if err := syncDispatch(ctx, func(dctx context.Context) error {
			return dispatcher.DispatchAgentStop(dctx, agent)
		}); err != nil {
			s.logStopRunMismatch(agent, "budget-suspend", err)
			s.clearWorkspaceAtStop(ctx, agent)
			if priorIntent == store.RunIntentRunning {
				if _, rerr := s.store.RevertRunIntent(ctx, agent.ID, store.RunIntentStopped, intentAt, store.RunIntentRunning); rerr != nil {
					slog.Error("Budget suspend: intent revert failed",
						"agent_id", agent.ID, "agent_name", agent.Name, "error", rerr)
				}
			}
			return fmt.Errorf("budget suspend dispatch failed: %w", err)
		}
	}

	releaseClaim := func() {
		if agent.RuntimeBrokerID != "" {
			s.releaseSupersededClaim(ctx, agent.ID, supersedes, intentAt)
		}
	}

	statusUpdate := store.AgentStatusUpdate{
		Phase:               string(state.PhaseSuspended),
		ContainerStatus:     "stopped",
		Activity:            "",
		RetainedActivity:    preStopRetained,
		SuspendReason:       string(reason),
		IfNoActiveIdleClaim: true,
	}
	recorded, err := s.recordStopStatus(ctx, agent.ID, stopRunID, "budget-suspend", statusUpdate)
	endLifecycleOp()
	if err != nil {
		if s.stopRunStillCurrent(ctx, agent.ID, stopRunID, "budget-suspend (credential revoke)") {
			s.revokeSuspendedCredentials(ctx, agent.ID)
		}
		releaseClaim()
		return fmt.Errorf("budget suspend status update failed: %w", err)
	}
	if !recorded {
		releaseClaim()
		return nil
	}

	s.completeAutoSuspendRecord(ctx, agent, stopRunID, reason)
	s.persistSuspendScreen(ctx, agent.ID, captured)
	releaseClaim()
	return nil
}

// BudgetBreachHandler returns (lazily constructing on first call) the Hub's
// raw-token budget breach handler.
func (s *Server) BudgetBreachHandler() *BudgetBreachHandler {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	h := s.budgetBreachHandler
	s.mu.RUnlock()
	if h != nil {
		return h
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.budgetBreachHandler == nil && s.store != nil {
		if s.pooledGroupResolver == nil {
			s.pooledGroupResolver = NewPooledGroupResolver(s.store)
		}
		var q durablequeue.Queue
		if s.durableQueue != nil {
			q = s.durableQueue
		}
		s.budgetBreachHandler = NewBudgetBreachHandler(s, q, slog.Default())
	}
	return s.budgetBreachHandler
}

// NewBudgetSuspendDrainer constructs the durablequeue.Drainer for
// usage.budget_suspend items.
func (s *Server) NewBudgetSuspendDrainer() (*durablequeue.Drainer, error) {
	if s == nil || s.durableQueue == nil {
		return nil, nil
	}
	dqs, ok := s.store.(store.DurableQueueStore)
	if !ok || dqs == nil {
		return nil, nil
	}
	h := s.BudgetBreachHandler()
	if h == nil {
		return nil, nil
	}
	return durablequeue.NewDrainer(durablequeue.DrainerConfig{
		Store: dqs,
		Queue: s.durableQueue,
		Kind:  usageBudgetSuspendQueueKind,
		Handler: func(ctx context.Context, item durablequeue.Item, _ durablequeue.Lease) durablequeue.Result {
			if err := h.HandleSuspendQueueItem(ctx, item); err != nil {
				return durablequeue.Result{Err: err}
			}
			return durablequeue.Result{Done: true}
		},
		Owner:          s.HubID(),
		Enabled:        h.enabled,
		PollInterval:   5 * time.Second,
		LeaseDuration:  60 * time.Second,
		HandlerTimeout: 45 * time.Second,
		BatchSize:      10,
	})
}

// startBudgetSuspendDrainer lazily constructs and starts the background
// durablequeue.Drainer for usage.budget_suspend when both tokenomics
// experiments are enabled.
func (s *Server) startBudgetSuspendDrainer(ctx context.Context) {
	if s == nil || s.durableQueue == nil {
		return
	}
	if !s.experimentEnabled(experiments.TokenomicsUsageLedger) ||
		!s.experimentEnabled(experiments.TokenomicsTokenBudgets) {
		return
	}
	s.mu.Lock()
	if s.budgetSuspendDrainer != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Ensure evaluator + default breach hook are initialized.
	_ = s.TokenBudgetEvaluator()

	drainer, err := s.NewBudgetSuspendDrainer()
	if err != nil {
		slog.Warn("failed to construct token budget suspend drainer", "error", err)
		return
	}
	if drainer == nil {
		return
	}

	s.mu.Lock()
	if s.budgetSuspendDrainer != nil {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	s.budgetSuspendDrainer = drainer
	s.budgetSuspendCancel = cancel
	s.budgetSuspendDone = done
	s.mu.Unlock()

	go func() {
		defer close(done)
		drainer.Run(runCtx)
	}()
}

// stopBudgetSuspendDrainer stops the background usage.budget_suspend drainer
// if it was started and waits for it to exit within ctx.
func (s *Server) stopBudgetSuspendDrainer(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.budgetSuspendCancel
	done := s.budgetSuspendDone
	s.budgetSuspendDrainer = nil
	s.budgetSuspendCancel = nil
	s.budgetSuspendDone = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
}
