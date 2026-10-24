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
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/agent"
)

// userSuppliedEnvKeysField is the applied-config JSON key of
// AgentAppliedConfig.UserSuppliedEnvKeys.
const userSuppliedEnvKeysField = "userSuppliedEnvKeys"

// setUserSuppliedEnvKeysHook, when set by a test, runs inside each
// SetAgentUserSuppliedEnvKeys attempt between the read and the conditional
// write, with the attempt's transaction.
var setUserSuppliedEnvKeysHook func(ctx context.Context, tx *ent.Tx, id uuid.UUID)

// SetAgentUserSuppliedEnvKeys implements store.AgentStore. See the
// interface for the contract. Each attempt reads the applied config and
// writes it back with only the userSuppliedEnvKeys key set (or removed for
// no keys), in one transaction with the row locked where the dialect
// supports it, conditional on the stored applied config being unchanged; a
// miss is retried, as SetAgentConfigDelivered does.
func (s *AgentStore) SetAgentUserSuppliedEnvKeys(ctx context.Context, id string, keys []string) (bool, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return false, err
	}
	var value json.RawMessage
	if len(keys) > 0 {
		if value, err = json.Marshal(keys); err != nil {
			return false, err
		}
	}
	for attempt := 0; attempt < configDeliveredAttempts; attempt++ {
		written, done, err := s.setAgentUserSuppliedEnvKeysOnce(ctx, uid, value)
		if err != nil || done {
			return written, err
		}
	}
	return false, fmt.Errorf("set user-supplied env key names for agent %s: applied config changed concurrently", id)
}

// setAgentUserSuppliedEnvKeysOnce is one SetAgentUserSuppliedEnvKeys
// attempt; value nil removes the key. done is false only when the applied
// config changed between the read and the write.
func (s *AgentStore) setAgentUserSuppliedEnvKeysOnce(ctx context.Context, uid uuid.UUID, value json.RawMessage) (written, done bool, err error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return false, true, err
	}
	defer func() { _ = tx.Rollback() }()
	q := tx.Agent.Query().Where(agent.IDEQ(uid), agent.DeletedAtIsNil())
	if s.usesRowLocks() {
		q = q.ForUpdate()
	}
	row, err := q.Select(agent.FieldAppliedConfig).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, true, nil
		}
		return false, true, mapError(err)
	}
	raw := map[string]json.RawMessage{}
	if row.AppliedConfig != "" {
		if err := json.Unmarshal([]byte(row.AppliedConfig), &raw); err != nil {
			return false, true, fmt.Errorf("set user-supplied env key names for agent %s: %w", uid, err)
		}
		if raw == nil { // the stored value was the JSON literal null
			raw = map[string]json.RawMessage{}
		}
	}
	current, present := raw[userSuppliedEnvKeysField]
	if value == nil {
		if !present {
			return false, true, nil
		}
		delete(raw, userSuppliedEnvKeysField)
	} else {
		if present && bytes.Equal(compactJSON(current), value) {
			return false, true, nil
		}
		raw[userSuppliedEnvKeysField] = value
	}
	updated, err := json.Marshal(raw)
	if err != nil {
		return false, true, err
	}
	if setUserSuppliedEnvKeysHook != nil {
		setUserSuppliedEnvKeysHook(ctx, tx, uid)
	}
	n, err := tx.Agent.Update().
		Where(agent.IDEQ(uid), agent.DeletedAtIsNil(), appliedConfigUnchanged(row.AppliedConfig)).
		SetAppliedConfig(string(updated)).
		Save(ctx)
	if err != nil {
		return false, true, mapError(err)
	}
	if n == 0 {
		return false, false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, true, err
	}
	return true, true, nil
}

// compactJSON returns raw without insignificant whitespace (raw itself when
// it does not compact), for comparing a stored JSON value with a marshalled
// one.
func compactJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}
