package db

import (
	"database/sql"
	"errors"
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

	return db
}

// TestNewLink verifies that the constructor inserts a new link and normalizes
// its URL (scheme stripped) before persisting.
func TestNewLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	link, err := repo.NewLink("https://example.com")
	if err != nil {
		t.Fatalf("NewLink: %v", err)
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

// TestNewLinkSkipsDuplicate verifies that constructing a link for a URL that
// already exists does not create a second row and signals ErrLinkExists.
func TestNewLinkSkipsDuplicate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	first, err := repo.NewLink("https://example.com")
	if err != nil {
		t.Fatalf("first NewLink: %v", err)
	}

	// Same host but different scheme/normalized form should hit the duplicate.
	second, err := repo.NewLink("http://example.com")
	if !errors.Is(err, ErrLinkExists) {
		t.Fatalf("expected ErrLinkExists, got %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("duplicate should reference existing link: got id %d want %d", second.ID, first.ID)
	}

	// Same URL but with query parameters in a different order must also be
	// treated as the same (already existing) link.
	third, err := repo.NewLink("https://example.com?b=2&a=1")
	if err != nil {
		t.Fatalf("third NewLink: %v", err)
	}
	fourth, err := repo.NewLink("https://example.com?a=1&b=2")
	if !errors.Is(err, ErrLinkExists) {
		t.Fatalf("expected ErrLinkExists for reordered query, got %v", err)
	}
	if fourth.ID != third.ID {
		t.Errorf("reordered query should reference existing link: got id %d want %d", fourth.ID, third.ID)
	}

	all, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected exactly 2 links after duplicates, got %d", len(all))
	}
}

// TestGetNextPendingLinkRequeuesStaleContent verifies that GetNextPendingLink
// returns a link not only when it has never been scraped, but also when its
// stored content (last_scrapped_at) is older than the moment the link was
// (re)added (added_at). A link scraped in the past and rediscovered later
// should be picked up again for a refresh.
func TestGetNextPendingLinkRequeuesStaleContent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	// Fresh link: not yet scraped, so it is pending.
	link, err := repo.NewLink("https://stale.example.com")
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	pending, err := repo.GetNextPendingLink()
	if err != nil {
		t.Fatalf("GetNextPendingLink: %v", err)
	}
	if pending.ID != link.ID {
		t.Fatalf("expected fresh link to be pending, got id %d", pending.ID)
	}

	// Scrape it, recording a content timestamp in the past.
	if err := repo.SaveScrapeResult(link.ID, "<html></html>", "readable"); err != nil {
		t.Fatalf("SaveScrapeResult: %v", err)
	}

	// After scraping, the link should no longer be pending.
	pending, err = repo.GetNextPendingLink()
	if err != nil {
		t.Fatalf("GetNextPendingLink: %v", err)
	}
	if pending.ID != 0 {
		t.Fatalf("expected no pending link after scrape, got id %d", pending.ID)
	}

	// Rediscover the same URL: NewLink bumps added_at to now, which is later
	// than the stored content, so the link becomes pending again.
	if _, err := repo.NewLink("https://stale.example.com"); !errors.Is(err, ErrLinkExists) {
		t.Fatalf("expected ErrLinkExists on rediscovery, got %v", err)
	}
	pending, err = repo.GetNextPendingLink()
	if err != nil {
		t.Fatalf("GetNextPendingLink: %v", err)
	}
	if pending.ID != link.ID {
		t.Fatalf("expected rediscovered link to be pending again, got id %d", pending.ID)
	}
}

// TestUpdateLink verifies that an existing link's URL can be changed and that
// the update is persisted, with the new URL also stored without a scheme.
func TestUpdateLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	added, err := repo.NewLink("https://old.example.com")
	if err != nil {
		t.Fatalf("NewLink: %v", err)
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
