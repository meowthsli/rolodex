package db

import (
	"database/sql"
	"testing"
)

// TestPendingIndexExists verifies the partial index over pending
// (not-yet-scraped) links is created by the migrations.
func TestPendingIndexExists(t *testing.T) {
	db := setupTestDB(t)

	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_link_queue_pending'",
	).Scan(&name)
	if err == sql.ErrNoRows {
		t.Fatal("expected partial index idx_link_queue_pending to exist")
	}
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if name != "idx_link_queue_pending" {
		t.Errorf("unexpected index name %q", name)
	}
}
