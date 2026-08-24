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
	if link.URL != "https://example.com" {
		t.Errorf("expected url https://example.com, got %q", link.URL)
	}

	got, err := repo.GetLink(link.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.URL != link.URL {
		t.Errorf("stored url mismatch: got %q want %q", got.URL, link.URL)
	}
}

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
	if updated.URL != "https://new.example.com" {
		t.Errorf("expected updated url, got %q", updated.URL)
	}

	got, err := repo.GetLink(added.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.URL != "https://new.example.com" {
		t.Errorf("update not persisted: got %q", got.URL)
	}
}
