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

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/agentcredential"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/agentidentitykey"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/runtimebroker"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
)

// delegateTypeAgent is the delegate type agent delegation edges are keyed by.
const delegateTypeAgent = store.PolicyPrincipalTypeAgent

// fixtureStore is an open hub store plus its Ent client. The Ent client is
// used for READ-ONLY checks the store interface does not expose (identity-key
// rows, agent credentials, broker slugs). Every write goes through the
// existing store.Store methods.
type fixtureStore struct {
	client *ent.Client
	store  *entadapter.CompositeStore
}

// openStore opens an existing SQLite hub DB with the offline-writer pattern
// used by cmd/hub_secret_migrate.go and cmd/server_recover_authz.go:
// entc.OpenSQLite -> entadapter.NewCompositeStore -> Migrate. The caller must
// have pinned the process to UTC (util.PinProcessUTC) first.
func openStore(ctx context.Context, dbPath string) (*fixtureStore, error) {
	client, err := entc.OpenSQLite("file:"+dbPath+"?cache=shared", entc.PoolConfig{})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	cs := entadapter.NewCompositeStore(client)
	if err := cs.Migrate(ctx); err != nil {
		_ = cs.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &fixtureStore{client: client, store: cs}, nil
}

func (f *fixtureStore) Close() error { return f.store.Close() }

// isNotFound reports whether err is the store's not-found sentinel.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// Preflight checks the recipe against the database before any write: the
// admin and projects it binds must exist and match, and none of the rows it
// would insert (IDs, slugs, identity keys, broker names) may already exist.
// It never writes.
func (f *fixtureStore) Preflight(ctx context.Context, r *Recipe) error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	u, err := f.store.GetUser(ctx, r.Admin.UserID)
	switch {
	case err != nil:
		add("admin user %s: %v", r.Admin.UserID, err)
	default:
		if !strings.EqualFold(u.Email, r.Admin.Email) {
			add("admin user %s has a different email than the recipe", r.Admin.UserID)
		}
		if u.Role != "admin" {
			add("admin user %s has role %q, want admin", r.Admin.UserID, u.Role)
		}
		if u.Status != "" && u.Status != "active" {
			add("admin user %s has status %q, want active", r.Admin.UserID, u.Status)
		}
	}
	if byEmail, err := f.store.GetUserByEmail(ctx, r.Admin.Email); err != nil {
		add("admin email lookup: %v", err)
	} else if byEmail.ID != r.Admin.UserID {
		add("admin email resolves to user %s, not %s", byEmail.ID, r.Admin.UserID)
	}

	for _, p := range r.Projects {
		got, err := f.store.GetProject(ctx, p.ID)
		if err != nil {
			add("project %s: %v", p.ID, err)
			continue
		}
		if got.Slug != p.Slug {
			add("project %s has slug %q, recipe says %q", p.ID, got.Slug, p.Slug)
		}
	}

	for _, a := range r.Agents {
		if _, err := f.store.GetAgent(ctx, a.ID); !isNotFound(err) {
			add("agent %s (%s): already exists or lookup failed (%v); refusing to write over existing rows", a.ID, a.Slug, err)
		}
		if _, err := f.store.GetAgentBySlug(ctx, a.ProjectID, a.Slug); !isNotFound(err) {
			add("agent slug %q in project %s: already exists or lookup failed (%v)", a.Slug, a.ProjectID, err)
		}
		pid, err := uuid.Parse(a.ProjectID)
		if err != nil {
			continue // already reported by Validate
		}
		n, err := f.client.AgentIdentityKey.Query().
			Where(agentidentitykey.ProjectIDEQ(pid), agentidentitykey.KeyIn(api.IdentityKeysFor(a.Slug, a.Slug)...)).
			Count(ctx)
		if err != nil {
			add("identity keys for %q: %v", a.Slug, err)
		} else if n > 0 {
			add("identity key %q is already held by another agent in project %s", a.Slug, a.ProjectID)
		}
	}

	for _, b := range r.Brokers {
		if _, err := f.store.GetRuntimeBroker(ctx, b.ID); !isNotFound(err) {
			add("broker %s (%s): already exists or lookup failed (%v)", b.ID, b.Slug, err)
		}
		if _, err := f.store.GetRuntimeBrokerByName(ctx, b.Name); !isNotFound(err) {
			add("broker name %q: already exists or lookup failed (%v)", b.Name, err)
		}
		n, err := f.client.RuntimeBroker.Query().Where(runtimebroker.SlugEQ(b.Slug)).Count(ctx)
		if err != nil {
			add("broker slug %q: %v", b.Slug, err)
		} else if n > 0 {
			add("broker slug %q already exists", b.Slug)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("preflight refused (%d problems):\n  - %s", len(errs), strings.Join(errs, "\n  - "))
	}
	return nil
}

// WriteResult lists the rows a run inserted, in order. On a partial failure it
// holds what was committed before the failure.
type WriteResult struct {
	Agents  []string `json:"agents"`
	Brokers []string `json:"brokers"`
	// FailedAt names the record whose write failed, if any.
	FailedAt string `json:"failedAt,omitempty"`
}

// buildAgent maps a recipe agent to the store row CreateAgent receives. Only
// identity, ownership and the allowed terminal state are set; every live,
// heartbeat, launch, claim, deletion and broker field stays at its zero value.
func buildAgent(r *Recipe, a RecipeAgent) *store.Agent {
	var labels map[string]string
	if len(a.Labels) > 0 {
		labels = maps.Clone(a.Labels)
	}
	return &store.Agent{
		ID:          a.ID,
		Slug:        a.Slug,
		Name:        a.Slug, // Name = Slug, as the API create handler does.
		Template:    a.Template,
		ProjectID:   a.ProjectID,
		Phase:       a.Phase,
		Activity:    a.Activity,
		Message:     a.Message,
		TaskSummary: a.TaskSummary,
		Labels:      labels,
		Detached:    true,
		CreatedBy:   r.Admin.UserID,
		OwnerID:     r.Admin.UserID,
		Ancestry:    []string{r.Admin.UserID},
		AppliedConfig: &store.AgentAppliedConfig{
			AgentRole:   a.AgentRole,
			CreatorName: r.Admin.Email,
		},
	}
}

// writeAgent inserts one agent:
//  1. CreateAgent + ReplaceAgentIdentityKeys in one WithTx;
//  2. UpdateAgent (StateVersion 1, read back from the store) only when the
//     recipe sets ExitCode/ExitReason, which CreateAgent does not write;
//  3. SetRunIntent(stopped), outside any transaction.
//
// It never calls UpdateAgentStatus.
func (f *fixtureStore) writeAgent(ctx context.Context, r *Recipe, a RecipeAgent) error {
	row := buildAgent(r, a)
	err := f.store.WithTx(ctx, func(tx store.Store) error {
		if err := tx.CreateAgent(ctx, row); err != nil {
			return fmt.Errorf("CreateAgent: %w", err)
		}
		if err := tx.ReplaceAgentIdentityKeys(ctx, row.ID, row.ProjectID, api.IdentityKeysFor(row.Slug, row.Name)); err != nil {
			return fmt.Errorf("ReplaceAgentIdentityKeys: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if a.ExitCode != nil || a.ExitReason != "" {
		fresh, err := f.store.GetAgent(ctx, a.ID)
		if err != nil {
			return fmt.Errorf("read back before exit-field update: %w", err)
		}
		if fresh.StateVersion != 1 {
			return fmt.Errorf("unexpected state version %d before exit-field update (want 1)", fresh.StateVersion)
		}
		if a.ExitCode != nil {
			code := *a.ExitCode
			fresh.ExitCode = &code
		}
		fresh.ExitReason = a.ExitReason
		if err := f.store.UpdateAgent(ctx, fresh); err != nil {
			return fmt.Errorf("UpdateAgent (exit fields): %w", err)
		}
	}

	if _, err := f.store.SetRunIntent(ctx, a.ID, store.RunIntentStopped); err != nil {
		return fmt.Errorf("SetRunIntent(stopped): %w", err)
	}
	return nil
}

// writeBroker inserts one B1 offline broker. Status, connection state and
// heartbeat are left to the schema defaults (offline, disconnected, NULL); no
// join token, secret or registration is created.
func (f *fixtureStore) writeBroker(ctx context.Context, b RecipeBroker) error {
	row := &store.RuntimeBroker{
		ID:      b.ID,
		Name:    b.Name,
		Slug:    b.Slug,
		Version: b.Version,
	}
	if len(b.Labels) > 0 {
		row.Labels = maps.Clone(b.Labels)
	}
	if err := f.store.CreateRuntimeBroker(ctx, row); err != nil {
		return fmt.Errorf("CreateRuntimeBroker: %w", err)
	}
	return nil
}

// Write inserts every recipe row in order, stopping at the first failure. The
// returned result always lists what was committed, so a partial failure can be
// reported honestly; the caller must then discard the clone.
func (f *fixtureStore) Write(ctx context.Context, r *Recipe) (WriteResult, error) {
	res := WriteResult{Agents: []string{}, Brokers: []string{}}
	for _, b := range r.Brokers {
		if err := f.writeBroker(ctx, b); err != nil {
			res.FailedAt = "broker " + b.ID
			return res, fmt.Errorf("broker %s (%s): %w", b.ID, b.Slug, err)
		}
		res.Brokers = append(res.Brokers, b.ID)
	}
	for _, a := range r.Agents {
		if err := f.writeAgent(ctx, r, a); err != nil {
			res.FailedAt = "agent " + a.ID
			// A failure after the transaction committed leaves this agent
			// persisted, so it is listed as written too.
			if _, gerr := f.store.GetAgent(ctx, a.ID); gerr == nil {
				res.Agents = append(res.Agents, a.ID)
			}
			return res, fmt.Errorf("agent %s (%s): %w", a.ID, a.Slug, err)
		}
		res.Agents = append(res.Agents, a.ID)
	}
	return res, nil
}

// Verify re-reads every recipe row and checks it against the recipe and the
// forbidden-field rules of the frozen acceptance (H3, H4). It is meant to run
// on a freshly reopened store, so it proves what was persisted.
func (f *fixtureStore) Verify(ctx context.Context, r *Recipe) error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	for _, want := range r.Agents {
		id := want.ID
		got, err := f.store.GetAgent(ctx, id)
		if err != nil {
			add("agent %s: %v", id, err)
			continue
		}
		eq := func(field string, g, w any) {
			if fmt.Sprint(g) != fmt.Sprint(w) {
				add("agent %s: %s = %v, want %v", id, field, g, w)
			}
		}
		eq("slug", got.Slug, want.Slug)
		eq("name", got.Name, want.Slug)
		eq("template", got.Template, want.Template)
		eq("projectId", got.ProjectID, want.ProjectID)
		eq("phase", got.Phase, want.Phase)
		eq("activity", got.Activity, want.Activity)
		eq("exitReason", got.ExitReason, want.ExitReason)
		eq("message", got.Message, want.Message)
		eq("taskSummary", got.TaskSummary, want.TaskSummary)
		eq("detached", got.Detached, true)
		eq("createdBy", got.CreatedBy, r.Admin.UserID)
		eq("ownerId", got.OwnerID, r.Admin.UserID)
		eq("generation", got.Generation, 1)
		if !slices.Equal(got.Ancestry, []string{r.Admin.UserID}) {
			add("agent %s: ancestry = %v, want [%s]", id, got.Ancestry, r.Admin.UserID)
		}
		if (got.ExitCode == nil) != (want.ExitCode == nil) || (got.ExitCode != nil && *got.ExitCode != *want.ExitCode) {
			add("agent %s: exitCode mismatch", id)
		}
		if !maps.Equal(got.Labels, want.Labels) {
			add("agent %s: labels = %v, want %v", id, got.Labels, want.Labels)
		}
		if got.AppliedConfig == nil || got.AppliedConfig.AgentRole != want.AgentRole {
			add("agent %s: appliedConfig.agentRole missing or not %q", id, want.AgentRole)
		}
		if _, err := classify(got.Phase, got.Activity); err != nil {
			add("agent %s: persisted state not allowed: %v", id, err)
		}

		// Forbidden: live / heartbeat / broker fields.
		if !got.LastSeen.IsZero() {
			add("agent %s: lastSeen is set (fabricated heartbeat)", id)
		}
		if !got.LastActivityEvent.IsZero() {
			add("agent %s: lastActivityEvent is set", id)
		}
		if !got.StartedAt.IsZero() {
			add("agent %s: startedAt is set", id)
		}
		if got.RuntimeBrokerID != "" {
			add("agent %s: runtimeBrokerId is set", id)
		}
		if !got.DeletedAt.IsZero() {
			add("agent %s: deletedAt is set", id)
		}
		// Forbidden: launch_* columns.
		if got.LaunchAsyncOptIn || got.LaunchID != "" || got.LaunchState != "" || got.LaunchEndReason != "" ||
			got.LaunchKind != "" || !got.LaunchDeadline.IsZero() || !got.LaunchLastReportAt.IsZero() ||
			got.LaunchOwner != "" || got.LaunchSeq != 0 || got.LaunchStep != "" || got.LaunchError != "" {
			add("agent %s: a launch_* column is set", id)
		}
		// Forbidden: start_claim_* columns.
		if got.StartClaimID != "" || got.StartClaimKind != "" || got.StartClaimState != "" || got.StartClaimOwner != "" ||
			got.StartClaimTarget != "" || got.StartClaimAt != nil || got.StartClaimLeaseUntil != nil ||
			got.StartClaimUnconfirmedAt != nil || got.StartClaimHoldUntil != nil || got.StartClaimLaunchID != "" {
			add("agent %s: a start_claim_* column is set", id)
		}
		// Forbidden: deletion_* columns.
		if got.DeletionState != "" || got.DeletionClaim != 0 || got.DeletionLeaseAt != nil || got.DeletionStartedAt != nil ||
			got.DeletionFailedAt != nil || got.DeletionCode != "" || got.DeletionError != "" ||
			got.DeletionPrior != "" || got.DeletionRequest != "" {
			add("agent %s: a deletion_* column is set", id)
		}
		// Forbidden: reincarnation columns.
		if got.ReincarnationState != "" || got.ReincarnationUpdatedAt != nil {
			add("agent %s: a reincarnation column is set", id)
		}
		if got.RunID != "" || len(got.PreviousRunIDs) != 0 {
			add("agent %s: a run ID is set (implies dispatch)", id)
		}
		if got.RunIntent != store.RunIntentStopped {
			add("agent %s: runIntent = %q, want stopped", id, got.RunIntent)
		}

		// Identity keys: exactly IdentityKeysFor(slug, name).
		uid, err := uuid.Parse(id)
		if err != nil {
			add("agent %s: %v", id, err)
			continue
		}
		keys, err := f.client.AgentIdentityKey.Query().Where(agentidentitykey.AgentIDEQ(uid)).All(ctx)
		if err != nil {
			add("agent %s: identity keys: %v", id, err)
		} else {
			var gotKeys []string
			for _, k := range keys {
				gotKeys = append(gotKeys, k.Key)
				if k.ProjectID.String() != want.ProjectID {
					add("agent %s: identity key %q in project %s", id, k.Key, k.ProjectID)
				}
			}
			slices.Sort(gotKeys)
			wantKeys := api.IdentityKeysFor(want.Slug, want.Slug)
			slices.Sort(wantKeys)
			if !slices.Equal(gotKeys, wantKeys) {
				add("agent %s: identity keys = %v, want %v", id, gotKeys, wantKeys)
			}
		}

		// Forbidden: credentials or delegation edges.
		if n, err := f.client.AgentCredential.Query().Where(agentcredential.AgentIDEQ(id)).Count(ctx); err != nil || n != 0 {
			add("agent %s: %d agent credentials (err %v), want 0", id, n, err)
		}
		if edges, err := f.store.GetDelegationEdgesForDelegate(ctx, delegateTypeAgent, id); err != nil || len(edges) != 0 {
			add("agent %s: %d delegation edges (err %v), want 0", id, len(edges), err)
		}
	}

	for _, want := range r.Brokers {
		id := want.ID
		got, err := f.store.GetRuntimeBroker(ctx, id)
		if err != nil {
			add("broker %s: %v", id, err)
			continue
		}
		if got.Name != want.Name || got.Slug != want.Slug {
			add("broker %s: name/slug = %q/%q, want %q/%q", id, got.Name, got.Slug, want.Name, want.Slug)
		}
		if got.Status != "offline" {
			add("broker %s: status = %q, want offline", id, got.Status)
		}
		if got.ConnectionState != "disconnected" {
			add("broker %s: connectionState = %q, want disconnected", id, got.ConnectionState)
		}
		if !got.LastHeartbeat.IsZero() {
			add("broker %s: lastHeartbeat is set", id)
		}
		if got.ConnectedHubID != nil || got.ConnectedSessionID != nil || got.ConnectedAt != nil {
			add("broker %s: a control-channel affinity field is set", id)
		}
		if _, err := f.store.GetBrokerSecret(ctx, id); !isNotFound(err) {
			add("broker %s: a broker secret exists or lookup failed (%v)", id, err)
		}
		if _, err := f.store.GetJoinTokenByBrokerID(ctx, id); !isNotFound(err) {
			add("broker %s: a join token exists or lookup failed (%v)", id, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("verification failed (%d problems):\n  - %s", len(errs), strings.Join(errs, "\n  - "))
	}
	return nil
}
