package facts

import (
	"database/sql"
	"testing"

	sq "github.com/bokwoon95/sq"
)

// TestUpsertPassStoresResultAndHash verifies that a pass is created with the
// supplied link, content hash and JSON result.
func TestUpsertPassStoresResultAndHash(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPassesRepository(db)
	linkID := insertLink(t, db)

	got, err := repo.UpsertPass(linkID, "facts", 0, 0, 0, "", HashContent("readable"), `{"entities":[]}`)
	if err != nil {
		t.Fatalf("upsert pass: %v", err)
	}
	if got.LinkQueueID != linkID {
		t.Errorf("link_queue_id = %d, want %d", got.LinkQueueID, linkID)
	}
	if got.ContentHash != HashContent("readable") {
		t.Errorf("content_hash not stored")
	}
	if got.Result != `{"entities":[]}` {
		t.Errorf("result = %q", got.Result)
	}
}

// TestUpsertPassOverwritesSameLink verifies the one-pass-per-(link, domain,
// chunk) contract: re-running a pass for the same triple updates the row in
// place (same id, new result) instead of inserting a second row.
func TestUpsertPassOverwritesSameLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPassesRepository(db)
	linkID := insertLink(t, db)

	first, err := repo.UpsertPass(linkID, "facts", 0, 0, 0, "", "hash1", "result1")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second, err := repo.UpsertPass(linkID, "facts", 0, 0, 0, "", "hash2", "result2")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("expected same pass id after re-run, got %d then %d", first.ID, second.ID)
	}
	if second.Result != "result2" {
		t.Errorf("result not refreshed: %q", second.Result)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM passes WHERE link_queue_id = {} AND domain = {}", linkID, "facts"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 pass per (link, domain, chunk), got %d", n)
	}
}

// TestUpsertPassSeparatesDomains verifies that two passes for the same link but
// different domains coexist as distinct rows rather than overwriting each other.
func TestUpsertPassSeparatesDomains(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPassesRepository(db)
	linkID := insertLink(t, db)

	if _, err := repo.UpsertPass(linkID, "facts", 0, 0, 0, "", "hash", "facts-result"); err != nil {
		t.Fatalf("upsert facts: %v", err)
	}
	if _, err := repo.UpsertPass(linkID, "entities", 0, 0, 0, "", "hash", "entities-result"); err != nil {
		t.Fatalf("upsert entities: %v", err)
	}

	factsPass, err := repo.GetPassByLink(linkID, "facts", 0)
	if err != nil {
		t.Fatalf("get facts pass: %v", err)
	}
	if factsPass.Result != "facts-result" {
		t.Errorf("facts result = %q", factsPass.Result)
	}
	entitiesPass, err := repo.GetPassByLink(linkID, "entities", 0)
	if err != nil {
		t.Fatalf("get entities pass: %v", err)
	}
	if entitiesPass.Result != "entities-result" {
		t.Errorf("entities result = %q", entitiesPass.Result)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM passes WHERE link_queue_id = {}", linkID),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 passes for the two domains, got %d", n)
	}
}

// TestUpsertPassSeparatesChunks verifies that distinct chunks of the same
// link/domain are stored as separate rows (one per chunk_index).
func TestUpsertPassSeparatesChunks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPassesRepository(db)
	linkID := insertLink(t, db)

	if _, err := repo.UpsertPass(linkID, "facts", 0, 0, 10, "", "h", "chunk0"); err != nil {
		t.Fatalf("upsert chunk0: %v", err)
	}
	if _, err := repo.UpsertPass(linkID, "facts", 1, 10, 20, "", "h", "chunk1"); err != nil {
		t.Fatalf("upsert chunk1: %v", err)
	}

	all, err := repo.ListPassesByLink(linkID, "facts")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 chunk passes, got %d", len(all))
	}
	if all[0].ChunkIndex != 0 || all[0].Result != "chunk0" {
		t.Errorf("chunk0 = %+v", all[0])
	}
	if all[1].ChunkIndex != 1 || all[1].ChunkStart != 10 || all[1].ChunkEnd != 20 {
		t.Errorf("chunk1 = %+v", all[1])
	}
}

// TestGetPassByLinkNotFound verifies a link with no analysis yet yields
// sql.ErrNoRows so callers can tell "not analyzed" apart from "failed".
func TestGetPassByLinkNotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPassesRepository(db)
	linkID := insertLink(t, db)

	_, err := repo.GetPassByLink(linkID, "facts", 0)
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

// TestSetPassErrorRecordsFailure verifies a failed pass is stored with an error
// message rather than being left absent.
func TestSetPassErrorRecordsFailure(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPassesRepository(db)
	linkID := insertLink(t, db)

	if err := repo.SetPassError(linkID, "facts", 0, 0, 0, "", "boom"); err != nil {
		t.Fatalf("set pass error: %v", err)
	}
	got, err := repo.GetPassByLink(linkID, "facts", 0)
	if err != nil {
		t.Fatalf("get pass: %v", err)
	}
	if !got.Error.Valid || got.Error.String != "boom" {
		t.Errorf("expected error 'boom', got %v", got.Error)
	}
}

// TestHashContentIsStable verifies the content hash is deterministic, which is
// what lets a later pass detect source drift.
func TestHashContentIsStable(t *testing.T) {
	a := HashContent("the quick brown fox")
	b := HashContent("the quick brown fox")
	if a != b {
		t.Errorf("hash not stable: %q != %q", a, b)
	}
	if HashContent("x") == HashContent("y") {
		t.Errorf("different inputs produced same hash")
	}
}
