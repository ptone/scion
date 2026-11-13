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

package entc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	enthubsetting "github.com/GoogleCloudPlatform/scion/pkg/ent/hubsetting"
	entskill "github.com/GoogleCloudPlatform/scion/pkg/ent/skill"
	entskillinjection "github.com/GoogleCloudPlatform/scion/pkg/ent/skillinjection"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openSkillMigrationTestClient(t *testing.T) *ent.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	client, err := OpenSQLite(path, PoolConfig{MaxOpenConns: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, AutoMigrate(context.Background(), client))
	return client
}

func TestMigrateSkillCoreScope_RewritesToGlobal(t *testing.T) {
	ctx := context.Background()
	client := openSkillMigrationTestClient(t)

	coreID := uuid.New()
	_, err := client.Skill.Create().
		SetID(coreID).
		SetName("deploy-checklist").
		SetSlug("deploy-checklist").
		SetScope("core").
		SetScopeID("").
		SetStoragePath("hubs/hub-1/skills/deploy-checklist").
		SetStorageURI("local://hubs/hub-1/skills/deploy-checklist").
		SetStatus(entskill.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	// Unrelated project and user skills remain untouched.
	projSkillID := uuid.New()
	_, err = client.Skill.Create().
		SetID(projSkillID).
		SetName("deploy-checklist").
		SetSlug("deploy-checklist").
		SetScope("project").
		SetScopeID("proj-1").
		SetStoragePath("hubs/hub-1/skills/groves/proj-1/deploy-checklist").
		SetStatus(entskill.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, AutoMigrate(ctx, client))

	got, err := client.Skill.Get(ctx, coreID)
	require.NoError(t, err)
	assert.Equal(t, "global", got.Scope)
	assert.Equal(t, "", got.ScopeID)
	assert.Equal(t, "deploy-checklist", got.Slug)
	assert.Equal(t, "deploy-checklist", got.Name)

	projGot, err := client.Skill.Get(ctx, projSkillID)
	require.NoError(t, err)
	assert.Equal(t, "project", projGot.Scope)
	assert.Equal(t, "proj-1", projGot.ScopeID)
}

func TestMigrateSkillCoreScope_CollisionRenamesWithSuffix(t *testing.T) {
	ctx := context.Background()
	client := openSkillMigrationTestClient(t)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Case 1: global "audit" exists; core "audit" renames to "audit-core".
	globalAuditID := uuid.New()
	_, err := client.Skill.Create().
		SetID(globalAuditID).
		SetName("audit").
		SetSlug("audit").
		SetScope("global").
		SetScopeID("").
		SetCreated(t0).
		Save(ctx)
	require.NoError(t, err)

	coreAuditID := uuid.New()
	_, err = client.Skill.Create().
		SetID(coreAuditID).
		SetName("audit").
		SetSlug("audit").
		SetScope("core").
		SetScopeID("").
		SetCreated(t0.Add(time.Second)).
		Save(ctx)
	require.NoError(t, err)

	// Case 2: global "lint" AND global "lint-core" both exist; core "lint" renames to "lint-core-2".
	for _, slug := range []string{"lint", "lint-core"} {
		_, err = client.Skill.Create().
			SetID(uuid.New()).
			SetName(slug).
			SetSlug(slug).
			SetScope("global").
			SetScopeID("").
			SetCreated(t0).
			Save(ctx)
		require.NoError(t, err)
	}
	coreLintID := uuid.New()
	_, err = client.Skill.Create().
		SetID(coreLintID).
		SetName("lint").
		SetSlug("lint").
		SetScope("core").
		SetScopeID("").
		SetCreated(t0.Add(2 * time.Second)).
		Save(ctx)
	require.NoError(t, err)

	// Case 3: 64-char limit with trailing hyphen at truncation boundary.
	// 58 'a's + '-' + 'bcde' = 63 chars; truncating to 59 chars (64 - len("-core"))
	// lands on the '-' which must be trimmed so the result is valid kebab-case.
	longSlug := strings.Repeat("a", 58) + "-bcde"
	require.Len(t, longSlug, 63)
	require.NoError(t, api.ValidateSkillName(longSlug))

	_, err = client.Skill.Create().
		SetID(uuid.New()).
		SetName(longSlug).
		SetSlug(longSlug).
		SetScope("global").
		SetScopeID("").
		SetCreated(t0).
		Save(ctx)
	require.NoError(t, err)

	coreLongID := uuid.New()
	_, err = client.Skill.Create().
		SetID(coreLongID).
		SetName(longSlug).
		SetSlug(longSlug).
		SetScope("core").
		SetScopeID("").
		SetCreated(t0.Add(3 * time.Second)).
		Save(ctx)
	require.NoError(t, err)

	// Capture slog warnings to verify old and new identities are logged.
	var logBuf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	require.NoError(t, MigrateSkillCoreScope(ctx, client))

	globalAudit, err := client.Skill.Get(ctx, globalAuditID)
	require.NoError(t, err)
	assert.Equal(t, "global", globalAudit.Scope)
	assert.Equal(t, "audit", globalAudit.Slug)

	migratedAudit, err := client.Skill.Get(ctx, coreAuditID)
	require.NoError(t, err)
	assert.Equal(t, "global", migratedAudit.Scope)
	assert.Equal(t, "audit-core", migratedAudit.Slug)

	migratedLint, err := client.Skill.Get(ctx, coreLintID)
	require.NoError(t, err)
	assert.Equal(t, "global", migratedLint.Scope)
	assert.Equal(t, "lint-core-2", migratedLint.Slug)

	migratedLong, err := client.Skill.Get(ctx, coreLongID)
	require.NoError(t, err)
	assert.Equal(t, "global", migratedLong.Scope)
	assert.LessOrEqual(t, len(migratedLong.Slug), 64)
	assert.Equal(t, strings.Repeat("a", 58)+"-core", migratedLong.Slug)
	assert.NoError(t, api.ValidateSkillName(migratedLong.Slug))

	logs := logBuf.String()
	assert.Contains(t, logs, coreAuditID.String())
	assert.Contains(t, logs, "old_slug=audit")
	assert.Contains(t, logs, "new_slug=audit-core")
	assert.Contains(t, logs, "old_slug=lint")
	assert.Contains(t, logs, "new_slug=lint-core-2")
}

func TestMigrateSkillCoreScope_RewritesInjectionReferences(t *testing.T) {
	ctx := context.Background()
	client := openSkillMigrationTestClient(t)

	// Non-colliding core skill "alpha" -> "alpha".
	_, err := client.Skill.Create().
		SetID(uuid.New()).
		SetName("alpha").
		SetSlug("alpha").
		SetScope("core").
		SetScopeID("").
		Save(ctx)
	require.NoError(t, err)

	// Colliding core skill "beta" -> "beta-core" (global "beta" exists).
	_, err = client.Skill.Create().
		SetID(uuid.New()).
		SetName("beta").
		SetSlug("beta").
		SetScope("global").
		SetScopeID("").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.Skill.Create().
		SetID(uuid.New()).
		SetName("beta").
		SetSlug("beta").
		SetScope("core").
		SetScopeID("").
		Save(ctx)
	require.NoError(t, err)

	// 1. Hub injected_skills setting (system untouched; user_defined rewritten and deduplicated).
	hubSetting := api.HubSkillInjectionSetting{
		System: []api.SkillReference{
			{URI: "scion-platform://scion"},
		},
		UserDefined: []api.SkillReference{
			{URI: "skill://core/alpha"},
			{URI: "skill://core/beta@^1.0", As: "core-beta", Optional: true},
			{URI: "skill://scion/core/beta@1.2.0"},
			{URI: "skill://core/dup-hub", As: "dropped-core"},
			{URI: "skill://scion/global/dup-hub", As: "kept-global"},
			{URI: "skill://core/dup-two", As: "first-core"},
			{URI: "skill://scion/core/dup-two", As: "second-core"},
			{URI: "gh://acme/skills/keep@main"},
		},
	}
	rawHubSetting, err := json.Marshal(hubSetting)
	require.NoError(t, err)
	_, err = client.HubSetting.Create().
		SetSection("injected_skills").
		SetValue(json.RawMessage(rawHubSetting)).
		Save(ctx)
	require.NoError(t, err)

	// 2. Project SkillInjection rows (including duplicate-target deduplication + warning).
	projID := uuid.New().String()
	var droppedCoreInjID, keptGlobalInjID uuid.UUID
	for i, uri := range []string{
		"skill://core/alpha",
		"skill://core/beta@1.0.0",
		"skill://core/dup-skill",
		"skill://scion/global/dup-skill",
		"skill://project/local-tool",
	} {
		rowID := uuid.New()
		switch uri {
		case "skill://core/dup-skill":
			droppedCoreInjID = rowID
		case "skill://scion/global/dup-skill":
			keptGlobalInjID = rowID
		}
		_, err = client.SkillInjection.Create().
			SetID(rowID).
			SetScope(entskillinjection.ScopeProject).
			SetScopeID(projID).
			SetSkillURI(uri).
			SetSortOrder(i + 1).
			Save(ctx)
		require.NoError(t, err)
	}

	// 3. User SkillInjection rows.
	userID := uuid.New().String()
	_, err = client.SkillInjection.Create().
		SetID(uuid.New()).
		SetScope(entskillinjection.ScopeUser).
		SetScopeID(userID).
		SetSkillURI("skill://core/beta").
		SetSkillAs("my-beta").
		SetAllowProgeny(true).
		SetSortOrder(1).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.SkillInjection.Create().
		SetID(uuid.New()).
		SetScope(entskillinjection.ScopeUser).
		SetScopeID(userID).
		SetSkillURI("skill:///core/alpha@latest").
		SetOptional(true).
		SetSortOrder(2).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.SkillInjection.Create().
		SetID(uuid.New()).
		SetScope(entskillinjection.ScopeUser).
		SetScopeID(userID).
		SetSkillURI("bare-skill").
		SetSortOrder(3).
		Save(ctx)
	require.NoError(t, err)

	var logBuf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	require.NoError(t, MigrateSkillCoreScope(ctx, client))

	// Verify hub setting.
	hs, err := client.HubSetting.Query().Where(enthubsetting.SectionEQ("injected_skills")).Only(ctx)
	require.NoError(t, err)
	var gotSetting api.HubSkillInjectionSetting
	require.NoError(t, json.Unmarshal(hs.Value, &gotSetting))
	assert.Equal(t, []api.SkillReference{{URI: "scion-platform://scion"}}, gotSetting.System)
	assert.Equal(t, []api.SkillReference{
		{URI: "skill://scion/global/alpha"},
		{URI: "skill://scion/global/beta-core@^1.0", As: "core-beta", Optional: true},
		{URI: "skill://scion/global/beta-core@1.2.0"},
		{URI: "skill://scion/global/dup-hub", As: "kept-global"},
		{URI: "skill://scion/global/dup-two", As: "first-core"},
		{URI: "gh://acme/skills/keep@main"},
	}, gotSetting.UserDefined)

	// Verify project SkillInjection rows and N3 duplicate-drop warning with both row IDs.
	projRows, err := client.SkillInjection.Query().
		Where(entskillinjection.ScopeEQ(entskillinjection.ScopeProject), entskillinjection.ScopeIDEQ(projID)).
		Order(ent.Asc(entskillinjection.FieldSortOrder)).
		All(ctx)
	require.NoError(t, err)
	var projURIs []string
	for _, r := range projRows {
		projURIs = append(projURIs, r.SkillURI)
	}
	assert.Equal(t, []string{
		"skill://scion/global/alpha",
		"skill://scion/global/beta-core@1.0.0",
		"skill://scion/global/dup-skill",
		"skill://project/local-tool",
	}, projURIs)

	logs := logBuf.String()
	assert.Contains(t, logs, "dropped_injection_id="+droppedCoreInjID.String())
	assert.Contains(t, logs, "kept_injection_id="+keptGlobalInjID.String())

	// Verify user SkillInjection rows.
	userRows, err := client.SkillInjection.Query().
		Where(entskillinjection.ScopeEQ(entskillinjection.ScopeUser), entskillinjection.ScopeIDEQ(userID)).
		Order(ent.Asc(entskillinjection.FieldSortOrder)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, userRows, 3)
	assert.Equal(t, "skill://scion/global/beta-core", userRows[0].SkillURI)
	assert.Equal(t, "my-beta", userRows[0].SkillAs)
	assert.True(t, userRows[0].AllowProgeny)
	assert.Equal(t, "skill://scion/global/alpha@latest", userRows[1].SkillURI)
	assert.True(t, userRows[1].Optional)
	assert.Equal(t, "bare-skill", userRows[2].SkillURI)
}

func TestMigrateSkillCoreScope_Idempotent(t *testing.T) {
	ctx := context.Background()
	client := openSkillMigrationTestClient(t)

	_, err := client.Skill.Create().
		SetID(uuid.New()).
		SetName("shared").
		SetSlug("shared").
		SetScope("global").
		SetScopeID("").
		Save(ctx)
	require.NoError(t, err)

	coreID := uuid.New()
	_, err = client.Skill.Create().
		SetID(coreID).
		SetName("shared").
		SetSlug("shared").
		SetScope("core").
		SetScopeID("").
		Save(ctx)
	require.NoError(t, err)

	soloID := uuid.New()
	_, err = client.Skill.Create().
		SetID(soloID).
		SetName("solo").
		SetSlug("solo").
		SetScope("core").
		SetScopeID("").
		Save(ctx)
	require.NoError(t, err)

	injID := uuid.New()
	_, err = client.SkillInjection.Create().
		SetID(injID).
		SetScope(entskillinjection.ScopeProject).
		SetScopeID("proj-idem").
		SetSkillURI("skill://core/shared").
		Save(ctx)
	require.NoError(t, err)

	// Run multiple times via MigrateSkillCoreScope and AutoMigrate.
	for i := range 3 {
		if i%2 == 0 {
			require.NoError(t, MigrateSkillCoreScope(ctx, client))
		} else {
			require.NoError(t, AutoMigrate(ctx, client))
		}

		remCore, err := client.Skill.Query().Where(entskill.ScopeEQ("core")).Count(ctx)
		require.NoError(t, err)
		assert.Zero(t, remCore)

		gotCollision, err := client.Skill.Get(ctx, coreID)
		require.NoError(t, err)
		assert.Equal(t, "global", gotCollision.Scope)
		assert.Equal(t, "shared-core", gotCollision.Slug)

		gotSolo, err := client.Skill.Get(ctx, soloID)
		require.NoError(t, err)
		assert.Equal(t, "global", gotSolo.Scope)
		assert.Equal(t, "solo", gotSolo.Slug)

		gotInj, err := client.SkillInjection.Get(ctx, injID)
		require.NoError(t, err)
		assert.Equal(t, "skill://scion/global/shared-core", gotInj.SkillURI)
	}
}

func TestMigrateSkillCoreScope_KeepsStoragePath(t *testing.T) {
	ctx := context.Background()
	client := openSkillMigrationTestClient(t)

	soloID := uuid.New()
	_, err := client.Skill.Create().
		SetID(soloID).
		SetName("legacy-core").
		SetSlug("legacy-core").
		SetScope("core").
		SetScopeID("").
		SetStorageBucket("local").
		SetStoragePath("skills/legacy-core").
		SetStorageURI("local://skills/legacy-core").
		Save(ctx)
	require.NoError(t, err)

	// Colliding row also retains its stored storage_path and storage_uri even when slug changes.
	_, err = client.Skill.Create().
		SetID(uuid.New()).
		SetName("colliding").
		SetSlug("colliding").
		SetScope("global").
		SetScopeID("").
		SetStorageBucket("local").
		SetStoragePath("hubs/h1/skills/global/colliding").
		SetStorageURI("local://hubs/h1/skills/global/colliding").
		Save(ctx)
	require.NoError(t, err)

	collidingCoreID := uuid.New()
	_, err = client.Skill.Create().
		SetID(collidingCoreID).
		SetName("colliding").
		SetSlug("colliding").
		SetScope("core").
		SetScopeID("").
		SetStorageBucket("local").
		SetStoragePath("hubs/h1/skills/colliding").
		SetStorageURI("local://hubs/h1/skills/colliding").
		Save(ctx)
	require.NoError(t, err)

	require.NoError(t, MigrateSkillCoreScope(ctx, client))

	gotSolo, err := client.Skill.Get(ctx, soloID)
	require.NoError(t, err)
	assert.Equal(t, "global", gotSolo.Scope)
	assert.Equal(t, "skills/legacy-core", gotSolo.StoragePath)
	assert.Equal(t, "local://skills/legacy-core", gotSolo.StorageURI)
	assert.Equal(t, "local", gotSolo.StorageBucket)

	gotColliding, err := client.Skill.Get(ctx, collidingCoreID)
	require.NoError(t, err)
	assert.Equal(t, "global", gotColliding.Scope)
	assert.Equal(t, "colliding-core", gotColliding.Slug)
	assert.Equal(t, "hubs/h1/skills/colliding", gotColliding.StoragePath)
	assert.Equal(t, "local://hubs/h1/skills/colliding", gotColliding.StorageURI)
}

func TestMigrateSkillCoreScope_RewrittenURI_ParsesAsScionGlobal(t *testing.T) {
	slugRemap := map[string]string{
		"alpha": "alpha",
		"beta":  "beta-core",
	}
	cases := []struct {
		raw      string
		wantURI  string
		wantName string
		wantVer  string
	}{
		{"skill://core/alpha", "skill://scion/global/alpha", "alpha", "latest"},
		{"skill://scion/core/beta@1.2.3", "skill://scion/global/beta-core@1.2.3", "beta-core", "1.2.3"},
		{"skill:///core/beta@^1.0", "skill://scion/global/beta-core@^1.0", "beta-core", "^1.0"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			rewritten, ok := rewriteCoreSkillURI(tc.raw, slugRemap)
			require.True(t, ok)
			assert.Equal(t, tc.wantURI, rewritten)
			// Must use the canonical 3-segment form so older binaries (without
			// "global" in registryAliases) also parse it as Registry=scion, Scope=global.
			assert.True(t, strings.HasPrefix(rewritten, "skill://scion/global/"))

			parsed, err := api.ParseSkillURI(rewritten)
			require.NoError(t, err)
			assert.Equal(t, "scion", parsed.Registry)
			assert.Equal(t, "global", parsed.Scope)
			assert.Equal(t, "", parsed.ScopeID)
			assert.Equal(t, tc.wantName, parsed.Name)
			assert.Equal(t, tc.wantVer, parsed.Version)
		})
	}
}

func TestMigrateSkillCoreScope_CollisionDoesNotStealSiblingCoreSlug(t *testing.T) {
	t0 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name         string
		coreACreated time.Time
		coreACoreAt  time.Time
	}{
		{"core_a_created_before_core_a_core", t0.Add(time.Second), t0.Add(2 * time.Second)},
		{"core_a_core_created_before_core_a", t0.Add(2 * time.Second), t0.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := openSkillMigrationTestClient(t)

			globalAID := uuid.New()
			_, err := client.Skill.Create().
				SetID(globalAID).
				SetName("a").
				SetSlug("a").
				SetScope("global").
				SetScopeID("").
				SetCreated(t0).
				Save(ctx)
			require.NoError(t, err)

			coreAID := uuid.New()
			_, err = client.Skill.Create().
				SetID(coreAID).
				SetName("a").
				SetSlug("a").
				SetScope("core").
				SetScopeID("").
				SetCreated(tc.coreACreated).
				Save(ctx)
			require.NoError(t, err)

			coreACoreID := uuid.New()
			_, err = client.Skill.Create().
				SetID(coreACoreID).
				SetName("a-core").
				SetSlug("a-core").
				SetScope("core").
				SetScopeID("").
				SetCreated(tc.coreACoreAt).
				Save(ctx)
			require.NoError(t, err)

			injAID := uuid.New()
			_, err = client.SkillInjection.Create().
				SetID(injAID).
				SetScope(entskillinjection.ScopeProject).
				SetScopeID("proj-order").
				SetSkillURI("skill://core/a").
				SetSortOrder(1).
				Save(ctx)
			require.NoError(t, err)

			injACoreID := uuid.New()
			_, err = client.SkillInjection.Create().
				SetID(injACoreID).
				SetScope(entskillinjection.ScopeProject).
				SetScopeID("proj-order").
				SetSkillURI("skill://core/a-core").
				SetSortOrder(2).
				Save(ctx)
			require.NoError(t, err)

			require.NoError(t, MigrateSkillCoreScope(ctx, client))

			gotGlobalA, err := client.Skill.Get(ctx, globalAID)
			require.NoError(t, err)
			assert.Equal(t, "global", gotGlobalA.Scope)
			assert.Equal(t, "a", gotGlobalA.Slug)

			gotCoreACore, err := client.Skill.Get(ctx, coreACoreID)
			require.NoError(t, err)
			assert.Equal(t, "global", gotCoreACore.Scope)
			assert.Equal(t, "a-core", gotCoreACore.Slug, "non-colliding sibling core row must keep its slug")

			gotCoreA, err := client.Skill.Get(ctx, coreAID)
			require.NoError(t, err)
			assert.Equal(t, "global", gotCoreA.Scope)
			assert.Equal(t, "a-core-2", gotCoreA.Slug, "colliding core row must skip sibling core row's slug")

			gotInjA, err := client.SkillInjection.Get(ctx, injAID)
			require.NoError(t, err)
			assert.Equal(t, "skill://scion/global/a-core-2", gotInjA.SkillURI)

			gotInjACore, err := client.SkillInjection.Get(ctx, injACoreID)
			require.NoError(t, err)
			assert.Equal(t, "skill://scion/global/a-core", gotInjACore.SkillURI)
		})
	}
}
