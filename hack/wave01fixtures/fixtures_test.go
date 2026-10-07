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

package main

// These tests run the helper against a REAL on-disk SQLite hub store opened
// with the same entc.OpenSQLite -> NewCompositeStore -> Migrate path the
// helper uses. Nothing is mocked. The admin user and project are created
// through the store directly to stand in for the rows the steward creates
// through the API while the slot is running.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

const (
	testAdminID    = "a0000000-0000-4000-8000-000000000001"
	testAdminEmail = "wave01-admin@example.test"
	testProjectID  = "b0000000-0000-4000-8000-000000000001"
	testProjSlug   = "payments-platform"
	testProject2ID = "b0000000-0000-4000-8000-000000000002"
	testProj2Slug  = "data-team"
)

func init() { util.PinProcessUTC() }

func intp(i int) *int { return &i }

// sliceRecipe is the Phase 1 vertical slice: one stopped agent, one offline
// broker.
func sliceRecipe() *Recipe {
	return &Recipe{
		Schema:   RecipeSchema,
		Admin:    RecipeAdmin{UserID: testAdminID, Email: testAdminEmail},
		Projects: []RecipeProj{{ID: testProjectID, Slug: testProjSlug}},
		Agents: []RecipeAgent{{
			ID:          "c0000000-0000-4000-8000-000000000001",
			ProjectID:   testProjectID,
			Slug:        "ledger-reconciler",
			Template:    "default",
			AgentRole:   "baseline",
			Phase:       "stopped",
			TaskSummary: "Reconciled the nightly ledger export against settlement files and flagged two mismatched batches.",
			Labels:      map[string]string{"team": "payments", "env": "staging"},
		}},
		Brokers: []RecipeBroker{{
			ID:   "d0000000-0000-4000-8000-000000000001",
			Name: "build-runner-eu-west1-a.internal.example.test",
			Slug: "build-runner-eu-west1-a",
		}},
	}
}

// allKindsRecipe covers every allowed (phase, activity) pair across two
// projects.
func allKindsRecipe() *Recipe {
	r := sliceRecipe()
	r.Projects = append(r.Projects, RecipeProj{ID: testProject2ID, Slug: testProj2Slug})
	r.Agents = append(r.Agents,
		RecipeAgent{ID: "c0000000-0000-4000-8000-000000000002", ProjectID: testProjectID, Slug: "fraud-rules-tuner",
			Template: "default", AgentRole: "full", Phase: "stopped", Activity: "crashed", ExitCode: intp(137), ExitReason: "crashed",
			TaskSummary: "Tuning fraud scoring thresholds; the process was killed while replaying the March sample."},
		RecipeAgent{ID: "c0000000-0000-4000-8000-000000000003", ProjectID: testProjectID, Slug: "invoice-migration",
			Template: "default", AgentRole: "baseline", Phase: "stopped", Activity: "limits_exceeded", ExitCode: intp(0), ExitReason: "limits_exceeded"},
		RecipeAgent{ID: "c0000000-0000-4000-8000-000000000004", ProjectID: testProject2ID, Slug: "schema-drift-check",
			Template: "default", AgentRole: "readonly", Phase: "error", ExitReason: "container_missing", ExitCode: intp(1),
			Message: "Container disappeared from the runtime inventory during a node drain."},
		RecipeAgent{ID: "c0000000-0000-4000-8000-000000000005", ProjectID: testProject2ID, Slug: "ledger-reconciler",
			Template: "default", AgentRole: "none", Phase: "created"},
	)
	return r
}

// newHubDB creates a real hub DB file with the admin and projects, closes it
// and checkpoints it, as the steward's stopped clone would be.
func newHubDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "hub.db")
	fs, err := openStore(ctx, dbPath)
	require.NoError(t, err)
	require.NoError(t, fs.store.CreateUser(ctx, &store.User{
		ID: testAdminID, Email: testAdminEmail, DisplayName: "Wave Admin", Role: "admin", Status: "active",
	}))
	require.NoError(t, fs.store.CreateProject(ctx, &store.Project{ID: testProjectID, Name: "Payments Platform", Slug: testProjSlug}))
	require.NoError(t, fs.store.CreateProject(ctx, &store.Project{ID: testProject2ID, Name: "Data Team", Slug: testProj2Slug}))
	require.NoError(t, fs.Close())
	_, err = checkpoint(dbPath)
	require.NoError(t, err)
	return dbPath
}

func writeRecipe(t *testing.T, r *Recipe) string {
	t.Helper()
	data, err := json.MarshalIndent(r, "", "  ")
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "recipe.json")
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

func runOpts(db, recipe string) Options {
	return Options{DBPath: db, RecipePath: recipe, Attestation: "slot=test; stopped; checkpointed; isolated clone"}
}

// rawRow reads nullable agent columns straight from SQLite, independent of the
// store's mapping.
func rawAgent(t *testing.T, dbPath, id string) map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cols := []string{"phase", "activity", "exit_reason", "last_seen", "last_activity_event", "started_at",
		"runtime_broker_id", "run_intent", "launch_state", "launch_id", "start_claim_id", "deletion_state", "deleted_at",
		"reincarnation_state", "detached", "state_version"}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	require.NoError(t, db.QueryRow("SELECT "+strings.Join(cols, ",")+" FROM agents WHERE id = ?", id).Scan(ptrs...))
	out := map[string]any{}
	for i, c := range cols {
		if b, ok := vals[i].([]byte); ok {
			vals[i] = string(b)
		}
		out[c] = vals[i]
	}
	return out
}

func assertNullOrEmpty(t *testing.T, row map[string]any, cols ...string) {
	t.Helper()
	for _, c := range cols {
		v := row[c]
		assert.True(t, v == nil || v == "", "column %s must be NULL/empty, got %v", c, v)
	}
}

func TestRunVerticalSlice(t *testing.T) {
	ctx := context.Background()
	db := newHubDB(t)
	r := sliceRecipe()
	m, err := Run(ctx, runOpts(db, writeRecipe(t, r)))
	require.NoError(t, err)
	assert.Equal(t, "ok", m.Outcome)
	assert.True(t, m.Verified)
	assert.Equal(t, []string{r.Agents[0].ID}, m.Written.Agents)
	assert.Equal(t, []string{r.Brokers[0].ID}, m.Written.Brokers)
	assert.NotEqual(t, m.Before.DB.SHA256, m.After.DB.SHA256)
	assert.False(t, m.After.WAL.Exists && m.After.WAL.Size > 0, "after-state must be checkpointed")
	assert.Len(t, m.Divergences, len(Divergences))
	assert.NotEmpty(t, m.RecipeSHA256)

	// Independent raw-SQL check of the persisted agent row.
	row := rawAgent(t, db, r.Agents[0].ID)
	assert.Equal(t, "stopped", row["phase"])
	assert.Equal(t, "stopped", row["run_intent"])
	assert.EqualValues(t, 1, row["detached"])
	assert.EqualValues(t, 1, row["state_version"])
	assertNullOrEmpty(t, row, "activity", "exit_reason", "last_seen", "last_activity_event", "started_at",
		"runtime_broker_id", "launch_state", "launch_id", "start_claim_id", "deletion_state", "deleted_at", "reincarnation_state")

	// Raw broker row: offline, disconnected, no heartbeat.
	sdb, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	require.NoError(t, err)
	defer func() { _ = sdb.Close() }()
	var status, conn string
	var hb sql.NullString
	require.NoError(t, sdb.QueryRow("SELECT status, connection_state, last_heartbeat FROM runtime_brokers WHERE id = ?",
		r.Brokers[0].ID).Scan(&status, &conn, &hb))
	assert.Equal(t, "offline", status)
	assert.Equal(t, "disconnected", conn)
	assert.False(t, hb.Valid, "last_heartbeat must be NULL")
	var n int
	require.NoError(t, sdb.QueryRow("SELECT count(*) FROM broker_join_tokens").Scan(&n))
	assert.Zero(t, n, "no join token may be issued")
	require.NoError(t, sdb.QueryRow("SELECT count(*) FROM broker_secrets").Scan(&n))
	assert.Zero(t, n, "no broker secret may be issued")
	require.NoError(t, sdb.QueryRow("SELECT count(*) FROM agent_identity_keys WHERE agent_id = ? AND key = ?",
		r.Agents[0].ID, r.Agents[0].Slug).Scan(&n))
	assert.Equal(t, 1, n, "identity key row must exist")
}

func TestRunAllAllowedKinds(t *testing.T) {
	ctx := context.Background()
	db := newHubDB(t)
	r := allKindsRecipe()
	m, err := Run(ctx, runOpts(db, writeRecipe(t, r)))
	require.NoError(t, err)
	require.Equal(t, "ok", m.Outcome)
	assert.Equal(t, map[string]int{"stopped": 1, "stopped/crashed": 1, "stopped/limits_exceeded": 1, "error": 1, "created": 1}, m.StateMix)

	// Same slug in two projects is allowed (unique per project).
	for _, a := range r.Agents {
		row := rawAgent(t, db, a.ID)
		assert.Equal(t, a.Phase, row["phase"], a.Slug)
		assert.Equal(t, "stopped", row["run_intent"], a.Slug)
		assertNullOrEmpty(t, row, "last_seen", "started_at", "last_activity_event", "runtime_broker_id")
		wantVersion := 1
		if a.ExitCode != nil || a.ExitReason != "" {
			wantVersion = 2
		}
		assert.EqualValues(t, wantVersion, row["state_version"], a.Slug)
	}

	// Store-level readback of exit fields after a fresh reopen.
	fs, err := openStore(ctx, db)
	require.NoError(t, err)
	defer func() { _ = fs.Close() }()
	got, err := fs.store.GetAgent(ctx, "c0000000-0000-4000-8000-000000000002")
	require.NoError(t, err)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 137, *got.ExitCode)
	assert.Equal(t, "crashed", got.ExitReason)
	assert.Equal(t, "crashed", got.Activity)
	assert.Equal(t, "full", got.AppliedConfig.AgentRole)
	assert.Equal(t, []string{testAdminID}, got.Ancestry)
	require.NoError(t, fs.Verify(ctx, r))
}

func TestRerunRefused(t *testing.T) {
	ctx := context.Background()
	db := newHubDB(t)
	recipe := writeRecipe(t, sliceRecipe())
	_, err := Run(ctx, runOpts(db, recipe))
	require.NoError(t, err)
	after, err := dbState(db)
	require.NoError(t, err)

	// Second run: refused by the run marker.
	m, err := Run(ctx, runOpts(db, recipe))
	require.Error(t, err)
	assert.Equal(t, "refused", m.Outcome)
	assert.Contains(t, err.Error(), "run marker")

	// Even with the marker removed, preflight refuses existing rows.
	require.NoError(t, os.Remove(db+markerSuffix))
	m, err = Run(ctx, runOpts(db, recipe))
	require.Error(t, err)
	assert.Equal(t, "refused", m.Outcome)
	assert.Contains(t, err.Error(), "already exists")
	assert.Nil(t, m.Written)

	// The refused runs wrote nothing.
	_, err = checkpoint(db)
	require.NoError(t, err)
	now, err := dbState(db)
	require.NoError(t, err)
	assert.Equal(t, after.DB.SHA256, now.DB.SHA256)
}

func TestPreflightRefusesWrongBinding(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(r *Recipe){
		"admin email":  func(r *Recipe) { r.Admin.Email = "someone-else@example.test" },
		"admin id":     func(r *Recipe) { r.Admin.UserID = "a0000000-0000-4000-8000-0000000000ff" },
		"project slug": func(r *Recipe) { r.Projects[0].Slug = "other-project" },
		"missing project": func(r *Recipe) {
			r.Projects[0].ID = "b0000000-0000-4000-8000-0000000000ff"
			r.Agents[0].ProjectID = r.Projects[0].ID
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			db := newHubDB(t)
			before, err := dbState(db)
			require.NoError(t, err)
			r := sliceRecipe()
			mutate(r)
			m, err := Run(ctx, runOpts(db, writeRecipe(t, r)))
			require.Error(t, err)
			assert.Equal(t, "refused", m.Outcome)
			_, statErr := os.Stat(db + markerSuffix)
			assert.True(t, os.IsNotExist(statErr), "no marker on preflight refusal")
			_, err = checkpoint(db)
			require.NoError(t, err)
			after, err := dbState(db)
			require.NoError(t, err)
			assert.Equal(t, before.DB.SHA256, after.DB.SHA256, "preflight refusal must not change rows")
		})
	}
}

func TestPreflightRefusesExistingIdentityKeyAndBroker(t *testing.T) {
	ctx := context.Background()
	db := newHubDB(t)
	// Pre-existing rows created by an earlier (real) store write.
	fs, err := openStore(ctx, db)
	require.NoError(t, err)
	existing := buildAgent(sliceRecipe(), sliceRecipe().Agents[0])
	existing.ID = "c0000000-0000-4000-8000-0000000000aa"
	require.NoError(t, fs.store.CreateAgent(ctx, existing))
	require.NoError(t, fs.store.ReplaceAgentIdentityKeys(ctx, existing.ID, existing.ProjectID, []string{existing.Slug}))
	require.NoError(t, fs.store.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: "d0000000-0000-4000-8000-0000000000aa", Name: "BUILD-RUNNER-EU-WEST1-A.internal.example.test", Slug: "other-slug"}))
	require.NoError(t, fs.Close())
	_, err = checkpoint(db)
	require.NoError(t, err)

	m, err := Run(ctx, runOpts(db, writeRecipe(t, sliceRecipe())))
	require.Error(t, err)
	assert.Equal(t, "refused", m.Outcome)
	assert.Contains(t, err.Error(), `agent slug "ledger-reconciler"`)
	assert.Contains(t, err.Error(), `identity key "ledger-reconciler"`)
	assert.Contains(t, err.Error(), "broker name")
}

func TestTargetPreconditions(t *testing.T) {
	ctx := context.Background()
	recipe := writeRecipe(t, sliceRecipe())

	t.Run("missing db is never created", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "absent.db")
		m, err := Run(ctx, runOpts(p, recipe))
		require.Error(t, err)
		assert.Equal(t, "refused", m.Outcome)
		_, statErr := os.Stat(p)
		assert.True(t, os.IsNotExist(statErr))
	})
	t.Run("relative path", func(t *testing.T) {
		_, err := Run(ctx, runOpts("hub.db", recipe))
		require.ErrorContains(t, err, "absolute")
	})
	t.Run("missing attestation", func(t *testing.T) {
		o := runOpts(newHubDB(t), recipe)
		o.Attestation = "  "
		_, err := Run(ctx, o)
		require.ErrorContains(t, err, "attest-stopped-clone")
	})
	t.Run("non-checkpointed wal", func(t *testing.T) {
		db := newHubDB(t)
		require.NoError(t, os.WriteFile(db+"-wal", []byte("not empty"), 0o600))
		_, err := Run(ctx, runOpts(db, recipe))
		require.ErrorContains(t, err, "not checkpointed")
	})
	t.Run("symlink", func(t *testing.T) {
		db := newHubDB(t)
		link := filepath.Join(t.TempDir(), "link.db")
		require.NoError(t, os.Symlink(db, link))
		_, err := Run(ctx, runOpts(link, recipe))
		require.ErrorContains(t, err, "regular file")
	})
}

// TestPartialFailureReported forces the second agent insert to fail with a
// real SQLite trigger and checks the run reports the partial write honestly.
func TestPartialFailureReported(t *testing.T) {
	ctx := context.Background()
	db := newHubDB(t)
	r := allKindsRecipe()
	sdb, err := sql.Open("sqlite", "file:"+db)
	require.NoError(t, err)
	_, err = sdb.Exec(`CREATE TRIGGER wave01_fail BEFORE INSERT ON agents WHEN NEW.slug = 'fraud-rules-tuner'
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	require.NoError(t, err)
	_, err = sdb.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	require.NoError(t, err)
	require.NoError(t, sdb.Close())

	m, err := Run(ctx, runOpts(db, writeRecipe(t, r)))
	require.Error(t, err)
	assert.Equal(t, "partial-discard-clone", m.Outcome)
	assert.Contains(t, err.Error(), "DISCARD")
	assert.Equal(t, []string{r.Agents[0].ID}, m.Written.Agents)
	assert.Equal(t, "agent "+r.Agents[1].ID, m.Written.FailedAt)
	assert.False(t, m.Verified)

	// The failed agent's transaction rolled back: no row, no identity key.
	rdb, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	require.NoError(t, err)
	defer func() { _ = rdb.Close() }()
	var n int
	require.NoError(t, rdb.QueryRow("SELECT count(*) FROM agent_identity_keys WHERE key = 'fraud-rules-tuner'").Scan(&n))
	assert.Zero(t, n)

	// And the clone cannot be reused.
	_, err = Run(ctx, runOpts(db, writeRecipe(t, r)))
	require.ErrorContains(t, err, "run marker")
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(r *Recipe){
		"stopped completed":      func(r *Recipe) { r.Agents[0].Activity = "completed" },
		"running":                func(r *Recipe) { r.Agents[0].Phase = "running" },
		"running working":        func(r *Recipe) { r.Agents[0].Phase, r.Agents[0].Activity = "running", "working" },
		"provisioning":           func(r *Recipe) { r.Agents[0].Phase = "provisioning" },
		"cloning":                func(r *Recipe) { r.Agents[0].Phase = "cloning" },
		"starting":               func(r *Recipe) { r.Agents[0].Phase = "starting" },
		"stopping":               func(r *Recipe) { r.Agents[0].Phase = "stopping" },
		"stopped offline":        func(r *Recipe) { r.Agents[0].Activity = "offline" },
		"error crashed":          func(r *Recipe) { r.Agents[0].Phase, r.Agents[0].Activity = "error", "crashed" },
		"created crashed":        func(r *Recipe) { r.Agents[0].Phase, r.Agents[0].Activity = "created", "crashed" },
		"error without message":  func(r *Recipe) { r.Agents[0].Phase, r.Agents[0].ExitReason = "error", "crashed" },
		"error without reason":   func(r *Recipe) { r.Agents[0].Phase, r.Agents[0].Message = "error", "boom" },
		"crashed without reason": func(r *Recipe) { r.Agents[0].Activity, r.Agents[0].ExitCode = "crashed", intp(1) },
		"crashed without code":   func(r *Recipe) { r.Agents[0].Activity, r.Agents[0].ExitReason = "crashed", "crashed" },
		"crashed wrong reason": func(r *Recipe) {
			r.Agents[0].Activity, r.Agents[0].ExitReason, r.Agents[0].ExitCode = "crashed", "evicted", intp(1)
		},
		"stopped with reason": func(r *Recipe) { r.Agents[0].ExitReason = "crashed" },
		"created with code":   func(r *Recipe) { r.Agents[0].Phase, r.Agents[0].ExitCode = "created", intp(0) },
		"unknown exit reason": func(r *Recipe) {
			r.Agents[0].Phase, r.Agents[0].ExitReason, r.Agents[0].Message = "error", "bored", "x"
		},
		"bad role":              func(r *Recipe) { r.Agents[0].AgentRole = "admin" },
		"empty role":            func(r *Recipe) { r.Agents[0].AgentRole = "" },
		"non-canonical slug":    func(r *Recipe) { r.Agents[0].Slug = "Ledger Reconciler" },
		"reserved slug":         func(r *Recipe) { r.Agents[0].Slug = "admin" },
		"empty template":        func(r *Recipe) { r.Agents[0].Template = "" },
		"uppercase uuid":        func(r *Recipe) { r.Agents[0].ID = strings.ToUpper(r.Agents[0].ID) },
		"bad uuid":              func(r *Recipe) { r.Agents[0].ID = "agent-1" },
		"unlisted project":      func(r *Recipe) { r.Agents[0].ProjectID = testProject2ID },
		"duplicate id":          func(r *Recipe) { r.Brokers[0].ID = r.Agents[0].ID },
		"admin id reused":       func(r *Recipe) { r.Agents[0].ID = testAdminID },
		"duplicate slug":        func(r *Recipe) { r.Agents = append(r.Agents, dupAgent(r.Agents[0])) },
		"duplicate broker slug": func(r *Recipe) { r.Brokers = append(r.Brokers, dupBroker(r.Brokers[0], "Other Name")) },
		"duplicate broker name": func(r *Recipe) {
			b := dupBroker(r.Brokers[0], strings.ToUpper(r.Brokers[0].Name))
			b.Slug = "other"
			r.Brokers = append(r.Brokers, b)
		},
		"too many labels": func(r *Recipe) {
			for i := 0; i < 9; i++ {
				r.Agents[0].Labels[string(rune('a'+i))] = "v"
			}
		},
		"padded summary": func(r *Recipe) { r.Agents[0].TaskSummary = " padded" },
		"huge summary":   func(r *Recipe) { r.Agents[0].TaskSummary = strings.Repeat("x", maxTaskSummaryLen+1) },
		"wrong schema":   func(r *Recipe) { r.Schema = "v0" },
		"empty":          func(r *Recipe) { r.Agents, r.Brokers = nil, nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := sliceRecipe()
			mutate(r)
			assert.Error(t, r.Validate())
		})
	}
	require.NoError(t, sliceRecipe().Validate())
	require.NoError(t, allKindsRecipe().Validate())
}

func dupAgent(a RecipeAgent) RecipeAgent {
	a.ID = "c0000000-0000-4000-8000-0000000000dd"
	return a
}

func dupBroker(b RecipeBroker, name string) RecipeBroker {
	b.ID = "d0000000-0000-4000-8000-0000000000dd"
	b.Name = name
	return b
}

// TestDecodeRejectsForbiddenFields proves live/heartbeat fields cannot be
// expressed in a recipe at all.
func TestDecodeRejectsForbiddenFields(t *testing.T) {
	base, err := json.Marshal(sliceRecipe())
	require.NoError(t, err)
	for _, field := range []string{"lastSeen", "startedAt", "lastActivityEvent", "runtimeBrokerId", "runIntent", "launchState", "name"} {
		t.Run("agent "+field, func(t *testing.T) {
			bad := strings.Replace(string(base), `"slug":"ledger-reconciler"`, `"slug":"ledger-reconciler","`+field+`":"x"`, 1)
			_, err := DecodeRecipe([]byte(bad))
			require.ErrorContains(t, err, "unknown field")
		})
	}
	for _, field := range []string{"status", "lastHeartbeat", "connectionState", "joinToken"} {
		t.Run("broker "+field, func(t *testing.T) {
			bad := strings.Replace(string(base), `"slug":"build-runner-eu-west1-a"`, `"slug":"build-runner-eu-west1-a","`+field+`":"x"`, 1)
			_, err := DecodeRecipe([]byte(bad))
			require.ErrorContains(t, err, "unknown field")
		})
	}
	_, err = DecodeRecipe(append(base, []byte(`{}`)...))
	require.ErrorContains(t, err, "unexpected data")
}

// TestExampleRecipesValidate keeps the checked-in examples valid.
func TestExampleRecipesValidate(t *testing.T) {
	matches, err := filepath.Glob("examples/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, matches)
	for _, p := range matches {
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			r, err := DecodeRecipe(data)
			require.NoError(t, err)
			require.NoError(t, r.Validate())
		})
	}
}

// TestVerifyDetectsForbiddenState proves Verify is not vacuous: each forbidden
// live/heartbeat/broker condition, written behind the helper's back with raw
// SQL, is reported.
func TestVerifyDetectsForbiddenState(t *testing.T) {
	ctx := context.Background()
	r := sliceRecipe()
	agentID, brokerID := r.Agents[0].ID, r.Brokers[0].ID
	cases := map[string]struct {
		stmt string
		arg  string
		want string
	}{
		"last_seen":         {"UPDATE agents SET last_seen = '2026-10-07 12:00:00+00:00' WHERE id = ?", agentID, "lastSeen is set"},
		"started_at":        {"UPDATE agents SET started_at = '2026-10-07 12:00:00+00:00' WHERE id = ?", agentID, "startedAt is set"},
		"runtime_broker_id": {"UPDATE agents SET runtime_broker_id = 'x' WHERE id = ?", agentID, "runtimeBrokerId is set"},
		"run_intent":        {"UPDATE agents SET run_intent = 'running' WHERE id = ?", agentID, "runIntent"},
		"launch_state":      {"UPDATE agents SET launch_state = 'ended' WHERE id = ?", agentID, "launch_* column"},
		"phase running":     {"UPDATE agents SET phase = 'running' WHERE id = ?", agentID, "not allowed"},
		"identity key":      {"DELETE FROM agent_identity_keys WHERE agent_id = ?", agentID, "identity keys"},
		"broker online":     {"UPDATE runtime_brokers SET status = 'online' WHERE id = ?", brokerID, "want offline"},
		"broker heartbeat":  {"UPDATE runtime_brokers SET last_heartbeat = '2026-10-07 12:00:00+00:00' WHERE id = ?", brokerID, "lastHeartbeat is set"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := newHubDB(t)
			_, err := Run(ctx, runOpts(db, writeRecipe(t, r)))
			require.NoError(t, err)
			sdb, err := sql.Open("sqlite", "file:"+db)
			require.NoError(t, err)
			_, err = sdb.Exec(tc.stmt, tc.arg)
			require.NoError(t, err)
			require.NoError(t, sdb.Close())

			fs, err := openStore(ctx, db)
			require.NoError(t, err)
			defer func() { _ = fs.Close() }()
			require.ErrorContains(t, fs.Verify(ctx, r), tc.want)
		})
	}
}
