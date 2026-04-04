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

package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func (s *SQLiteStore) CreateGCPServiceAccount(ctx context.Context, sa *store.GCPServiceAccount) error {
	if sa.CreatedAt.IsZero() {
		sa.CreatedAt = time.Now()
	}

	scopesStr := strings.Join(sa.DefaultScopes, ",")

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gcp_service_accounts (id, scope, scope_id, email, project_id, display_name, default_scopes, verified, verified_at, created_by, created_at, managed, managed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sa.ID, sa.Scope, sa.ScopeID, sa.Email, sa.ProjectID, sa.DisplayName,
		scopesStr, sa.Verified, nullableTime(sa.VerifiedAt), sa.CreatedBy, sa.CreatedAt,
		sa.Managed, sa.ManagedBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *SQLiteStore) GetGCPServiceAccount(ctx context.Context, id string) (*store.GCPServiceAccount, error) {
	var sa store.GCPServiceAccount
	var scopesStr string
	var verifiedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT id, scope, scope_id, email, project_id, display_name, default_scopes, verified, verified_at, created_by, created_at, managed, managed_by
		FROM gcp_service_accounts WHERE id = ?`, id,
	).Scan(&sa.ID, &sa.Scope, &sa.ScopeID, &sa.Email, &sa.ProjectID, &sa.DisplayName,
		&scopesStr, &sa.Verified, &verifiedAt, &sa.CreatedBy, &sa.CreatedAt,
		&sa.Managed, &sa.ManagedBy,
	)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if scopesStr != "" {
		sa.DefaultScopes = strings.Split(scopesStr, ",")
	}
	if verifiedAt.Valid {
		sa.VerifiedAt = verifiedAt.Time
	}

	return &sa, nil
}

func (s *SQLiteStore) UpdateGCPServiceAccount(ctx context.Context, sa *store.GCPServiceAccount) error {
	scopesStr := strings.Join(sa.DefaultScopes, ",")

	result, err := s.db.ExecContext(ctx, `
		UPDATE gcp_service_accounts
		SET email = ?, project_id = ?, display_name = ?, default_scopes = ?, verified = ?, verified_at = ?, managed = ?, managed_by = ?
		WHERE id = ?`,
		sa.Email, sa.ProjectID, sa.DisplayName, scopesStr, sa.Verified, nullableTime(sa.VerifiedAt),
		sa.Managed, sa.ManagedBy, sa.ID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) DeleteGCPServiceAccount(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gcp_service_accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListGCPServiceAccounts(ctx context.Context, filter store.GCPServiceAccountFilter) ([]store.GCPServiceAccount, error) {
	query := `SELECT id, scope, scope_id, email, project_id, display_name, default_scopes, verified, verified_at, created_by, created_at, managed, managed_by FROM gcp_service_accounts WHERE 1=1`
	var args []interface{}

	if filter.Scope != "" {
		query += ` AND scope = ?`
		args = append(args, filter.Scope)
	}
	if filter.ScopeID != "" {
		query += ` AND scope_id = ?`
		args = append(args, filter.ScopeID)
	}
	if filter.Email != "" {
		query += ` AND email = ?`
		args = append(args, filter.Email)
	}
	if filter.Managed != nil {
		query += ` AND managed = ?`
		args = append(args, *filter.Managed)
	}

	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []store.GCPServiceAccount
	for rows.Next() {
		var sa store.GCPServiceAccount
		var scopesStr string
		var verifiedAt sql.NullTime

		if err := rows.Scan(&sa.ID, &sa.Scope, &sa.ScopeID, &sa.Email, &sa.ProjectID, &sa.DisplayName,
			&scopesStr, &sa.Verified, &verifiedAt, &sa.CreatedBy, &sa.CreatedAt,
			&sa.Managed, &sa.ManagedBy,
		); err != nil {
			return nil, err
		}

		if scopesStr != "" {
			sa.DefaultScopes = strings.Split(scopesStr, ",")
		}
		if verifiedAt.Valid {
			sa.VerifiedAt = verifiedAt.Time
		}

		results = append(results, sa)
	}

	return results, rows.Err()
}

func (s *SQLiteStore) CountGCPServiceAccounts(ctx context.Context, filter store.GCPServiceAccountFilter) (int, error) {
	query := `SELECT COUNT(*) FROM gcp_service_accounts WHERE 1=1`
	var args []interface{}

	if filter.Scope != "" {
		query += ` AND scope = ?`
		args = append(args, filter.Scope)
	}
	if filter.ScopeID != "" {
		query += ` AND scope_id = ?`
		args = append(args, filter.ScopeID)
	}
	if filter.Email != "" {
		query += ` AND email = ?`
		args = append(args, filter.Email)
	}
	if filter.Managed != nil {
		query += ` AND managed = ?`
		args = append(args, *filter.Managed)
	}

	var count int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
