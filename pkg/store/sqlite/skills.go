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
	"fmt"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// CreateSkill creates a new skill record.
func (s *SQLiteStore) CreateSkill(ctx context.Context, skill *store.Skill) error {
	return fmt.Errorf("CreateSkill: %w", store.ErrNotFound) // TODO: implement
}

// GetSkill retrieves a skill by ID.
func (s *SQLiteStore) GetSkill(ctx context.Context, id string) (*store.Skill, error) {
	return nil, fmt.Errorf("GetSkill: %w", store.ErrNotFound) // TODO: implement
}

// GetSkillByName retrieves a skill by name, scope, and scopeID.
func (s *SQLiteStore) GetSkillByName(ctx context.Context, name, scope, scopeID string) (*store.Skill, error) {
	return nil, fmt.Errorf("GetSkillByName: %w", store.ErrNotFound) // TODO: implement
}

// UpdateSkill updates an existing skill.
func (s *SQLiteStore) UpdateSkill(ctx context.Context, skill *store.Skill) error {
	return fmt.Errorf("UpdateSkill: %w", store.ErrNotFound) // TODO: implement
}

// DeleteSkill removes a skill by ID and all associated versions.
func (s *SQLiteStore) DeleteSkill(ctx context.Context, id string) error {
	return fmt.Errorf("DeleteSkill: %w", store.ErrNotFound) // TODO: implement
}

// ListSkills returns skills matching the filter criteria.
func (s *SQLiteStore) ListSkills(ctx context.Context, filter store.SkillFilter, opts store.ListOptions) (*store.ListResult[store.Skill], error) {
	return &store.ListResult[store.Skill]{}, nil // TODO: implement
}

// CreateSkillVersion creates a new version for a skill.
func (s *SQLiteStore) CreateSkillVersion(ctx context.Context, version *store.SkillVersion) error {
	return fmt.Errorf("CreateSkillVersion: %w", store.ErrNotFound) // TODO: implement
}

// GetSkillVersion retrieves a skill version by ID.
func (s *SQLiteStore) GetSkillVersion(ctx context.Context, id string) (*store.SkillVersion, error) {
	return nil, fmt.Errorf("GetSkillVersion: %w", store.ErrNotFound) // TODO: implement
}

// GetSkillVersionByNumber retrieves a specific version of a skill.
func (s *SQLiteStore) GetSkillVersionByNumber(ctx context.Context, skillID, version string) (*store.SkillVersion, error) {
	return nil, fmt.Errorf("GetSkillVersionByNumber: %w", store.ErrNotFound) // TODO: implement
}

// ListSkillVersions returns all versions for a skill.
func (s *SQLiteStore) ListSkillVersions(ctx context.Context, skillID string) ([]store.SkillVersion, error) {
	return nil, nil // TODO: implement
}

// DeleteSkillVersion removes a skill version by ID.
func (s *SQLiteStore) DeleteSkillVersion(ctx context.Context, id string) error {
	return fmt.Errorf("DeleteSkillVersion: %w", store.ErrNotFound) // TODO: implement
}
