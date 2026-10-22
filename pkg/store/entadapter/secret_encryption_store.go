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
	"fmt"
	"time"

	"entgo.io/ent/dialect"

	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	enthubencryptionmigration "github.com/GoogleCloudPlatform/scion/pkg/ent/hubencryptionmigration"
	enthubencryptionpolicy "github.com/GoogleCloudPlatform/scion/pkg/ent/hubencryptionpolicy"
	entsecret "github.com/GoogleCloudPlatform/scion/pkg/ent/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Encryption policy fence and migration primitives (ptone/scion#1530 §6).
//
// Locking: on PostgreSQL every fenced payload write reads the policy row
// FOR SHARE and every policy transition reads it FOR UPDATE, so a
// transition waits for in-flight writes and later writes see the new
// policy. SQLite serializes write transactions (one connection), so the
// policy read and the write in one transaction cannot interleave with a
// transition.

// encryptionPolicyID is the singleton policy row's ID.
const encryptionPolicyID = 1

var (
	_ store.SecretStore           = (*SecretStore)(nil)
	_ store.SecretEncryptionStore = (*SecretStore)(nil)
)

func isPostgres(c *ent.Client) bool { return c.Driver().Dialect() == dialect.Postgres }

// loadEncryptionPolicyForWrite reads the policy for a fenced write on c (a
// transaction client), share-locked on PostgreSQL. A nil policy means no
// policy row: the fence is off.
func loadEncryptionPolicyForWrite(ctx context.Context, c *ent.Client) (*store.EncryptionPolicy, error) {
	q := c.HubEncryptionPolicy.Query().Where(enthubencryptionpolicy.IDEQ(encryptionPolicyID))
	if isPostgres(c) {
		q = q.ForShare()
	}
	row, err := q.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, mapError(err)
	}
	return entPolicyToStore(row), nil
}

// fenceValueChange applies the policy fence to a write of newValue over
// the existing row (key, scope, scopeID). newRef is the SecretRef the
// write leaves on the row (nil: the existing one is kept). An unchanged
// value is not a payload change and is not fenced. It returns
// store.ErrNotFound when the row does not exist.
func fenceValueChange(ctx context.Context, c *ent.Client, key, scope, scopeID, newValue string, newRef *string) error {
	existing, err := getSecretRow(ctx, c, key, scope, scopeID)
	if err != nil {
		return err
	}
	if existing.EncryptedValue == newValue {
		return nil
	}
	ref := existing.SecretRef
	if newRef != nil {
		ref = *newRef
	}
	policy, err := loadEncryptionPolicyForWrite(ctx, c)
	if err != nil {
		return err
	}
	if !policy.AllowsWrite(newValue, ref) {
		return store.ErrEncryptionPolicyMismatch
	}
	return nil
}

// ScanSecretPayloads returns up to limit rows with ID > afterID in
// ascending ID order, across every scope and scope ID.
func (s *SecretStore) ScanSecretPayloads(ctx context.Context, afterID string, limit int) ([]store.SecretPayload, error) {
	if limit < 1 || limit > store.MaxSecretPayloadScan {
		return nil, fmt.Errorf("%w: scan limit must be between 1 and %d", store.ErrInvalidInput, store.MaxSecretPayloadScan)
	}
	q := s.client.Secret.Query()
	if afterID != "" {
		uid, err := parseUUID(afterID)
		if err != nil {
			return nil, err
		}
		q = q.Where(entsecret.IDGT(uid))
	}
	rows, err := q.Order(ent.Asc(entsecret.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]store.SecretPayload, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.SecretPayload{
			ID:             r.ID.String(),
			Key:            r.Key,
			Scope:          r.Scope,
			ScopeID:        r.ScopeID,
			Version:        r.Version,
			EncryptedValue: r.EncryptedValue,
			SecretRef:      r.SecretRef,
		})
	}
	return out, nil
}

// ReplaceSecretCiphertext replaces only encrypted_value of the row with
// id, while its version and its exact stored bytes still match. The
// updated timestamp is written back unchanged, so the row's metadata is
// preserved exactly.
func (s *SecretStore) ReplaceSecretCiphertext(ctx context.Context, id string, expectedVersion int, expectedCiphertext, replacement string) (bool, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return false, err
	}
	applied := false
	err = runInEntTx(ctx, s.client, s.inTx, func(c *ent.Client) error {
		q := c.Secret.Query().Where(entsecret.IDEQ(uid))
		if isPostgres(c) {
			q = q.ForUpdate()
		}
		row, err := q.Only(ctx)
		if ent.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return mapError(err)
		}
		// Exact byte comparison in Go as well as in the predicate below,
		// so no database collation can match different bytes.
		if row.Version != expectedVersion || row.EncryptedValue != expectedCiphertext {
			return nil
		}
		policy, err := loadEncryptionPolicyForWrite(ctx, c)
		if err != nil {
			return err
		}
		if replacement != expectedCiphertext && !policy.AllowsWrite(replacement, row.SecretRef) {
			return store.ErrEncryptionPolicyMismatch
		}
		n, err := c.Secret.Update().
			Where(
				entsecret.IDEQ(uid),
				entsecret.VersionEQ(expectedVersion),
				entsecret.EncryptedValueEQ(expectedCiphertext),
			).
			SetEncryptedValue(replacement).
			SetUpdated(row.Updated).
			Save(ctx)
		if err != nil {
			return mapError(err)
		}
		applied = n == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func entPolicyToStore(r *ent.HubEncryptionPolicy) *store.EncryptionPolicy {
	p := &store.EncryptionPolicy{
		Generation:       r.Generation,
		MinReaderVersion: r.MinReaderVersion,
		AllowedWriteKIDs: append([]string{}, r.AllowedWriteKids...),
		RequiredReadKIDs: append([]string{}, r.RequiredReadKids...),
		State:            store.EncryptionPolicyState(r.State),
		UpdatedAt:        r.UpdatedAt.UTC(),
	}
	if r.MigrationID != nil {
		p.MigrationID = *r.MigrationID
	}
	return p
}

// GetEncryptionPolicy returns the policy or store.ErrNotFound.
func (s *SecretStore) GetEncryptionPolicy(ctx context.Context) (*store.EncryptionPolicy, error) {
	row, err := s.client.HubEncryptionPolicy.Get(ctx, encryptionPolicyID)
	if err != nil {
		return nil, mapError(err)
	}
	return entPolicyToStore(row), nil
}

// UpdateEncryptionPolicy is the generation compare-and-swap of the policy
// row, exclusive-locked on PostgreSQL.
func (s *SecretStore) UpdateEncryptionPolicy(ctx context.Context, expectedGeneration int64, next store.EncryptionPolicy) (bool, error) {
	if err := next.Validate(); err != nil {
		return false, err
	}
	if expectedGeneration < 0 || next.Generation != expectedGeneration+1 {
		return false, fmt.Errorf("%w: the next encryption policy generation must be the expected generation plus one", store.ErrInvalidInput)
	}
	if next.MigrationID != "" {
		if _, err := parseUUID(next.MigrationID); err != nil {
			return false, err
		}
	}
	applied := false
	err := runInEntTx(ctx, s.client, s.inTx, func(c *ent.Client) error {
		q := c.HubEncryptionPolicy.Query().Where(enthubencryptionpolicy.IDEQ(encryptionPolicyID))
		if isPostgres(c) {
			q = q.ForUpdate()
		}
		cur, err := q.Only(ctx)
		now := time.Now().UTC()
		switch {
		case ent.IsNotFound(err):
			if expectedGeneration != 0 {
				return nil
			}
			create := c.HubEncryptionPolicy.Create().
				SetID(encryptionPolicyID).
				SetGeneration(next.Generation).
				SetMinReaderVersion(next.MinReaderVersion).
				SetAllowedWriteKids(append([]string{}, next.AllowedWriteKIDs...)).
				SetRequiredReadKids(append([]string{}, next.RequiredReadKIDs...)).
				SetState(enthubencryptionpolicy.State(next.State)).
				SetUpdatedAt(now)
			if next.MigrationID != "" {
				create.SetMigrationID(next.MigrationID)
			}
			if _, err := create.Save(ctx); err != nil {
				if ent.IsConstraintError(err) {
					return nil // a concurrent creator won
				}
				return mapError(err)
			}
			applied = true
			return nil
		case err != nil:
			return mapError(err)
		}
		if cur.Generation != expectedGeneration {
			return nil
		}
		upd := c.HubEncryptionPolicy.UpdateOneID(encryptionPolicyID).
			Where(enthubencryptionpolicy.GenerationEQ(expectedGeneration)).
			SetGeneration(next.Generation).
			SetMinReaderVersion(next.MinReaderVersion).
			SetAllowedWriteKids(append([]string{}, next.AllowedWriteKIDs...)).
			SetRequiredReadKids(append([]string{}, next.RequiredReadKIDs...)).
			SetState(enthubencryptionpolicy.State(next.State)).
			SetUpdatedAt(now)
		if next.MigrationID != "" {
			upd.SetMigrationID(next.MigrationID)
		} else {
			upd.ClearMigrationID()
		}
		if _, err := upd.Save(ctx); err != nil {
			if ent.IsNotFound(err) {
				return nil
			}
			return mapError(err)
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func entMigrationToStore(r *ent.HubEncryptionMigration) *store.EncryptionMigration {
	return &store.EncryptionMigration{
		ID:                    r.ID.String(),
		SourceGeneration:      r.SourceGeneration,
		TargetKID:             r.TargetKid,
		State:                 store.EncryptionMigrationState(r.State),
		LastScannedID:         r.LastScannedID,
		PlaintextReadAdmitted: r.PlaintextReadAdmitted,
		Scanned:               r.Scanned,
		Rewritten:             r.Rewritten,
		Failures:              r.Failures,
		Created:               r.Created.UTC(),
		Updated:               r.Updated.UTC(),
	}
}

// CreateEncryptionMigration stores a new migration record.
func (s *SecretStore) CreateEncryptionMigration(ctx context.Context, m *store.EncryptionMigration) error {
	if err := m.Validate(); err != nil {
		return err
	}
	uid, err := parseUUID(m.ID)
	if err != nil {
		return err
	}
	admitted := m.PlaintextReadAdmitted && m.State != store.EncryptionMigrationVerified
	now := time.Now().UTC()
	row, err := s.client.HubEncryptionMigration.Create().
		SetID(uid).
		SetSourceGeneration(m.SourceGeneration).
		SetTargetKid(m.TargetKID).
		SetState(enthubencryptionmigration.State(m.State)).
		SetLastScannedID(m.LastScannedID).
		SetPlaintextReadAdmitted(admitted).
		SetScanned(m.Scanned).
		SetRewritten(m.Rewritten).
		SetFailures(m.Failures).
		SetCreated(now).
		SetUpdated(now).
		Save(ctx)
	if err != nil {
		return mapError(err)
	}
	*m = *entMigrationToStore(row)
	return nil
}

// GetEncryptionMigration returns the record or store.ErrNotFound.
func (s *SecretStore) GetEncryptionMigration(ctx context.Context, id string) (*store.EncryptionMigration, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	row, err := s.client.HubEncryptionMigration.Get(ctx, uid)
	if err != nil {
		return nil, mapError(err)
	}
	return entMigrationToStore(row), nil
}

// UpdateEncryptionMigration updates the mutable fields while the stored
// state is expectedState. A verified record is terminal: it stays verified
// and never regains plaintext admission.
func (s *SecretStore) UpdateEncryptionMigration(ctx context.Context, m *store.EncryptionMigration, expectedState store.EncryptionMigrationState) (bool, error) {
	if err := m.Validate(); err != nil {
		return false, err
	}
	if expectedState == store.EncryptionMigrationVerified && (m.State != store.EncryptionMigrationVerified || m.PlaintextReadAdmitted) {
		return false, fmt.Errorf("%w: a verified encryption migration cannot be resumed or re-admit plaintext", store.ErrInvalidInput)
	}
	uid, err := parseUUID(m.ID)
	if err != nil {
		return false, err
	}
	admitted := m.PlaintextReadAdmitted && m.State != store.EncryptionMigrationVerified
	applied := false
	err = runInEntTx(ctx, s.client, s.inTx, func(c *ent.Client) error {
		row, err := c.HubEncryptionMigration.UpdateOneID(uid).
			Where(enthubencryptionmigration.StateEQ(enthubencryptionmigration.State(expectedState))).
			SetState(enthubencryptionmigration.State(m.State)).
			SetLastScannedID(m.LastScannedID).
			SetPlaintextReadAdmitted(admitted).
			SetScanned(m.Scanned).
			SetRewritten(m.Rewritten).
			SetFailures(m.Failures).
			SetUpdated(time.Now().UTC()).
			Save(ctx)
		if ent.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return mapError(err)
		}
		*m = *entMigrationToStore(row)
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}
