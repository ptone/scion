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

package enttest

import (
	"context"
	"os"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
)

// TestMain wires the package's Postgres lifecycle (MainSetup/MainTeardown):
// a no-op in the default (SQLite) build, and provisioning/dropping a
// per-package ephemeral Postgres database in the `integration` build when
// SCION_TEST_POSTGRES_URL is set.
func TestMain(m *testing.M) {
	MainSetup()
	code := m.Run()
	MainTeardown()
	os.Exit(code)
}

// TestAutoMigrate_FreshPostgres_ThenIdempotent is the fresh-database
// regression test for the normalizeBrokerLabels migration bug
// (pkg/ent/entc/client.go): two UPDATE runtime_brokers statements failed
// with 42P01 on a database where that table didn't exist yet, and the
// discarded Go error left the migration transaction aborted, so the next
// statement (the real migration's own SAVEPOINT) failed with an unrelated-
// looking SQLSTATE 25P02.
//
// Skips cleanly (does nothing) unless built with -tags integration and run
// with SCION_TEST_POSTGRES_URL set to a live Postgres server — see the
// enttest package doc and enttest_postgres.go.
//
//	go test -tags integration ./pkg/store/enttest/... \
//	  -run TestAutoMigrate_FreshPostgres_ThenIdempotent -v
//	SCION_TEST_POSTGRES_URL=postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable
func TestAutoMigrate_FreshPostgres_ThenIdempotent(t *testing.T) {
	if !Active() {
		t.Skip("SCION_TEST_POSTGRES_URL not set (or built without -tags integration); skipping Postgres-only regression test")
	}

	// NewClient creates a brand-new, empty schema and calls entc.AutoMigrate
	// on it internally (enttest_postgres.go:158) — this alone is the exact
	// fresh-database scenario that crashed in production. NewClient calls
	// t.Fatalf on migration failure, so reaching the next line already
	// proves the first, fresh-database migration succeeded.
	client := NewClient(t)

	// Re-running migration against the now-populated schema must also
	// succeed: this exercises the UPDATE statements' normal path (the
	// table exists now) and skipExistingRelations' 42P07 "already exists"
	// skip path together, proving the fix doesn't just move the bug from
	// "table missing" to "table present".
	if err := entc.AutoMigrate(context.Background(), client); err != nil {
		t.Fatalf("second AutoMigrate run against an already-migrated schema failed (idempotence broken): %v", err)
	}
}
