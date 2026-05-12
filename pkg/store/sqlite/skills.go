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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ============================================================================
// Skill Operations
// ============================================================================

// CreateSkill creates a new skill record.
func (s *SQLiteStore) CreateSkill(ctx context.Context, skill *store.Skill) error {
	if skill.ID == "" || skill.Name == "" {
		return store.ErrInvalidInput
	}

	now := time.Now()
	if skill.Created.IsZero() {
		skill.Created = now
	}
	if skill.Updated.IsZero() {
		skill.Updated = now
	}
	if skill.Status == "" {
		skill.Status = store.SkillStatusDraft
	}
	if skill.Scope == "" {
		skill.Scope = store.SkillScopeGlobal
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO skills (
			id, name, display_name, description,
			scope, scope_id, status, latest_version,
			labels, annotations,
			owner_id, created_by, updated_by,
			created, updated
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		skill.ID, skill.Name, skill.DisplayName, skill.Description,
		skill.Scope, skill.ScopeID, skill.Status, skill.LatestVersion,
		marshalJSON(skill.Labels), marshalJSON(skill.Annotations),
		skill.OwnerID, skill.CreatedBy, skill.UpdatedBy,
		skill.Created, skill.Updated,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// GetSkill retrieves a skill by ID.
func (s *SQLiteStore) GetSkill(ctx context.Context, id string) (*store.Skill, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, display_name, description,
			scope, scope_id, status, latest_version,
			labels, annotations,
			owner_id, created_by, updated_by,
			created, updated
		FROM skills WHERE id = ?
	`, id)

	skill, err := scanSkill(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return skill, nil
}

// GetSkillByName retrieves a skill by name, scope, and scopeID.
func (s *SQLiteStore) GetSkillByName(ctx context.Context, name, scope, scopeID string) (*store.Skill, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, display_name, description,
			scope, scope_id, status, latest_version,
			labels, annotations,
			owner_id, created_by, updated_by,
			created, updated
		FROM skills WHERE name = ? AND scope = ? AND scope_id = ?
	`, name, scope, scopeID)

	skill, err := scanSkill(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return skill, nil
}

// UpdateSkill updates an existing skill.
func (s *SQLiteStore) UpdateSkill(ctx context.Context, skill *store.Skill) error {
	skill.Updated = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE skills SET
			name = ?, display_name = ?, description = ?,
			scope = ?, scope_id = ?, status = ?, latest_version = ?,
			labels = ?, annotations = ?,
			owner_id = ?, updated_by = ?,
			updated = ?
		WHERE id = ?
	`,
		skill.Name, skill.DisplayName, skill.Description,
		skill.Scope, skill.ScopeID, skill.Status, skill.LatestVersion,
		marshalJSON(skill.Labels), marshalJSON(skill.Annotations),
		skill.OwnerID, skill.UpdatedBy,
		skill.Updated, skill.ID,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return store.ErrAlreadyExists
		}
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteSkill removes a skill by ID and all associated versions.
// Versions are cascade-deleted by the foreign key constraint.
func (s *SQLiteStore) DeleteSkill(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM skills WHERE id = ?", id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListSkills returns skills matching the filter criteria.
func (s *SQLiteStore) ListSkills(ctx context.Context, filter store.SkillFilter, opts store.ListOptions) (*store.ListResult[store.Skill], error) {
	var conditions []string
	var args []interface{}

	if filter.Name != "" {
		conditions = append(conditions, "name = ?")
		args = append(args, filter.Name)
	}
	if filter.Scope != "" {
		conditions = append(conditions, "scope = ?")
		args = append(args, filter.Scope)
	}
	if filter.ScopeID != "" {
		conditions = append(conditions, "scope_id = ?")
		args = append(args, filter.ScopeID)
	}
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.OwnerID != "" {
		conditions = append(conditions, "owner_id = ?")
		args = append(args, filter.OwnerID)
	}
	if filter.Search != "" {
		conditions = append(conditions, "(name LIKE ? OR description LIKE ?)")
		searchTerm := "%" + filter.Search + "%"
		args = append(args, searchTerm, searchTerm)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count.
	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM skills %s", whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	query := fmt.Sprintf(`
		SELECT id, name, display_name, description,
			scope, scope_id, status, latest_version,
			labels, annotations,
			owner_id, created_by, updated_by,
			created, updated
		FROM skills %s
		ORDER BY created DESC
		LIMIT ?
	`, whereClause)

	queryArgs := append(args, limit+1) //nolint:gocritic

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	skills, err := scanSkills(rows)
	if err != nil {
		return nil, err
	}

	result := &store.ListResult[store.Skill]{
		TotalCount: totalCount,
	}

	if len(skills) > limit {
		result.Items = skills[:limit]
		result.NextCursor = skills[limit-1].ID
	} else {
		result.Items = skills
	}

	return result, nil
}

// ============================================================================
// Skill Version Operations
// ============================================================================

// CreateSkillVersion creates a new version for a skill.
func (s *SQLiteStore) CreateSkillVersion(ctx context.Context, version *store.SkillVersion) error {
	if version.ID == "" || version.SkillID == "" || version.Version == "" {
		return store.ErrInvalidInput
	}

	now := time.Now()
	if version.Created.IsZero() {
		version.Created = now
	}
	if version.Status == "" {
		version.Status = store.SkillVersionStatusDraft
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO skill_versions (
			id, skill_id, version, content_hash,
			files, status, changelog, created_by, created
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		version.ID, version.SkillID, version.Version, version.ContentHash,
		marshalJSON(version.Files), version.Status, version.Changelog,
		version.CreatedBy, version.Created,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return store.ErrAlreadyExists
		}
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return fmt.Errorf("skill %s does not exist: %w", version.SkillID, store.ErrNotFound)
		}
		return err
	}
	return nil
}

// GetSkillVersion retrieves a skill version by ID.
func (s *SQLiteStore) GetSkillVersion(ctx context.Context, id string) (*store.SkillVersion, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, skill_id, version, content_hash,
			files, status, changelog, created_by, created
		FROM skill_versions WHERE id = ?
	`, id)

	version, err := scanSkillVersion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return version, nil
}

// GetSkillVersionByNumber retrieves a specific version of a skill.
func (s *SQLiteStore) GetSkillVersionByNumber(ctx context.Context, skillID, version string) (*store.SkillVersion, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, skill_id, version, content_hash,
			files, status, changelog, created_by, created
		FROM skill_versions WHERE skill_id = ? AND version = ?
	`, skillID, version)

	sv, err := scanSkillVersion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return sv, nil
}

// ListSkillVersions returns all versions for a skill.
func (s *SQLiteStore) ListSkillVersions(ctx context.Context, skillID string) ([]store.SkillVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, skill_id, version, content_hash,
			files, status, changelog, created_by, created
		FROM skill_versions WHERE skill_id = ?
		ORDER BY created DESC
	`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSkillVersions(rows)
}

// DeleteSkillVersion removes a skill version by ID.
func (s *SQLiteStore) DeleteSkillVersion(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM skill_versions WHERE id = ?", id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ============================================================================
// Scan Helpers
// ============================================================================

// rowScanner abstracts *sql.Row and *sql.Rows for shared scanning logic.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// scanSkillFromRow scans a single skill row from any scanner.
func scanSkillFromRow(scanner rowScanner) (*store.Skill, error) {
	skill := &store.Skill{}
	var labels, annotations string

	err := scanner.Scan(
		&skill.ID, &skill.Name, &skill.DisplayName, &skill.Description,
		&skill.Scope, &skill.ScopeID, &skill.Status, &skill.LatestVersion,
		&labels, &annotations,
		&skill.OwnerID, &skill.CreatedBy, &skill.UpdatedBy,
		&skill.Created, &skill.Updated,
	)
	if err != nil {
		return nil, err
	}

	unmarshalJSON(labels, &skill.Labels)
	unmarshalJSON(annotations, &skill.Annotations)

	return skill, nil
}

// scanSkill scans a single skill from a *sql.Row.
func scanSkill(row *sql.Row) (*store.Skill, error) {
	return scanSkillFromRow(row)
}

// scanSkills scans rows into a Skill slice.
func scanSkills(rows *sql.Rows) ([]store.Skill, error) {
	var skills []store.Skill
	for rows.Next() {
		skill, err := scanSkillFromRow(rows)
		if err != nil {
			return nil, err
		}
		skills = append(skills, *skill)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return skills, nil
}

// scanSkillVersionFromRow scans a single skill version from any scanner.
func scanSkillVersionFromRow(scanner rowScanner) (*store.SkillVersion, error) {
	sv := &store.SkillVersion{}
	var filesJSON string

	err := scanner.Scan(
		&sv.ID, &sv.SkillID, &sv.Version, &sv.ContentHash,
		&filesJSON, &sv.Status, &sv.Changelog, &sv.CreatedBy, &sv.Created,
	)
	if err != nil {
		return nil, err
	}

	unmarshalJSON(filesJSON, &sv.Files)

	return sv, nil
}

// scanSkillVersion scans a single skill version from a *sql.Row.
func scanSkillVersion(row *sql.Row) (*store.SkillVersion, error) {
	return scanSkillVersionFromRow(row)
}

// scanSkillVersions scans rows into a SkillVersion slice.
func scanSkillVersions(rows *sql.Rows) ([]store.SkillVersion, error) {
	var versions []store.SkillVersion
	for rows.Next() {
		sv, err := scanSkillVersionFromRow(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, *sv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return versions, nil
}
