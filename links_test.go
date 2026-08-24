package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")

	m, err := migrate.New("file://migrations", "sqlite3://"+dbPath)
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

	return db
}

// TestAddLink verifies that a new link can be inserted and that its URL is
// normalized (scheme stripped) before being persisted.
func TestAddLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	link, err := repo.AddLink("https://example.com")
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	if link.ID == 0 {
		t.Errorf("expected non-zero id, got %d", link.ID)
	}
	if link.URL != "example.com" {
		t.Errorf("expected scheme-less url example.com, got %q", link.URL)
	}

	got, err := repo.GetLink(link.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.URL != link.URL {
		t.Errorf("stored url mismatch: got %q want %q", got.URL, link.URL)
	}
}

// TestUpdateLink verifies that an existing link's URL can be changed and that
// the update is persisted, with the new URL also stored without a scheme.
func TestUpdateLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	added, err := repo.AddLink("https://old.example.com")
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	updated, err := repo.UpdateLink(added.ID, "https://new.example.com")
	if err != nil {
		t.Fatalf("UpdateLink: %v", err)
	}

	if updated.ID != added.ID {
		t.Errorf("id changed: got %d want %d", updated.ID, added.ID)
	}
	if updated.URL != "new.example.com" {
		t.Errorf("expected updated url, got %q", updated.URL)
	}

	got, err := repo.GetLink(added.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.URL != "new.example.com" {
		t.Errorf("update not persisted: got %q", got.URL)
	}
}

// TestNormalizeURL checks that normalizeURL strips http/https prefixes across
// various casings and leaves scheme-less URLs untouched.
func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com": "example.com",
		"http://example.com":  "example.com",
		"HTTPS://Example.com": "Example.com",
		"example.com":         "example.com",
		"https://example.com/path?q=1": "example.com/path?q=1",
	}
	for in, want := range cases {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNewLink checks that the link constructor strips the scheme and leaves
// all other fields in their zero value.
func TestNewLink(t *testing.T) {
	l := NewLink("https://example.com/page")
	if l.URL != "example.com/page" {
		t.Errorf("NewLink stripped scheme: got %q want %q", l.URL, "example.com/page")
	}
	if l.ID != 0 || l.Content != "" || l.Error.Valid || l.LastScrapped.Valid {
		t.Errorf("NewLink should only set URL, got %+v", l)
	}
}
