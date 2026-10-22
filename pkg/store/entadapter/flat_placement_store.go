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

package entadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/predicate"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/runtimebroker"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Flat Runtime Broker placement persistence
// (.design/flat-runtime-brokers-contract.md section 8).

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// isFlatRuntimeBroker reports whether the Runtime Broker row stores a
// runtime target. A missing row is not flat (it counts as legacy).
func (s *AgentStore) isFlatRuntimeBroker(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := s.client.RuntimeBroker.Query().
		Where(runtimebroker.IDEQ(id), runtimebroker.RuntimeTargetIDNotNil()).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// flatRuntimeBrokerIDs returns the IDs of every flat Runtime Broker row.
func (s *AgentStore) flatRuntimeBrokerIDs(ctx context.Context) ([]string, error) {
	rows, err := s.client.RuntimeBroker.Query().
		Where(runtimebroker.RuntimeTargetIDNotNil()).
		Select(runtimebroker.FieldID).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID.String())
	}
	return ids, nil
}

// SetAgentPinnedRuntimeTarget implements store.AgentStore.
func (s *AgentStore) SetAgentPinnedRuntimeTarget(ctx context.Context, agentID string, expected, next store.PinnedPlacement) (*store.Agent, error) {
	uid, err := parseUUID(agentID)
	if err != nil {
		return nil, err
	}
	if next.RuntimeBrokerID == "" || next.RuntimeTargetID == "" || next.RuntimeTargetType == "" {
		return nil, fmt.Errorf("%w: next placement must name a Runtime Broker and a runtime target", store.ErrInvalidPinnedPlacement)
	}

	preds := []predicate.Agent{agent.IDEQ(uid)}
	if expected.RuntimeBrokerID == "" {
		// runtime_broker_id is nullable: "" matches both '' and NULL.
		preds = append(preds, agent.Or(agent.RuntimeBrokerIDEQ(""), agent.RuntimeBrokerIDIsNil()))
	} else {
		preds = append(preds, agent.RuntimeBrokerIDEQ(expected.RuntimeBrokerID))
	}
	if expected.RuntimeTargetID == "" {
		preds = append(preds, agent.PinnedRuntimeTargetIDIsNil(), agent.PinnedRuntimeBrokerIDIsNil())
	} else {
		preds = append(preds,
			agent.PinnedRuntimeBrokerIDEQ(expected.RuntimeBrokerID),
			agent.PinnedRuntimeTargetIDEQ(expected.RuntimeTargetID),
			agent.PinnedRuntimeTargetTypeEQ(expected.RuntimeTargetType))
	}

	affected, err := s.client.Agent.Update().
		Where(preds...).
		SetRuntimeBrokerID(next.RuntimeBrokerID).
		SetPinnedRuntimeBrokerID(next.RuntimeBrokerID).
		SetPinnedRuntimeTargetID(next.RuntimeTargetID).
		SetPinnedRuntimeTargetType(next.RuntimeTargetType).
		AddStateVersion(1).
		Save(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	if affected == 0 {
		exists, err := s.client.Agent.Query().Where(agent.IDEQ(uid)).Exist(ctx)
		if err != nil {
			return nil, mapError(err)
		}
		if !exists {
			return nil, store.ErrNotFound
		}
		return nil, store.ErrPinnedPlacementChanged
	}
	return s.GetAgent(ctx, agentID)
}

// SetAgentRuntimeBrokerIfEmpty implements store.AgentStore.
func (s *AgentStore) SetAgentRuntimeBrokerIfEmpty(ctx context.Context, agentID, brokerID string) (*store.Agent, error) {
	uid, err := parseUUID(agentID)
	if err != nil {
		return nil, err
	}
	if brokerID == "" {
		return nil, fmt.Errorf("SetAgentRuntimeBrokerIfEmpty: empty runtime broker ID")
	}
	affected, err := s.client.Agent.Update().
		Where(agent.IDEQ(uid), agent.Or(agent.RuntimeBrokerIDEQ(""), agent.RuntimeBrokerIDIsNil())).
		SetRuntimeBrokerID(brokerID).
		AddStateVersion(1).
		Save(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	cur, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if affected == 0 && cur.RuntimeBrokerID != brokerID {
		return nil, store.ErrRuntimeBrokerChanged
	}
	return cur, nil
}

// SetAgentTemplatePin implements store.AgentStore.
func (s *AgentStore) SetAgentTemplatePin(ctx context.Context, agentID, templateID, expectedHash, newHash string) (int64, error) {
	uid, err := parseUUID(agentID)
	if err != nil {
		return 0, err
	}
	if templateID == "" {
		return 0, fmt.Errorf("SetAgentTemplatePin: empty template ID")
	}
	useLock := s.usesRowLocks()
	for i := 0; i < clearRuntimeTargetAttempts; i++ {
		v, done, err := s.setTemplatePinOnce(ctx, uid, templateID, expectedHash, newHash, useLock)
		if err != nil || done {
			return v, err
		}
	}
	return 0, fmt.Errorf("set template pin for agent %s: agent changed concurrently %d times", agentID, clearRuntimeTargetAttempts)
}

// setTemplatePinOnce is one SetAgentTemplatePin attempt. done is false only
// when the conditional write matched no row because the agent changed after
// it was read.
func (s *AgentStore) setTemplatePinOnce(ctx context.Context, uid uuid.UUID, templateID, expectedHash, newHash string, useLock bool) (int64, bool, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	q := tx.Agent.Query().Where(agent.IDEQ(uid), agent.DeletedAtIsNil())
	if useLock {
		q = q.ForUpdate()
	}
	row, err := q.Select(agent.FieldAppliedConfig, agent.FieldStateVersion).Only(ctx)
	if err != nil {
		return 0, false, mapError(err)
	}
	if row.AppliedConfig == "" {
		return 0, false, store.ErrTemplatePinChanged
	}
	// Edit the raw JSON object, as ClearAgentRuntimeTarget does, so every
	// other stored key is kept exactly as written.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row.AppliedConfig), &raw); err != nil {
		return 0, false, fmt.Errorf("set template pin for agent %s: %w", uid, err)
	}
	var gotID, gotHash string
	if v, ok := raw["templateId"]; ok {
		if err := json.Unmarshal(v, &gotID); err != nil {
			return 0, false, fmt.Errorf("set template pin for agent %s: %w", uid, err)
		}
	}
	if v, ok := raw["templateHash"]; ok {
		if err := json.Unmarshal(v, &gotHash); err != nil {
			return 0, false, fmt.Errorf("set template pin for agent %s: %w", uid, err)
		}
	}
	if gotID != templateID || gotHash != expectedHash {
		return 0, false, store.ErrTemplatePinChanged
	}
	if newHash == "" {
		delete(raw, "templateHash")
	} else {
		enc, err := json.Marshal(newHash)
		if err != nil {
			return 0, false, err
		}
		raw["templateHash"] = enc
	}
	updated, err := json.Marshal(raw)
	if err != nil {
		return 0, false, err
	}
	newVersion := row.StateVersion + 1
	n, err := tx.Agent.Update().
		Where(
			agent.IDEQ(uid),
			agent.DeletedAtIsNil(),
			agent.AppliedConfigEQ(row.AppliedConfig),
			agent.StateVersionEQ(row.StateVersion),
		).
		SetAppliedConfig(string(updated)).
		SetStateVersion(newVersion).
		Save(ctx)
	if err != nil {
		return 0, false, mapError(err)
	}
	if n == 0 {
		return 0, false, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return newVersion, true, nil
}

// GetLegacyRuntimeBrokerByName implements store.ProjectStore.
func (s *ProjectStore) GetLegacyRuntimeBrokerByName(ctx context.Context, name string) (*store.RuntimeBroker, error) {
	b, err := s.client.RuntimeBroker.Query().
		Where(runtimebroker.NameEqualFold(name), runtimebroker.RuntimeTargetIDIsNil()).
		Order(ent.Asc(runtimebroker.FieldCreated), ent.Asc(runtimebroker.FieldID)).
		First(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return entBrokerToStore(b), nil
}

// SetRuntimeBrokerTarget implements store.ProjectStore. It updates only the
// display name of an already-flat row with the same target ID and type.
func (s *ProjectStore) SetRuntimeBrokerTarget(ctx context.Context, brokerID string, desc api.RuntimeTargetDescriptor) (*store.RuntimeBroker, error) {
	uid, err := parseUUID(brokerID)
	if err != nil {
		return nil, err
	}
	cur, err := s.client.RuntimeBroker.Get(ctx, uid)
	if err != nil {
		return nil, mapError(err)
	}
	if cur.RuntimeTargetID == nil || *cur.RuntimeTargetID == "" {
		return nil, store.ErrRuntimeBrokerNotFlat
	}
	if *cur.RuntimeTargetID != desc.ID || cur.RuntimeTargetType != desc.Type {
		return nil, store.ErrRuntimeTargetChanged
	}
	affected, err := s.client.RuntimeBroker.Update().
		Where(runtimebroker.IDEQ(uid),
			runtimebroker.RuntimeTargetIDEQ(desc.ID),
			runtimebroker.RuntimeTargetTypeEQ(desc.Type)).
		SetRuntimeTargetDisplayName(desc.DisplayName).
		Save(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	if affected == 0 {
		return nil, store.ErrRuntimeTargetChanged
	}
	return s.GetRuntimeBroker(ctx, brokerID)
}
