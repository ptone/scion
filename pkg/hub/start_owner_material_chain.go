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
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Start-time owner material and the agent's recorded chain.
//
// At create, start and restart the dispatcher reads user-scope material
// (secrets and env vars) keyed on agent.OwnerID. A row the owner shares
// with progeny (AllowProgeny) is held to the user-scope secret parent
// ceiling (startSecretResolveOpts) for secrets, and is unchanged for env
// vars. A row not shared with progeny is the owner's own material: it is
// delivered only along the agent's recorded chain. The rule, per row of
// owner R:
//
//   - the agent's stored ancestry, when it has one, starts at R
//     (Ancestry[0], the provenance root the runtime read is keyed on);
//   - the agent's recorded delegation chain, followed edge by edge in the
//     agent's project, ends at user R.
//
// When either does not hold, the row is withheld from the start (a
// lower-scope row with the same key stays in effect). This is the case of
// an agent whose authority was re-recorded to another user (for example a
// reincarnation by another user): its ancestry still starts at its creator,
// but its chain now ends at the other user, so the creator's own rows stop
// reaching it.
//
// The rule is about who the chain ends at, not what it allows: the chain's
// permission clauses (effect ceilings, held or inactive principals, project
// admission) are not applied to these rows, so a user's own agent at any
// role keeps the user's own rows as before.
//
// Legacy data keeps today's delivery, with a rate-limited Warn naming the
// cohort (ptone 2026-10-28, legacy cohort decision A):
//
//   - the agent's own edge is missing (legacy_no_edge), is the migration
//     sentinel (legacy_sentinel_edge), carries no recorded provenance
//     (legacy_unadopted_edge), is one of several active edges
//     (legacy_ambiguous_edges), or records a principal that does not
//     type-check (legacy_inconsistent_principal). The ancestry check
//     above still applies.
//
// An adopted edge (source system_migration) and an edge recorded from a
// user access token (source uat) are recorded edges: their delegator is
// the user, so the owner's own agent passes. A row's CreatedBy is not
// consulted (rows written before it was recorded have none).
//
// Fail closed: a store error while following the chain, a depth or cycle
// violation, or a hop above the agent's own edge without recorded
// provenance withholds every such row.

// Owner material chain outcomes.
type ownerChainOutcome int

const (
	// ownerChainEndsAtUser: the recorded chain ends at terminalUserID.
	ownerChainEndsAtUser ownerChainOutcome = iota
	// ownerChainLegacy: the agent's own edge has no recorded provenance.
	ownerChainLegacy
	// ownerChainWithhold: the chain cannot be followed (reason).
	ownerChainWithhold
)

// Withhold reasons and legacy cohorts of the owner material chain rule.
const (
	ownerChainReasonTerminalNotOwner = "terminal_user_not_owner"
	ownerChainReasonRootNotOwner     = "provenance_root_not_owner"
	ownerChainReasonUnrecordedAbove  = "unrecorded_hop_above_agent"
	ownerChainReasonInvalid          = "chain_invalid"
	ownerChainReasonLookupError      = "chain_lookup_error"

	ownerChainCohortAmbiguousEdges        = "legacy_ambiguous_edges"
	ownerChainCohortInconsistentPrincipal = "legacy_inconsistent_principal"
)

// ownerChainVerdict is the result of following an agent's recorded chain.
type ownerChainVerdict struct {
	outcome        ownerChainOutcome
	terminalUserID string // ownerChainEndsAtUser
	reason         string // withhold reason, or legacy cohort
}

// ownerChainVerdict follows agent's recorded delegation edges in its project
// up to the first user delegator. It reads edges only; it allows nothing
// by itself.
func (a *AuthzService) ownerChainVerdict(ctx context.Context, agent *store.Agent) ownerChainVerdict {
	if a == nil || a.store == nil || agent == nil || agent.ID == "" {
		return ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonLookupError}
	}
	ctx = maskAuthzInputs(ctx)
	visited := make(map[string]bool, maxDelegationDepth+1)
	delegateID := agent.ID
	for depth := 0; ; depth++ {
		if depth > maxDelegationDepth || visited[delegateID] {
			return ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonInvalid}
		}
		visited[delegateID] = true
		active, err := a.activeProjectEdges(ctx, delegateID, agent.ProjectID)
		if err != nil {
			return ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonLookupError}
		}
		cohort := ""
		var edge *store.DelegationEdge
		switch len(active) {
		case 0:
			cohort = legacyDeliveryCohortNoEdge
		case 1:
			edge = active[0]
			switch {
			case isMigrationSentinel(edge):
				cohort = legacyDeliveryCohortSentinelEdge
			case !knownProvenanceVersion(edge.ProvenanceVersion) ||
				(edge.Kind != store.EffectCeilingPrincipal && edge.Kind != store.EffectCeilingBounded):
				cohort = legacyDeliveryCohortUnadoptedEdge
			case checkEdgePrincipal(edge) != nil:
				cohort = ownerChainCohortInconsistentPrincipal
			}
		default:
			cohort = ownerChainCohortAmbiguousEdges
		}
		if cohort != "" {
			if depth == 0 {
				return ownerChainVerdict{outcome: ownerChainLegacy, reason: cohort}
			}
			return ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonUnrecordedAbove}
		}
		switch edge.DelegatorType {
		case store.DelegationPrincipalUser:
			return ownerChainVerdict{outcome: ownerChainEndsAtUser, terminalUserID: edge.DelegatorID}
		case store.DelegationPrincipalAgent:
			delegateID = edge.DelegatorID
		default:
			// checkEdgePrincipal admits only user and agent delegators.
			return ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonInvalid}
		}
	}
}

// ownerRowDecision applies the rule to one row of owner rowOwner. It
// returns whether the row is delivered, and the withhold reason or the
// legacy cohort ("" for a plain delivery).
func ownerRowDecision(v ownerChainVerdict, ancestry []string, rowOwner string) (deliver bool, legacy bool, reason string) {
	if rowOwner == "" {
		return false, false, ownerChainReasonRootNotOwner
	}
	if len(ancestry) > 0 && ancestry[0] != rowOwner {
		return false, false, ownerChainReasonRootNotOwner
	}
	switch v.outcome {
	case ownerChainLegacy:
		return true, true, v.reason
	case ownerChainEndsAtUser:
		if v.terminalUserID != rowOwner {
			return false, false, ownerChainReasonTerminalNotOwner
		}
		return true, false, ""
	default:
		return false, false, v.reason
	}
}

// Kinds counted by the owner material gate.
const (
	ownerMaterialKindSecret = "secret"
	ownerMaterialKindEnvVar = "env_var"
)

// Sites the owner material gate reports.
const (
	ownerMaterialSiteResolveSecrets = "resolve_secrets"
	ownerMaterialSiteAsNeeded       = "as_needed"
	ownerMaterialSiteLaunchSecrets  = "launch_secrets"
	ownerMaterialSiteLaunchEnv      = "launch_env"
	ownerMaterialSiteEnvStorage     = "env_storage"
	ownerMaterialSiteCloneToken     = "clone_token"
)

// ownerMaterialChainWarnInterval is the shortest gap between two Warn lines
// for the same agent, site and reason.
const ownerMaterialChainWarnInterval = 10 * time.Minute

type ownerMaterialCounts struct{ secrets, envVars int }

func (c *ownerMaterialCounts) add(kind string) {
	if kind == ownerMaterialKindEnvVar {
		c.envVars++
		return
	}
	c.secrets++
}

// ownerMaterialGate applies the owner material chain rule to the non-shared
// user-scope rows of one start-time site, for one agent. The chain is
// followed once, on the first row. Withheld and legacy rows are counted and
// reported by flush. A nil gate allows every row (no authorization service
// is configured).
type ownerMaterialGate struct {
	d       *HTTPAgentDispatcher
	agent   *store.Agent
	done    bool
	verdict ownerChainVerdict

	withheld map[string]*ownerMaterialCounts // by reason
	legacy   map[string]*ownerMaterialCounts // by cohort
}

func (d *HTTPAgentDispatcher) newOwnerMaterialGate(agent *store.Agent) *ownerMaterialGate {
	if d == nil || d.authzService == nil || agent == nil || agent.OwnerID == "" {
		return nil
	}
	return &ownerMaterialGate{d: d, agent: agent}
}

// allow reports whether a non-shared user-scope row of rowOwner, of kind
// (ownerMaterialKindSecret or ownerMaterialKindEnvVar), may be delivered.
func (g *ownerMaterialGate) allow(ctx context.Context, kind, rowOwner string) bool {
	if g == nil {
		return true
	}
	if !g.done {
		g.verdict = g.d.authzService.ownerChainVerdict(ctx, g.agent)
		g.done = true
	}
	deliver, legacy, reason := ownerRowDecision(g.verdict, g.agent.Ancestry, rowOwner)
	switch {
	case !deliver:
		if g.withheld == nil {
			g.withheld = make(map[string]*ownerMaterialCounts)
		}
		if g.withheld[reason] == nil {
			g.withheld[reason] = &ownerMaterialCounts{}
		}
		g.withheld[reason].add(kind)
	case legacy:
		if g.legacy == nil {
			g.legacy = make(map[string]*ownerMaterialCounts)
		}
		if g.legacy[reason] == nil {
			g.legacy[reason] = &ownerMaterialCounts{}
		}
		g.legacy[reason].add(kind)
	}
	return deliver
}

// allowEnvRow applies the rule to a stored env var row listed under filter:
// a user-scope row not shared with progeny (plain or secret-flagged) must
// pass allow; every other row is not the gate's to decide.
func (g *ownerMaterialGate) allowEnvRow(ctx context.Context, filter store.EnvVarFilter, v store.EnvVar) bool {
	if g == nil || filter.Scope != store.ScopeUser || v.AllowProgeny {
		return true
	}
	owner := v.ScopeID
	if owner == "" {
		owner = filter.ScopeID
	}
	return g.allow(ctx, ownerMaterialKindEnvVar, owner)
}

// flush reports the rows counted since the last flush, for site: one
// rate-limited Warn per reason or cohort with the agent, the counts and the
// reason (never a key or a value), and the metric. The counts are reset.
func (g *ownerMaterialGate) flush(ctx context.Context, site string) {
	if g == nil {
		return
	}
	a := g.d.authzService
	for reason, c := range g.withheld {
		a.recordOwnerMaterialChain(ctx, "withheld", reason, c)
		if !g.d.admitOwnerChainWarn(g.agent.ID+"/"+site, "withheld/"+reason) {
			continue
		}
		g.d.log.WarnContext(ctx, "start-time user material withheld: the agent's recorded delegation chain does not end at the material's owner",
			"site", site, "agent_id", g.agent.ID, "owner_id", g.agent.OwnerID,
			"terminal_user_id", g.verdict.terminalUserID, "reason", reason,
			"secrets", c.secrets, "env_vars", c.envVars)
	}
	for cohort, c := range g.legacy {
		a.recordOwnerMaterialChain(ctx, "legacy", cohort, c)
		if !g.d.admitOwnerChainWarn(g.agent.ID+"/"+site, "legacy/"+cohort) {
			continue
		}
		g.d.log.WarnContext(ctx, "start-time user material delivered through the legacy fallback: the agent's delegation chain is not recorded",
			"site", site, "agent_id", g.agent.ID, "owner_id", g.agent.OwnerID,
			"cohort", cohort, "exception", "legacy",
			"secrets", c.secrets, "env_vars", c.envVars)
	}
	g.withheld, g.legacy = nil, nil
}

// admitOwnerChainWarn rate limits the owner material chain Warn per key and
// cause. Without a limiter every line is logged.
func (d *HTTPAgentDispatcher) admitOwnerChainWarn(key, cause string) bool {
	if d.ownerChainWarn == nil {
		return true
	}
	record, _ := d.ownerChainWarn.admit(key, cause)
	return record
}

func newOwnerMaterialChainLimiter() *hookRefusalLimiter {
	return newHookRefusalLimiter(ownerMaterialChainWarnInterval, time.Now)
}

// ownerMaterialChainMetrics counts rows the owner material chain rule
// withholds or delivers through the legacy fallback.
type ownerMaterialChainMetrics interface {
	RecordOwnerMaterialChain(ctx context.Context, outcome, reason, kind string, n int64)
}

// OTelOwnerMaterialChainMetrics counts them as
// scion.hub.user_material.owner_chain (attributes outcome, reason, kind).
type OTelOwnerMaterialChainMetrics struct {
	total metric.Int64Counter
}

// NewOTelOwnerMaterialChainMetrics creates the counter.
func NewOTelOwnerMaterialChainMetrics(mp metric.MeterProvider) (*OTelOwnerMaterialChainMetrics, error) {
	if mp == nil {
		return nil, errors.New("otel owner material chain metrics: nil MeterProvider")
	}
	c, err := mp.Meter(instrumentationScope).Int64Counter("scion.hub.user_material.owner_chain",
		metric.WithUnit("{row}"))
	if err != nil {
		return nil, fmt.Errorf("creating user_material.owner_chain counter: %w", err)
	}
	return &OTelOwnerMaterialChainMetrics{total: c}, nil
}

// RecordOwnerMaterialChain implements ownerMaterialChainMetrics.
func (m *OTelOwnerMaterialChainMetrics) RecordOwnerMaterialChain(ctx context.Context, outcome, reason, kind string, n int64) {
	m.total.Add(ctx, n, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("reason", reason),
		attribute.String("kind", kind),
	))
}

// SetOwnerMaterialChainMetrics installs the owner material chain counter.
func (s *Server) SetOwnerMaterialChainMetrics(m ownerMaterialChainMetrics) {
	if s == nil || s.authzService == nil {
		return
	}
	s.authzService.ownerChainMetrics = m
}

// OwnerMaterialChainWithheld returns the number of rows the owner material
// chain rule withheld since the process started.
func (a *AuthzService) OwnerMaterialChainWithheld() int64 {
	if a == nil {
		return 0
	}
	return a.ownerChainWithheld.Load()
}

func (a *AuthzService) recordOwnerMaterialChain(ctx context.Context, outcome, reason string, c *ownerMaterialCounts) {
	if a == nil || c == nil {
		return
	}
	if outcome == "withheld" {
		a.ownerChainWithheld.Add(int64(c.secrets + c.envVars))
	}
	m := a.ownerChainMetrics
	if m == nil {
		return
	}
	if c.secrets > 0 {
		m.RecordOwnerMaterialChain(ctx, outcome, reason, ownerMaterialKindSecret, int64(c.secrets))
	}
	if c.envVars > 0 {
		m.RecordOwnerMaterialChain(ctx, outcome, reason, ownerMaterialKindEnvVar, int64(c.envVars))
	}
}

// ownerCloneTokenAllowed applies the start-time user-scope checks
// (startSecretResolveOpts: the parent ceiling for a shared row, the owner
// material chain rule for a row not shared) to the owner's GITHUB_TOKEN row
// before the NoAuth clone-token fallback reads it by name. A missing row,
// or one whose metadata cannot be read, is not read either way: the read
// that follows would find nothing usable, and refusing on a fault is fail
// closed. Without an authorization service the row is not checked, as
// Resolve does not check it then.
func (d *HTTPAgentDispatcher) ownerCloneTokenAllowed(ctx context.Context, agent *store.Agent) bool {
	opts, gate := d.startSecretResolveOpts(ctx, agent)
	if opts == nil || opts.UserScopeCheck == nil {
		return true
	}
	defer gate.flush(ctx, ownerMaterialSiteCloneToken)
	meta, err := d.secretBackend.GetMeta(ctx, "GITHUB_TOKEN", store.ScopeUser, agent.OwnerID)
	if err != nil || meta == nil {
		return false
	}
	return opts.UserScopeCheck(*meta)
}
