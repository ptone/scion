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

//go:build !no_sqlite

package sqlite

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Skill CRUD Tests
// ============================================================================

func TestSkillCreate(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:          api.NewUUID(),
		Name:        "code-review",
		DisplayName: "Code Review",
		Description: "Automated code review skill",
		Scope:       store.SkillScopeGlobal,
		ScopeID:     "",
		Status:      store.SkillStatusActive,
		OwnerID:     "user-1",
		CreatedBy:   "user-1",
		Labels:      map[string]string{"category": "quality"},
	}

	err := s.CreateSkill(ctx, skill)
	require.NoError(t, err)
	assert.False(t, skill.Created.IsZero())
	assert.False(t, skill.Updated.IsZero())
}

func TestSkillCreateDefaults(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "minimal-skill",
	}

	err := s.CreateSkill(ctx, skill)
	require.NoError(t, err)
	assert.Equal(t, store.SkillStatusDraft, skill.Status)
	assert.Equal(t, store.SkillScopeGlobal, skill.Scope)
	assert.False(t, skill.Created.IsZero())
}

func TestSkillCreateInvalidInput(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Missing ID
	err := s.CreateSkill(ctx, &store.Skill{Name: "no-id"})
	assert.ErrorIs(t, err, store.ErrInvalidInput)

	// Missing Name
	err = s.CreateSkill(ctx, &store.Skill{ID: api.NewUUID()})
	assert.ErrorIs(t, err, store.ErrInvalidInput)
}

func TestSkillCreateDuplicate(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:    api.NewUUID(),
		Name:  "dup-skill",
		Scope: store.SkillScopeGlobal,
	}

	err := s.CreateSkill(ctx, skill)
	require.NoError(t, err)

	// Same name+scope+scopeID should fail
	dup := &store.Skill{
		ID:    api.NewUUID(),
		Name:  "dup-skill",
		Scope: store.SkillScopeGlobal,
	}
	err = s.CreateSkill(ctx, dup)
	assert.ErrorIs(t, err, store.ErrAlreadyExists)
}

func TestSkillCreateSameNameDifferentScope(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Same name in different scopes should work
	skill1 := &store.Skill{
		ID:    api.NewUUID(),
		Name:  "shared-skill",
		Scope: store.SkillScopeGlobal,
	}
	require.NoError(t, s.CreateSkill(ctx, skill1))

	skill2 := &store.Skill{
		ID:      api.NewUUID(),
		Name:    "shared-skill",
		Scope:   store.SkillScopeProject,
		ScopeID: "project-123",
	}
	require.NoError(t, s.CreateSkill(ctx, skill2))

	skill3 := &store.Skill{
		ID:      api.NewUUID(),
		Name:    "shared-skill",
		Scope:   store.SkillScopeUser,
		ScopeID: "user-456",
	}
	require.NoError(t, s.CreateSkill(ctx, skill3))
}

func TestSkillGet(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	id := api.NewUUID()
	skill := &store.Skill{
		ID:          id,
		Name:        "get-skill",
		DisplayName: "Get Skill",
		Description: "A skill for testing get",
		Scope:       store.SkillScopeProject,
		ScopeID:     "proj-1",
		Status:      store.SkillStatusActive,
		OwnerID:     "user-1",
		CreatedBy:   "user-1",
		UpdatedBy:   "user-2",
		Labels:      map[string]string{"env": "test"},
		Annotations: map[string]string{"note": "testing"},
	}

	require.NoError(t, s.CreateSkill(ctx, skill))

	got, err := s.GetSkill(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, "get-skill", got.Name)
	assert.Equal(t, "Get Skill", got.DisplayName)
	assert.Equal(t, "A skill for testing get", got.Description)
	assert.Equal(t, store.SkillScopeProject, got.Scope)
	assert.Equal(t, "proj-1", got.ScopeID)
	assert.Equal(t, store.SkillStatusActive, got.Status)
	assert.Equal(t, "user-1", got.OwnerID)
	assert.Equal(t, "user-1", got.CreatedBy)
	assert.Equal(t, "user-2", got.UpdatedBy)
	assert.Equal(t, "test", got.Labels["env"])
	assert.Equal(t, "testing", got.Annotations["note"])
}

func TestSkillGetNotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	_, err := s.GetSkill(ctx, "nonexistent-id")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillGetByName(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:      api.NewUUID(),
		Name:    "lookup-skill",
		Scope:   store.SkillScopeProject,
		ScopeID: "proj-abc",
		Status:  store.SkillStatusActive,
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	got, err := s.GetSkillByName(ctx, "lookup-skill", store.SkillScopeProject, "proj-abc")
	require.NoError(t, err)
	assert.Equal(t, skill.ID, got.ID)
	assert.Equal(t, "lookup-skill", got.Name)
}

func TestSkillGetByNameNotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	_, err := s.GetSkillByName(ctx, "nonexistent", store.SkillScopeGlobal, "")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillUpdate(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:          api.NewUUID(),
		Name:        "update-skill",
		DisplayName: "Original Name",
		Description: "Original desc",
		Scope:       store.SkillScopeGlobal,
		Status:      store.SkillStatusDraft,
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	// Update fields
	skill.DisplayName = "Updated Name"
	skill.Description = "Updated description"
	skill.Status = store.SkillStatusActive
	skill.LatestVersion = "1.0.0"
	skill.Labels = map[string]string{"updated": "true"}

	err := s.UpdateSkill(ctx, skill)
	require.NoError(t, err)

	got, err := s.GetSkill(ctx, skill.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", got.DisplayName)
	assert.Equal(t, "Updated description", got.Description)
	assert.Equal(t, store.SkillStatusActive, got.Status)
	assert.Equal(t, "1.0.0", got.LatestVersion)
	assert.Equal(t, "true", got.Labels["updated"])
}

func TestSkillUpdateNotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   "nonexistent-id",
		Name: "ghost",
	}
	err := s.UpdateSkill(ctx, skill)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillDelete(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "delete-me",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	err := s.DeleteSkill(ctx, skill.ID)
	require.NoError(t, err)

	// Verify it's gone
	_, err = s.GetSkill(ctx, skill.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillDeleteNotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	err := s.DeleteSkill(ctx, "nonexistent-id")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillDeleteCascadesVersions(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "cascading-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	// Create some versions
	v1 := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "1.0.0",
		Status:  store.SkillVersionStatusPublished,
	}
	v2 := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "2.0.0",
		Status:  store.SkillVersionStatusPublished,
	}
	require.NoError(t, s.CreateSkillVersion(ctx, v1))
	require.NoError(t, s.CreateSkillVersion(ctx, v2))

	// Delete the skill — versions should cascade
	require.NoError(t, s.DeleteSkill(ctx, skill.ID))

	// Verify versions are gone
	_, err := s.GetSkillVersion(ctx, v1.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.GetSkillVersion(ctx, v2.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ============================================================================
// Skill List Tests
// ============================================================================

func TestSkillListEmpty(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	result, err := s.ListSkills(ctx, store.SkillFilter{}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.TotalCount)
	assert.Empty(t, result.Items)
}

func TestSkillListAll(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		skill := &store.Skill{
			ID:   api.NewUUID(),
			Name: "skill-" + api.NewUUID()[:8],
		}
		require.NoError(t, s.CreateSkill(ctx, skill))
	}

	result, err := s.ListSkills(ctx, store.SkillFilter{}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 3, result.TotalCount)
	assert.Len(t, result.Items, 3)
}

func TestSkillListFilterByScope(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Create skills in different scopes
	globalSkill := &store.Skill{
		ID:    api.NewUUID(),
		Name:  "global-skill",
		Scope: store.SkillScopeGlobal,
	}
	projectSkill := &store.Skill{
		ID:      api.NewUUID(),
		Name:    "project-skill",
		Scope:   store.SkillScopeProject,
		ScopeID: "proj-1",
	}
	require.NoError(t, s.CreateSkill(ctx, globalSkill))
	require.NoError(t, s.CreateSkill(ctx, projectSkill))

	result, err := s.ListSkills(ctx, store.SkillFilter{Scope: store.SkillScopeGlobal}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
	assert.Equal(t, "global-skill", result.Items[0].Name)
}

func TestSkillListFilterByStatus(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	active := &store.Skill{
		ID:     api.NewUUID(),
		Name:   "active-skill",
		Status: store.SkillStatusActive,
	}
	archived := &store.Skill{
		ID:     api.NewUUID(),
		Name:   "archived-skill",
		Status: store.SkillStatusArchived,
	}
	require.NoError(t, s.CreateSkill(ctx, active))
	require.NoError(t, s.CreateSkill(ctx, archived))

	result, err := s.ListSkills(ctx, store.SkillFilter{Status: store.SkillStatusActive}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
	assert.Equal(t, "active-skill", result.Items[0].Name)
}

func TestSkillListFilterByOwner(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill1 := &store.Skill{
		ID:      api.NewUUID(),
		Name:    "owner1-skill",
		OwnerID: "owner-1",
	}
	skill2 := &store.Skill{
		ID:      api.NewUUID(),
		Name:    "owner2-skill",
		OwnerID: "owner-2",
	}
	require.NoError(t, s.CreateSkill(ctx, skill1))
	require.NoError(t, s.CreateSkill(ctx, skill2))

	result, err := s.ListSkills(ctx, store.SkillFilter{OwnerID: "owner-1"}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
	assert.Equal(t, "owner1-skill", result.Items[0].Name)
}

func TestSkillListSearch(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill1 := &store.Skill{
		ID:          api.NewUUID(),
		Name:        "security-audit",
		Description: "Perform security audits",
	}
	skill2 := &store.Skill{
		ID:          api.NewUUID(),
		Name:        "code-review",
		Description: "Review code changes",
	}
	require.NoError(t, s.CreateSkill(ctx, skill1))
	require.NoError(t, s.CreateSkill(ctx, skill2))

	// Search by name
	result, err := s.ListSkills(ctx, store.SkillFilter{Search: "security"}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
	assert.Equal(t, "security-audit", result.Items[0].Name)

	// Search by description
	result, err = s.ListSkills(ctx, store.SkillFilter{Search: "Review"}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
	assert.Equal(t, "code-review", result.Items[0].Name)
}

func TestSkillListPagination(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		skill := &store.Skill{
			ID:   api.NewUUID(),
			Name: "page-skill-" + api.NewUUID()[:8],
		}
		require.NoError(t, s.CreateSkill(ctx, skill))
	}

	// Limit to 3
	result, err := s.ListSkills(ctx, store.SkillFilter{}, store.ListOptions{Limit: 3})
	require.NoError(t, err)
	assert.Equal(t, 5, result.TotalCount)
	assert.Len(t, result.Items, 3)
	assert.NotEmpty(t, result.NextCursor)
}

func TestSkillListLimitCapping(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Create one skill to have something
	require.NoError(t, s.CreateSkill(ctx, &store.Skill{
		ID:   api.NewUUID(),
		Name: "limit-test",
	}))

	// Limit > 200 should be capped
	result, err := s.ListSkills(ctx, store.SkillFilter{}, store.ListOptions{Limit: 500})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
}

// ============================================================================
// Skill Version CRUD Tests
// ============================================================================

func TestSkillVersionCreate(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "versioned-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	version := &store.SkillVersion{
		ID:          api.NewUUID(),
		SkillID:     skill.ID,
		Version:     "1.0.0",
		ContentHash: "sha256:abc123",
		Status:      store.SkillVersionStatusPublished,
		Changelog:   "Initial release",
		CreatedBy:   "user-1",
		Files: []store.SkillFile{
			{Path: "SKILL.md", Size: 1024, Hash: "sha256:file1", Mode: "0644"},
			{Path: "scripts/run.sh", Size: 256, Hash: "sha256:file2", Mode: "0755"},
		},
	}

	err := s.CreateSkillVersion(ctx, version)
	require.NoError(t, err)
	assert.False(t, version.Created.IsZero())
}

func TestSkillVersionCreateDefaults(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "default-ver-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	version := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "0.1.0",
	}

	err := s.CreateSkillVersion(ctx, version)
	require.NoError(t, err)
	assert.Equal(t, store.SkillVersionStatusDraft, version.Status)
	assert.False(t, version.Created.IsZero())
}

func TestSkillVersionCreateInvalidInput(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Missing ID
	err := s.CreateSkillVersion(ctx, &store.SkillVersion{SkillID: "x", Version: "1.0.0"})
	assert.ErrorIs(t, err, store.ErrInvalidInput)

	// Missing SkillID
	err = s.CreateSkillVersion(ctx, &store.SkillVersion{ID: api.NewUUID(), Version: "1.0.0"})
	assert.ErrorIs(t, err, store.ErrInvalidInput)

	// Missing Version
	err = s.CreateSkillVersion(ctx, &store.SkillVersion{ID: api.NewUUID(), SkillID: "x"})
	assert.ErrorIs(t, err, store.ErrInvalidInput)
}

func TestSkillVersionCreateDuplicate(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "dup-ver-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	v1 := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "1.0.0",
	}
	require.NoError(t, s.CreateSkillVersion(ctx, v1))

	// Same skill+version should fail
	v2 := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "1.0.0",
	}
	err := s.CreateSkillVersion(ctx, v2)
	assert.ErrorIs(t, err, store.ErrAlreadyExists)
}

func TestSkillVersionCreateForeignKeyFails(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	version := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: "nonexistent-skill",
		Version: "1.0.0",
	}
	err := s.CreateSkillVersion(ctx, version)
	assert.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillVersionGet(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "get-ver-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	versionID := api.NewUUID()
	version := &store.SkillVersion{
		ID:          versionID,
		SkillID:     skill.ID,
		Version:     "1.2.3",
		ContentHash: "sha256:def456",
		Status:      store.SkillVersionStatusPublished,
		Changelog:   "Bug fix",
		CreatedBy:   "user-2",
		Files: []store.SkillFile{
			{Path: "SKILL.md", Size: 512, Hash: "sha256:abc", Mode: "0644"},
		},
	}
	require.NoError(t, s.CreateSkillVersion(ctx, version))

	got, err := s.GetSkillVersion(ctx, versionID)
	require.NoError(t, err)
	assert.Equal(t, versionID, got.ID)
	assert.Equal(t, skill.ID, got.SkillID)
	assert.Equal(t, "1.2.3", got.Version)
	assert.Equal(t, "sha256:def456", got.ContentHash)
	assert.Equal(t, store.SkillVersionStatusPublished, got.Status)
	assert.Equal(t, "Bug fix", got.Changelog)
	assert.Equal(t, "user-2", got.CreatedBy)
	require.Len(t, got.Files, 1)
	assert.Equal(t, "SKILL.md", got.Files[0].Path)
	assert.Equal(t, int64(512), got.Files[0].Size)
}

func TestSkillVersionGetNotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	_, err := s.GetSkillVersion(ctx, "nonexistent-id")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillVersionGetByNumber(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "by-number-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	v1 := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "1.0.0",
		Status:  store.SkillVersionStatusPublished,
	}
	v2 := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "2.0.0",
		Status:  store.SkillVersionStatusPublished,
	}
	require.NoError(t, s.CreateSkillVersion(ctx, v1))
	require.NoError(t, s.CreateSkillVersion(ctx, v2))

	got, err := s.GetSkillVersionByNumber(ctx, skill.ID, "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, v1.ID, got.ID)
	assert.Equal(t, "1.0.0", got.Version)

	got2, err := s.GetSkillVersionByNumber(ctx, skill.ID, "2.0.0")
	require.NoError(t, err)
	assert.Equal(t, v2.ID, got2.ID)
	assert.Equal(t, "2.0.0", got2.Version)
}

func TestSkillVersionGetByNumberNotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "no-such-ver-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	_, err := s.GetSkillVersionByNumber(ctx, skill.ID, "99.0.0")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillVersionList(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "list-ver-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	versions := []string{"1.0.0", "1.1.0", "2.0.0"}
	for _, v := range versions {
		sv := &store.SkillVersion{
			ID:      api.NewUUID(),
			SkillID: skill.ID,
			Version: v,
			Status:  store.SkillVersionStatusPublished,
		}
		require.NoError(t, s.CreateSkillVersion(ctx, sv))
	}

	got, err := s.ListSkillVersions(ctx, skill.ID)
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestSkillVersionListEmpty(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "no-versions-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	got, err := s.ListSkillVersions(ctx, skill.ID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSkillVersionDelete(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "del-ver-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	version := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "1.0.0",
	}
	require.NoError(t, s.CreateSkillVersion(ctx, version))

	err := s.DeleteSkillVersion(ctx, version.ID)
	require.NoError(t, err)

	// Verify it's gone
	_, err = s.GetSkillVersion(ctx, version.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillVersionDeleteNotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	err := s.DeleteSkillVersion(ctx, "nonexistent-id")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ============================================================================
// Integration / Cross-Method Tests
// ============================================================================

func TestSkillFullLifecycle(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// 1. Create a skill
	skillID := api.NewUUID()
	skill := &store.Skill{
		ID:          skillID,
		Name:        "lifecycle-skill",
		DisplayName: "Lifecycle Skill",
		Description: "Full lifecycle test",
		Scope:       store.SkillScopeGlobal,
		Status:      store.SkillStatusDraft,
		OwnerID:     "owner-1",
		CreatedBy:   "owner-1",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	// 2. Add versions
	v1ID := api.NewUUID()
	v1 := &store.SkillVersion{
		ID:          v1ID,
		SkillID:     skillID,
		Version:     "1.0.0",
		ContentHash: "sha256:v1hash",
		Status:      store.SkillVersionStatusPublished,
		Changelog:   "Initial release",
		CreatedBy:   "owner-1",
		Files: []store.SkillFile{
			{Path: "SKILL.md", Size: 1000, Hash: "sha256:f1"},
		},
	}
	require.NoError(t, s.CreateSkillVersion(ctx, v1))

	v2ID := api.NewUUID()
	v2 := &store.SkillVersion{
		ID:          v2ID,
		SkillID:     skillID,
		Version:     "1.1.0",
		ContentHash: "sha256:v2hash",
		Status:      store.SkillVersionStatusPublished,
		Changelog:   "Added new feature",
		CreatedBy:   "owner-1",
		Files: []store.SkillFile{
			{Path: "SKILL.md", Size: 1200, Hash: "sha256:f2"},
			{Path: "scripts/analyze.sh", Size: 300, Hash: "sha256:f3", Mode: "0755"},
		},
	}
	require.NoError(t, s.CreateSkillVersion(ctx, v2))

	// 3. Update skill to active with latest version
	skill.Status = store.SkillStatusActive
	skill.LatestVersion = "1.1.0"
	require.NoError(t, s.UpdateSkill(ctx, skill))

	// 4. Verify updated state
	got, err := s.GetSkill(ctx, skillID)
	require.NoError(t, err)
	assert.Equal(t, store.SkillStatusActive, got.Status)
	assert.Equal(t, "1.1.0", got.LatestVersion)

	// 5. Verify lookup by name
	byName, err := s.GetSkillByName(ctx, "lifecycle-skill", store.SkillScopeGlobal, "")
	require.NoError(t, err)
	assert.Equal(t, skillID, byName.ID)

	// 6. List versions
	versions, err := s.ListSkillVersions(ctx, skillID)
	require.NoError(t, err)
	assert.Len(t, versions, 2)

	// 7. Get specific version by number
	gotV1, err := s.GetSkillVersionByNumber(ctx, skillID, "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "sha256:v1hash", gotV1.ContentHash)
	assert.Len(t, gotV1.Files, 1)

	gotV2, err := s.GetSkillVersionByNumber(ctx, skillID, "1.1.0")
	require.NoError(t, err)
	assert.Equal(t, "sha256:v2hash", gotV2.ContentHash)
	assert.Len(t, gotV2.Files, 2)

	// 8. Delete a specific version
	require.NoError(t, s.DeleteSkillVersion(ctx, v1ID))
	versions, err = s.ListSkillVersions(ctx, skillID)
	require.NoError(t, err)
	assert.Len(t, versions, 1)

	// 9. Delete the skill (cascade should remove remaining version)
	require.NoError(t, s.DeleteSkill(ctx, skillID))
	_, err = s.GetSkill(ctx, skillID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.GetSkillVersion(ctx, v2ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSkillListMultipleFilters(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Create skills with different attributes
	skills := []store.Skill{
		{ID: api.NewUUID(), Name: "sec-audit", Scope: store.SkillScopeGlobal, Status: store.SkillStatusActive, OwnerID: "user-1"},
		{ID: api.NewUUID(), Name: "sec-scan", Scope: store.SkillScopeGlobal, Status: store.SkillStatusActive, OwnerID: "user-2"},
		{ID: api.NewUUID(), Name: "perf-bench", Scope: store.SkillScopeProject, ScopeID: "proj-1", Status: store.SkillStatusActive, OwnerID: "user-1"},
		{ID: api.NewUUID(), Name: "draft-skill", Scope: store.SkillScopeGlobal, Status: store.SkillStatusDraft, OwnerID: "user-1"},
	}
	for i := range skills {
		require.NoError(t, s.CreateSkill(ctx, &skills[i]))
	}

	// Filter by scope + status
	result, err := s.ListSkills(ctx, store.SkillFilter{
		Scope:  store.SkillScopeGlobal,
		Status: store.SkillStatusActive,
	}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, result.TotalCount)

	// Filter by scope + owner
	result, err = s.ListSkills(ctx, store.SkillFilter{
		Scope:   store.SkillScopeGlobal,
		OwnerID: "user-1",
	}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, result.TotalCount) // sec-audit + draft-skill

	// Filter by exact name
	result, err = s.ListSkills(ctx, store.SkillFilter{
		Name: "sec-audit",
	}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalCount)
	assert.Equal(t, "sec-audit", result.Items[0].Name)
}

func TestSkillVersionFilesRoundTrip(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "files-roundtrip",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	files := []store.SkillFile{
		{Path: "SKILL.md", Size: 2048, Hash: "sha256:aaa", Mode: "0644"},
		{Path: "scripts/build.sh", Size: 512, Hash: "sha256:bbb", Mode: "0755"},
		{Path: "references/guide.md", Size: 4096, Hash: "sha256:ccc", Mode: "0644"},
	}

	version := &store.SkillVersion{
		ID:          api.NewUUID(),
		SkillID:     skill.ID,
		Version:     "1.0.0",
		ContentHash: "sha256:overall",
		Files:       files,
		Status:      store.SkillVersionStatusPublished,
	}
	require.NoError(t, s.CreateSkillVersion(ctx, version))

	got, err := s.GetSkillVersion(ctx, version.ID)
	require.NoError(t, err)
	require.Len(t, got.Files, 3)
	assert.Equal(t, "SKILL.md", got.Files[0].Path)
	assert.Equal(t, int64(2048), got.Files[0].Size)
	assert.Equal(t, "sha256:aaa", got.Files[0].Hash)
	assert.Equal(t, "0644", got.Files[0].Mode)
	assert.Equal(t, "scripts/build.sh", got.Files[1].Path)
	assert.Equal(t, "0755", got.Files[1].Mode)
	assert.Equal(t, "references/guide.md", got.Files[2].Path)
}

func TestSkillLabelsAnnotationsRoundTrip(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "metadata-roundtrip",
		Labels: map[string]string{
			"category": "security",
			"tier":     "premium",
		},
		Annotations: map[string]string{
			"docs-url":   "https://example.com/docs",
			"maintainer": "team-sec",
		},
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	got, err := s.GetSkill(ctx, skill.ID)
	require.NoError(t, err)
	assert.Equal(t, "security", got.Labels["category"])
	assert.Equal(t, "premium", got.Labels["tier"])
	assert.Equal(t, "https://example.com/docs", got.Annotations["docs-url"])
	assert.Equal(t, "team-sec", got.Annotations["maintainer"])
}

func TestSkillNilMapsHandledGracefully(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "nil-maps-skill",
		// Labels and Annotations are nil
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	got, err := s.GetSkill(ctx, skill.ID)
	require.NoError(t, err)
	// Should not panic — nil maps are fine
	assert.Equal(t, "nil-maps-skill", got.Name)
}

func TestSkillVersionEmptyFilesRoundTrip(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:   api.NewUUID(),
		Name: "empty-files-skill",
	}
	require.NoError(t, s.CreateSkill(ctx, skill))

	version := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: "0.1.0",
		// Files is nil
	}
	require.NoError(t, s.CreateSkillVersion(ctx, version))

	got, err := s.GetSkillVersion(ctx, version.ID)
	require.NoError(t, err)
	// Should not panic — nil/empty files handled gracefully
	assert.Equal(t, "0.1.0", got.Version)
}
