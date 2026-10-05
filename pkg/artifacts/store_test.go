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

package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// storeBackend opens a fresh, empty database of one dialect. The returned
// open function yields additional handles on the same database, for
// concurrency tests.
type storeBackend struct {
	name   string
	driver string
	open   func(t *testing.T) (db *sql.DB, reopen func() *sql.DB)
}

// testBackends returns SQLite always and Postgres when
// SCION_TEST_POSTGRES_DSN names a server (the same variable the hub's web
// chat store tests use). Each Postgres test runs in its own schema, which
// is dropped afterwards.
func testBackends() []storeBackend {
	b := []storeBackend{{
		name:   "sqlite",
		driver: "sqlite",
		open: func(t *testing.T) (*sql.DB, func() *sql.DB) {
			path := filepath.Join(t.TempDir(), "artifacts.db")
			dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
			reopen := func() *sql.DB {
				db, err := sql.Open("sqlite", dsn)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return db
			}
			return reopen(), reopen
		},
	}}
	dsn := os.Getenv("SCION_TEST_POSTGRES_DSN")
	if dsn == "" {
		return b
	}
	return append(b, storeBackend{
		name:   "postgres",
		driver: "postgres",
		open: func(t *testing.T) (*sql.DB, func() *sql.DB) {
			schema := "artifacts_test_" + strings.ReplaceAll(uuid.NewString()[:13], "-", "")
			admin, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
				t.Fatalf("create schema: %v", err)
			}
			t.Cleanup(func() {
				_, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE")
				_ = admin.Close()
			})
			sep := "?"
			if strings.Contains(dsn, "?") {
				sep = "&"
			}
			scoped := dsn + sep + "search_path=" + schema
			reopen := func() *sql.DB {
				db, err := sql.Open("pgx", scoped)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return db
			}
			return reopen(), reopen
		},
	})
}

// forEachBackend runs fn against a fresh database of every available
// dialect.
func forEachBackend(t *testing.T, fn func(t *testing.T, db *sql.DB, st Store, reopen func() *sql.DB)) {
	t.Helper()
	for _, b := range testBackends() {
		t.Run(b.name, func(t *testing.T) {
			db, reopen := b.open(t)
			st := NewStore(db, b.driver)
			if err := st.Init(context.Background()); err != nil {
				t.Fatalf("Init: %v", err)
			}
			fn(t, db, st, func() *sql.DB { return reopen() })
		})
	}
}

func TestNewStoreDialect(t *testing.T) {
	for driver, want := range map[string]dialect{
		"postgres": dialectPostgres, "pgx": dialectPostgres,
		"sqlite": dialectSQLite, "": dialectSQLite, "sqlite3": dialectSQLite,
	} {
		if got := NewStore(nil, driver).(*sqlStore).dialect; got != want {
			t.Errorf("NewStore(%q) dialect %v, want %v", driver, got, want)
		}
	}
}

func TestRebind(t *testing.T) {
	pg := &sqlStore{dialect: dialectPostgres}
	if got := pg.rebind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Errorf("postgres rebind = %q", got)
	}
	lite := &sqlStore{dialect: dialectSQLite}
	if got := lite.rebind("a = ?"); got != "a = ?" {
		t.Errorf("sqlite rebind = %q", got)
	}
}

func TestStoreInitIsIdempotent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *sql.DB, st Store, reopen func() *sql.DB) {
		ctx := context.Background()
		seedArtifact(t, st, "")
		for i := 0; i < 2; i++ {
			if err := st.Init(ctx); err != nil {
				t.Fatalf("Init #%d: %v", i+2, err)
			}
		}
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM artifact_migrations").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != len(migrations) {
			t.Errorf("ledger has %d rows, want %d", n, len(migrations))
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM artifact").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("re-Init lost data: %d artifacts, want 1", n)
		}
		for _, table := range []string{"artifact", "artifact_version", "artifact_file", "artifact_grant", "artifact_message_ref", "artifact_migrations"} {
			if _, err := db.Exec("SELECT 1 FROM " + table + " WHERE 1 = 0"); err != nil {
				t.Errorf("table %s missing: %v", table, err)
			}
		}
	})
}

// TestStoreInitConcurrent models several hubs starting at once against one
// database (P1 acceptance, ptone/scion#3208: a Postgres hub starts twice
// without errors).
func TestStoreInitConcurrent(t *testing.T) {
	for _, b := range testBackends() {
		t.Run(b.name, func(t *testing.T) {
			_, reopen := b.open(t)
			var wg sync.WaitGroup
			errs := make([]error, 4)
			for i := range errs {
				db := reopen()
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					errs[i] = NewStore(db, b.driver).Init(context.Background())
				}(i)
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Errorf("Init #%d: %v", i, err)
				}
			}
		})
	}
}

// seedArtifact writes a one-file artifact with a home-scope read grant and
// returns its rows.
func seedArtifact(t *testing.T, st Store, key string) (*Artifact, *Version, File, Grant) {
	t.Helper()
	now := time.Date(2026, 10, 5, 12, 30, 45, 123456789, time.FixedZone("x", 3600))
	a := &Artifact{
		ID: uuid.NewString(), ScopeKind: ScopeKindProject, ScopeRef: "project-1",
		OwnerKind: PrincipalKindAgent, OwnerRef: "agent-1", Key: key, Title: "Design",
		CreatedAt: now, UpdatedAt: now,
	}
	v := &Version{
		ID: uuid.NewString(), ArtifactID: a.ID, Seq: 1, Kind: VersionKindPublish, EntryPath: "design.md",
		TotalBytes: 5, FileCount: 1, CreatedByKind: a.OwnerKind, CreatedByRef: a.OwnerRef,
		CreatedAt: now, State: VersionStateReady,
	}
	f := File{VersionID: v.ID, Path: "design.md", Size: 5, SHA256: strings.Repeat("ab", 32), MediaType: "text/markdown"}
	g := Grant{
		ID: uuid.NewString(), ArtifactID: a.ID, SubjectKind: SubjectScope, SubjectRef: a.ScopeRef,
		Permission: GrantRead, CreatedByRef: PrincipalRef(a.OwnerKind, a.OwnerRef), CreatedAt: now,
	}
	if err := st.CreatePublished(context.Background(), a, v, []File{f}, []Grant{g}); err != nil {
		t.Fatalf("CreatePublished: %v", err)
	}
	return a, v, f, g
}

// dbRounded is t as the store reads it back: UTC, microsecond precision.
func dbRounded(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func TestStoreRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *sql.DB, st Store, _ func() *sql.DB) {
		ctx := context.Background()
		a, v, f, g := seedArtifact(t, st, "")

		got, err := st.GetArtifact(ctx, a.ID)
		if err != nil {
			t.Fatalf("GetArtifact: %v", err)
		}
		want := *a
		want.CurrentSeq = 1
		want.CreatedAt = dbRounded(a.CreatedAt)
		want.UpdatedAt = dbRounded(a.UpdatedAt)
		if fmt.Sprint(*got) != fmt.Sprint(want) {
			t.Errorf("GetArtifact:\n got %+v\nwant %+v", *got, want)
		}
		if got.Key != "" || got.ExpiresAt != nil || got.DeletedAt != nil {
			t.Errorf("nullable columns not null: %+v", got)
		}

		gv, err := st.GetVersion(ctx, a.ID, 1)
		if err != nil {
			t.Fatalf("GetVersion: %v", err)
		}
		wantV := *v
		wantV.CreatedAt = dbRounded(v.CreatedAt)
		if *gv != wantV {
			t.Errorf("GetVersion:\n got %+v\nwant %+v", *gv, wantV)
		}

		files, err := st.ListFiles(ctx, v.ID)
		if err != nil || len(files) != 1 || files[0] != f {
			t.Errorf("ListFiles = %+v, %v; want [%+v]", files, err, f)
		}
		gf, err := st.GetFile(ctx, v.ID, "design.md")
		if err != nil || *gf != f {
			t.Errorf("GetFile = %+v, %v; want %+v", gf, err, f)
		}

		grants, err := st.ListGrants(ctx, a.ID)
		if err != nil || len(grants) != 1 {
			t.Fatalf("ListGrants = %+v, %v", grants, err)
		}
		wantG := g
		wantG.CreatedAt = dbRounded(g.CreatedAt)
		if fmt.Sprint(grants[0]) != fmt.Sprint(wantG) {
			t.Errorf("ListGrants:\n got %+v\nwant %+v", grants[0], wantG)
		}
	})
}

func TestStoreNotFound(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *sql.DB, st Store, _ func() *sql.DB) {
		ctx := context.Background()
		a, v, _, _ := seedArtifact(t, st, "")
		checks := map[string]error{}
		_, checks["missing artifact"] = st.GetArtifact(ctx, uuid.NewString())
		_, checks["malformed id"] = st.GetArtifact(ctx, "not-a-uuid/../x")
		_, checks["missing version"] = st.GetVersion(ctx, a.ID, 2)
		_, checks["missing file"] = st.GetFile(ctx, v.ID, "other.md")
		for name, err := range checks {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", name, err)
			}
		}
		grants, err := st.ListGrants(ctx, uuid.NewString())
		if err != nil || len(grants) != 0 {
			t.Errorf("ListGrants(missing) = %v, %v; want empty", grants, err)
		}

		// A soft-deleted artifact is not found.
		if _, err := db.Exec(st.(*sqlStore).rebind("UPDATE artifact SET deleted_at = ? WHERE id = ?"),
			st.(*sqlStore).timeArg(time.Now()), a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetArtifact(ctx, a.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("soft-deleted artifact: err = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreKeyUniquePerOwnerAndScope(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *sql.DB, st Store, _ func() *sql.DB) {
		seedArtifact(t, st, "reports/q3")
		// Two artifacts without a key never collide.
		seedArtifact(t, st, "")
		seedArtifact(t, st, "")

		a := &Artifact{
			ID: uuid.NewString(), ScopeKind: ScopeKindProject, ScopeRef: "project-1",
			OwnerKind: PrincipalKindAgent, OwnerRef: "agent-1", Key: "reports/q3", Title: "dup",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		v := &Version{ID: uuid.NewString(), ArtifactID: a.ID, Seq: 1, Kind: VersionKindPublish,
			EntryPath: "x", CreatedAt: time.Now(), State: VersionStateReady}
		if err := st.CreatePublished(context.Background(), a, v, nil, nil); err == nil {
			t.Fatal("duplicate key for the same owner and scope was accepted")
		}
		// The same key under another owner is fine.
		a.ID, a.OwnerRef = uuid.NewString(), "agent-2"
		v.ID, v.ArtifactID = uuid.NewString(), a.ID
		if err := st.CreatePublished(context.Background(), a, v, nil, nil); err != nil {
			t.Fatalf("same key, other owner: %v", err)
		}
	})
}

func TestStoreCreatePublishedIsAtomic(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *sql.DB, st Store, _ func() *sql.DB) {
		now := time.Now()
		a := &Artifact{ID: uuid.NewString(), ScopeKind: ScopeKindProject, ScopeRef: "p",
			OwnerKind: PrincipalKindUser, OwnerRef: "u", Title: "t", CreatedAt: now, UpdatedAt: now}
		v := &Version{ID: uuid.NewString(), ArtifactID: a.ID, Seq: 1, Kind: VersionKindPublish,
			EntryPath: "a.txt", CreatedAt: now, State: VersionStateReady}
		files := []File{
			{VersionID: v.ID, Path: "a.txt", SHA256: "x", MediaType: "text/plain"},
			{VersionID: v.ID, Path: "a.txt", SHA256: "y", MediaType: "text/plain"}, // duplicate PK
		}
		if err := st.CreatePublished(context.Background(), a, v, files, nil); err == nil {
			t.Fatal("duplicate file path was accepted")
		}
		if _, err := st.GetArtifact(context.Background(), a.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("failed publish left an artifact row behind: %v", err)
		}
	})
}
