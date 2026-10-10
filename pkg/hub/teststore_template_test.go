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

package hub

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// testStoreDB returns the *sql.DB behind a test store.
func testStoreDB(t *testing.T, s store.Store) *sql.DB {
	t.Helper()
	d, ok := s.(interface{ DB() *sql.DB })
	require.True(t, ok, "test store %T exposes no DB()", s)
	db := d.DB()
	require.NotNil(t, db)
	return db
}

// sqliteSchema returns every schema object of db as "type name tbl_name sql".
func sqliteSchema(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var typ, name, tbl, def string
		require.NoError(t, rows.Scan(&typ, &name, &tbl, &def))
		out = append(out, fmt.Sprintf("%s %s %s %s", typ, name, tbl, def))
	}
	require.NoError(t, rows.Err())
	return out
}

// sqliteRowCounts returns the row count of every table in db.
func sqliteRowCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	_ = rows.Close()
	counts := make(map[string]int, len(tables))
	for _, name := range tables {
		var n int
		require.NoError(t, db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %q`, name)).Scan(&n))
		counts[name] = n
	}
	return counts
}

// TestNewTestStoreTemplateMatchesMigrate checks that a newTestStore(":memory:")
// store, which is a copy of the migrate-once template, has the same schema
// and the same seeded and backfilled rows as a store that ran the full
// Migrate itself, and keeps the connection's foreign-key enforcement.
func TestNewTestStoreTemplateMatchesMigrate(t *testing.T) {
	restored, err := newTestStore(t, ":memory:")
	require.NoError(t, err)
	migrated, err := newTestStoreAt(t, fmt.Sprintf("file:hubtestmigrated%d?mode=memory&cache=shared", testStoreSeq.Add(1)))
	require.NoError(t, err)

	rdb, mdb := testStoreDB(t, restored), testStoreDB(t, migrated)
	require.Equal(t, sqliteSchema(t, mdb), sqliteSchema(t, rdb))
	require.Equal(t, sqliteRowCounts(t, mdb), sqliteRowCounts(t, rdb))

	var fk int
	require.NoError(t, rdb.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	require.Equal(t, 1, fk, "foreign keys stay on after the template restore")
}

// TestNewTestStoreTemplateIsolation checks that template copies share
// nothing: a write to one store is visible neither in another store nor in
// a store restored after the write (so the template itself is unchanged).
func TestNewTestStoreTemplateIsolation(t *testing.T) {
	s1, err := newTestStore(t, ":memory:")
	require.NoError(t, err)
	s2, err := newTestStore(t, ":memory:")
	require.NoError(t, err)

	_, err = testStoreDB(t, s1).Exec(`CREATE TABLE isolation_probe (x INTEGER)`)
	require.NoError(t, err)
	_, err = testStoreDB(t, s1).Exec(`INSERT INTO isolation_probe (x) VALUES (1)`)
	require.NoError(t, err)

	s3, err := newTestStore(t, ":memory:")
	require.NoError(t, err)

	probe := func(s store.Store) int {
		var n int
		require.NoError(t, testStoreDB(t, s).QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE name = 'isolation_probe'`).Scan(&n))
		return n
	}
	require.Equal(t, 1, probe(s1))
	require.Equal(t, 0, probe(s2), "a parallel copy must not see another copy's writes")
	require.Equal(t, 0, probe(s3), "a later copy must not see an earlier copy's writes")
}
