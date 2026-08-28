package facts

import (
	"context"
	"database/sql"
	"testing"
)

// TestMockAnalyzerReturnsProgrammed verifies the mock ignores its input and
// returns the fixed answer (and error) it was configured with.
func TestMockAnalyzerReturnsProgrammed(t *testing.T) {
	m := MockAnalyzer{Result: `{"mock":true}`}
	got, err := m.Analyze(context.Background(), "anything at all")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"mock":true}` {
		t.Errorf("result = %q, want %q", got, `{"mock":true}`)
	}

	mErr := MockAnalyzer{Err: sql.ErrNoRows}
	if _, err := mErr.Analyze(context.Background(), ""); err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

// TestProcessOnceAnalyzesAndSaves verifies a link with readable text gets
// analyzed and its result persisted as a pass with a matching content hash.
func TestProcessOnceAnalyzesAndSaves(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPassesRepository(db)
	linkID := insertLink(t, db)

	if _, err := db.Exec("UPDATE link_queue SET readable_text = ?, last_scrapped_at = CURRENT_TIMESTAMP WHERE id = ?",
		"Apple acquired NeXT in 1996.", linkID); err != nil {
		t.Fatalf("set readable_text: %v", err)
	}

	m := NewFactsMachine(db, MockAnalyzer{Result: `{"entities":[{"id":"APPLE","type":"Organization","properties":{"name":"Apple"}}]}`}, 0, "facts",
		NewGoqiteEntityPublisher(db))
	if err := m.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("process once: %v", err)
	}

	// The single "Apple" entity extracted by the pass must be published as a
	// lifecycle event through the real goqite publisher.
	events := drainEntityEvents(t, db)
	if len(events) < 1 {
		t.Fatalf("expected at least 1 entity event, got %d", len(events))
	}
	if events[0].Name != "Apple" {
		t.Errorf("event[0].Name = %q, want Apple", events[0].Name)
	}
	if events[0].ID == 0 {
		t.Errorf("event[0].ID = 0, want a real entity id")
	}

	pass, err := repo.GetPassByLink(linkID, "facts", 0)
	if err != nil {
		t.Fatalf("get pass: %v", err)
	}
	if pass.Result != `{"entities":[{"id":"APPLE","type":"Organization","properties":{"name":"Apple"}}]}` {
		t.Errorf("stored result = %q", pass.Result)
	}
	if pass.ContentHash != HashContent("Apple acquired NeXT in 1996.") {
		t.Errorf("content hash does not match hashed readable text")
	}
}

// TestProcessOnceSkipsProcessedLink verifies an already-analyzed link is not
// processed again, so re-running never creates a second pass for the same link.
func TestProcessOnceSkipsProcessedLink(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	if _, err := db.Exec("UPDATE link_queue SET readable_text = ?, last_scrapped_at = CURRENT_TIMESTAMP WHERE id = ?",
		"some text", linkID); err != nil {
		t.Fatalf("set readable_text: %v", err)
	}

	m := NewFactsMachine(db, MockAnalyzer{Result: "r1"}, 0, "test", NewGoqiteEntityPublisher(db))
	if err := m.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := m.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM passes WHERE link_queue_id = ?", linkID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 pass for processed link, got %d", n)
	}
}

// TestProcessOnceSkipsNotScrapedLink verifies that a link is only picked up
// once it has been scraped (last_scrapped_at set) — having readable text alone
// is not enough.
func TestProcessOnceSkipsNotScrapedLink(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	// readable_text present, but last_scrapped_at still NULL => not yet scraped.
	if _, err := db.Exec("UPDATE link_queue SET readable_text = ? WHERE id = ?",
		"some text", linkID); err != nil {
		t.Fatalf("set readable_text: %v", err)
	}

	m := NewFactsMachine(db, MockAnalyzer{Result: "r"}, 0, "facts", NewGoqiteEntityPublisher(db))
	if err := m.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("process once: %v", err)
	}

	_, err := NewPassesRepository(db).GetPassByLink(linkID, "facts", 0)
	if err != sql.ErrNoRows {
		t.Errorf("expected no pass for unscraped link, got %v", err)
	}
}
