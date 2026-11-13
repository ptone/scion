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

//go:build integration

package entc_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	enthubsetting "github.com/GoogleCloudPlatform/scion/pkg/ent/hubsetting"
	entskillinjection "github.com/GoogleCloudPlatform/scion/pkg/ent/skillinjection"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateSkillCoreScope_Postgres(t *testing.T) {
	baseDSN := os.Getenv("SCION_TEST_POSTGRES_URL")
	if baseDSN == "" {
		baseDSN = os.Getenv("SCION_PG_TEST_DSN")
	}
	if baseDSN == "" {
		t.Skip("SCION_TEST_POSTGRES_URL (or SCION_PG_TEST_DSN) not set; skipping Postgres integration test")
	}
	ctx := context.Background()

	dsn := newIsolatedPostgresSchema(t, baseDSN)
	client, err := entc.OpenPostgres(dsn, entc.PoolConfig{MaxOpenConns: 5})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	require.NoError(t, entc.AutoMigrate(ctx, client))

	soloID := uuid.New()
	_, err = client.Skill.Create().
		SetID(soloID).
		SetName("pg-solo").
		SetSlug("pg-solo").
		SetScope("core").
		SetScopeID("").
		SetStoragePath("hubs/pg/skills/pg-solo").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.Skill.Create().
		SetID(uuid.New()).
		SetName("pg-clash").
		SetSlug("pg-clash").
		SetScope("global").
		SetScopeID("").
		Save(ctx)
	require.NoError(t, err)

	clashID := uuid.New()
	_, err = client.Skill.Create().
		SetID(clashID).
		SetName("pg-clash").
		SetSlug("pg-clash").
		SetScope("core").
		SetScopeID("").
		SetStoragePath("hubs/pg/skills/pg-clash").
		Save(ctx)
	require.NoError(t, err)

	rawHub, err := json.Marshal(api.HubSkillInjectionSetting{
		UserDefined: []api.SkillReference{{URI: "skill://core/pg-clash@1.0.0"}},
	})
	require.NoError(t, err)
	_, err = client.HubSetting.Create().
		SetSection("injected_skills").
		SetValue(json.RawMessage(rawHub)).
		Save(ctx)
	require.NoError(t, err)

	projID := uuid.New().String()
	_, err = client.SkillInjection.Create().
		SetID(uuid.New()).
		SetScope(entskillinjection.ScopeProject).
		SetScopeID(projID).
		SetSkillURI("skill://core/pg-solo").
		Save(ctx)
	require.NoError(t, err)

	// Re-run AutoMigrate (and verify idempotency on Postgres).
	for range 2 {
		require.NoError(t, entc.AutoMigrate(ctx, client))
	}

	gotSolo, err := client.Skill.Get(ctx, soloID)
	require.NoError(t, err)
	assert.Equal(t, "global", gotSolo.Scope)
	assert.Equal(t, "pg-solo", gotSolo.Slug)
	assert.Equal(t, "hubs/pg/skills/pg-solo", gotSolo.StoragePath)

	gotClash, err := client.Skill.Get(ctx, clashID)
	require.NoError(t, err)
	assert.Equal(t, "global", gotClash.Scope)
	assert.Equal(t, "pg-clash-core", gotClash.Slug)
	assert.Equal(t, "hubs/pg/skills/pg-clash", gotClash.StoragePath)

	hs, err := client.HubSetting.Query().Where(enthubsetting.SectionEQ("injected_skills")).Only(ctx)
	require.NoError(t, err)
	var gotSetting api.HubSkillInjectionSetting
	require.NoError(t, json.Unmarshal(hs.Value, &gotSetting))
	assert.Equal(t, []api.SkillReference{{URI: "skill://scion/global/pg-clash-core@1.0.0"}}, gotSetting.UserDefined)

	projRows, err := client.SkillInjection.Query().
		Where(entskillinjection.ScopeEQ(entskillinjection.ScopeProject), entskillinjection.ScopeIDEQ(projID)).
		Order(ent.Asc(entskillinjection.FieldSortOrder)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, projRows, 1)
	assert.Equal(t, "skill://scion/global/pg-solo", projRows[0].SkillURI)
}
