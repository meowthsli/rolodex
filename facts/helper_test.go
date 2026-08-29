package facts

import (
	"database/sql"
	"path/filepath"
	"testing"

	sq "github.com/bokwoon95/sq"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/mattn/go-sqlite3"
)

// setupTestDB spins up a fresh, migrated SQLite database in a temp dir and
// returns a handle to it. The database file is removed automatically when the
// test finishes.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")

	m, err := migrate.New("file://../migrations", "sqlite3://"+dbPath)
	if err != nil {
		t.Fatalf("migrate new: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Provision the FTS5 index once for the test process, mirroring the app's
	// single InitFTS call at startup.
	InitFTS(db)

	return db
}

// insertLink inserts a minimal link_queue row (required to satisfy the
// passes.link_queue_id foreign key) and returns its id.
func insertLink(t *testing.T, db *sql.DB) int {
	t.Helper()

	if _, err := sq.Exec(db, sq.SQLite.Queryf("INSERT INTO link_queue (url) VALUES ({})", "https://example.com/"+t.Name())); err != nil {
		t.Fatalf("insert link: %v", err)
	}
	id, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT last_insert_rowid() AS id"), func(row *sq.Row) int { return row.Int("id") })
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}
