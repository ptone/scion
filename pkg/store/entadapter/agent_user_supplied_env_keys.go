// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package entadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/predicate"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// userSuppliedEnvKeysField is the applied-config JSON key of
// AgentAppliedConfig.UserSuppliedEnvKeys.
const userSuppliedEnvKeysField = "userSuppliedEnvKeys"

// addUserSuppliedEnvKeysHook, when set by a test, runs inside each
// AddAgentUserSuppliedEnvKeys attempt between the read and the conditional
// write, with the attempt's transaction.
var addUserSuppliedEnvKeysHook func(ctx context.Context, tx *ent.Tx, id uuid.UUID)

// AddAgentUserSuppliedEnvKeys implements store.AgentStore. See the
// interface for the contract. Each attempt reads the applied config, merges
// keys into the stored userSuppliedEnvKeys list (sorted union) and writes
// it back with only that key changed, in one transaction with the row
// locked where the dialect supports it, conditional on the stored applied
// config being unchanged; a miss is retried, as SetAgentConfigDelivered
// does.
func (s *AgentStore) AddAgentUserSuppliedEnvKeys(ctx context.Context, id string, keys []string) ([]string, bool, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, false, err
	}
	for attempt := 0; attempt < configDeliveredAttempts; attempt++ {
		names, written, done, err := s.addAgentUserSuppliedEnvKeysOnce(ctx, uid, keys)
		if err != nil || done {
			return names, written, err
		}
	}
	return nil, false, fmt.Errorf("add user-supplied env key names for agent %s: applied config changed concurrently", id)
}

// addAgentUserSuppliedEnvKeysOnce is one AddAgentUserSuppliedEnvKeys
// attempt. done is false only when the applied config changed between the
// read and the write; written reports that this attempt stored the list.
func (s *AgentStore) addAgentUserSuppliedEnvKeysOnce(ctx context.Context, uid uuid.UUID, keys []string) (names []string, written, done bool, err error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, false, true, err
	}
	defer func() { _ = tx.Rollback() }()
	q := tx.Agent.Query().Where(agent.IDEQ(uid), agent.DeletedAtIsNil())
	if s.usesRowLocks() {
		q = q.ForUpdate()
	}
	row, err := q.Select(agent.FieldAppliedConfig).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, false, true, nil
		}
		return nil, false, true, mapError(err)
	}
	raw := map[string]json.RawMessage{}
	if row.AppliedConfig != "" {
		if err := json.Unmarshal([]byte(row.AppliedConfig), &raw); err != nil {
			return nil, false, true, fmt.Errorf("add user-supplied env key names for agent %s: %w", uid, err)
		}
		if raw == nil { // the stored value was the JSON literal null
			raw = map[string]json.RawMessage{}
		}
	}
	var stored []string
	if v, ok := raw[userSuppliedEnvKeysField]; ok && len(v) > 0 && string(v) != "null" {
		if err := json.Unmarshal(v, &stored); err != nil {
			return nil, false, true, fmt.Errorf("add user-supplied env key names for agent %s: %w", uid, err)
		}
	}
	merged := unionSorted(stored, keys)
	if slices.Equal(merged, stored) {
		return stored, false, true, nil
	}
	value, err := json.Marshal(merged)
	if err != nil {
		return nil, false, true, err
	}
	raw[userSuppliedEnvKeysField] = value
	updated, err := json.Marshal(raw)
	if err != nil {
		return nil, false, true, err
	}
	if addUserSuppliedEnvKeysHook != nil {
		addUserSuppliedEnvKeysHook(ctx, tx, uid)
	}
	n, err := tx.Agent.Update().
		Where(agent.IDEQ(uid), agent.DeletedAtIsNil(), appliedConfigUnchanged(row.AppliedConfig)).
		SetAppliedConfig(string(updated)).
		Save(ctx)
	if err != nil {
		return nil, false, true, mapError(err)
	}
	if n == 0 {
		return nil, false, false, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, false, true, err
	}
	return merged, true, true, nil
}

// unionSorted returns the sorted, deduplicated union of a and b, without
// empty names; nil when it is empty.
func unionSorted(a, b []string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, k := range list {
			if k != "" {
				set[k] = struct{}{}
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// keepUserSuppliedKeysHook, when set by a test, runs inside each whole-row
// write attempt (UpdateAgent, UpdateAgentConfig) between the applied-config
// read and the conditional write, with the attempt's client.
var keepUserSuppliedKeysHook func(ctx context.Context, client *ent.Client, id uuid.UUID)

// saveKeepingUserSuppliedKeys runs one whole-row write of applied, built and
// saved by save against client, so that the write keeps every
// userSuppliedEnvKeys name stored on the row: it reads the stored applied
// config (row-locked where the dialect supports it), passes save the
// applied config with the stored names merged in (keepUserSuppliedKeys) and
// a predicate that makes the write conditional on the stored applied
// config being unchanged. A miss is retried when the stored applied config
// changed after the read; any other miss (version, launch state, missing
// row) is returned as zero rows for the caller to classify. Run it inside a
// transaction so the read and the write share it.
func (s *AgentStore) saveKeepingUserSuppliedKeys(ctx context.Context, client *ent.Client, uid uuid.UUID, applied *store.AgentAppliedConfig, save func(applied *store.AgentAppliedConfig, unchanged predicate.Agent) (int, error)) (int, error) {
	for attempt := 0; attempt < configDeliveredAttempts; attempt++ {
		read, found, err := s.readAppliedConfig(ctx, client, uid, true)
		if err != nil {
			return 0, err
		}
		if !found {
			return save(applied, agent.IDEQ(uid))
		}
		if keepUserSuppliedKeysHook != nil {
			keepUserSuppliedKeysHook(ctx, client, uid)
		}
		n, err := save(keepUserSuppliedKeys(applied, read), appliedConfigUnchanged(read))
		if err != nil || n > 0 {
			return n, err
		}
		now, found, err := s.readAppliedConfig(ctx, client, uid, false)
		if err != nil {
			return 0, err
		}
		if !found || now == read {
			return 0, nil
		}
	}
	// Unreachable where the row is locked (Postgres) or the store has one
	// connection (SQLite); a conflict, so callers that retry one retry this.
	return 0, fmt.Errorf("update agent %s: applied config changed concurrently: %w", uid, store.ErrVersionConflict)
}

// readAppliedConfig reads the raw applied_config of the agent row uid,
// soft-deleted rows included, locking the row when lock is set and the
// dialect supports it. found is false when the row does not exist.
func (s *AgentStore) readAppliedConfig(ctx context.Context, client *ent.Client, uid uuid.UUID, lock bool) (raw string, found bool, err error) {
	q := client.Agent.Query().Where(agent.IDEQ(uid))
	if lock && s.usesRowLocks() {
		q = q.ForUpdate()
	}
	row, err := q.Select(agent.FieldAppliedConfig).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, mapError(err)
	}
	return row.AppliedConfig, true, nil
}

// keepUserSuppliedKeys returns applied with its UserSuppliedEnvKeys set to
// the sorted union of the names in the stored raw applied config and its
// own; applied itself when that adds nothing. Only that one field changes:
// every other field, ConfigDelivered included, is written as given. A
// stored value that does not parse holds no names (AddAgentUserSuppliedEnvKeys
// never writes to such a row), so the write replaces it as before.
func keepUserSuppliedKeys(applied *store.AgentAppliedConfig, storedRaw string) *store.AgentAppliedConfig {
	if storedRaw == "" {
		return applied
	}
	var stored struct {
		UserSuppliedEnvKeys []string `json:"userSuppliedEnvKeys"`
	}
	if err := json.Unmarshal([]byte(storedRaw), &stored); err != nil || len(stored.UserSuppliedEnvKeys) == 0 {
		return applied
	}
	var given []string
	if applied != nil {
		given = applied.UserSuppliedEnvKeys
	}
	merged := unionSorted(stored.UserSuppliedEnvKeys, given)
	if slices.Equal(merged, given) {
		return applied
	}
	out := store.AgentAppliedConfig{}
	if applied != nil {
		out = *applied
	}
	out.UserSuppliedEnvKeys = merged
	return &out
}

// inAgentWriteTx runs fn in a new transaction, or directly on s.client when
// s.client already wraps one (inside store.WithTx; the caller then owns
// commit and rollback), and returns fn's row count.
func (s *AgentStore) inAgentWriteTx(ctx context.Context, fn func(client *ent.Client) (int, error)) (int, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		if !errors.Is(err, ent.ErrTxStarted) {
			return 0, err
		}
		return fn(s.client)
	}
	n, err := fn(tx.Client())
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("agent store: commit agent update: %w", err)
	}
	return n, nil
}
