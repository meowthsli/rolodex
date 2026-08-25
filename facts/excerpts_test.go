package facts

import (
	"testing"
)

// TestSaveExcerptDeDup verifies that saving the same span twice (same pass and
// offsets) does not create a duplicate row and returns the canonical excerpt.
func TestSaveExcerptDeDup(t *testing.T) {
	db := setupTestDB(t)
	passes := NewPassesRepository(db)
	excerpts := NewExcerptsRepository(db)
	linkID := insertLink(t, db)

	pass, err := passes.UpsertPass(linkID, "h", "{}")
	if err != nil {
		t.Fatalf("upsert pass: %v", err)
	}

	first, err := excerpts.SaveExcerpt(pass.ID, "Apple Inc.", 0, 10, HashSpan("Apple Inc."))
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	second, err := excerpts.SaveExcerpt(pass.ID, "Apple Inc.", 0, 10, HashSpan("Apple Inc."))
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("expected same excerpt id, got %d then %d", first.ID, second.ID)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM excerpts WHERE pass_id = ?", pass.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 excerpt after dedup, got %d", n)
	}
}

// TestSaveExcerptDistinctOffsets verifies different spans within the same pass
// are stored separately.
func TestSaveExcerptDistinctOffsets(t *testing.T) {
	db := setupTestDB(t)
	passes := NewPassesRepository(db)
	excerpts := NewExcerptsRepository(db)
	linkID := insertLink(t, db)

	pass, err := passes.UpsertPass(linkID, "h", "{}")
	if err != nil {
		t.Fatalf("upsert pass: %v", err)
	}
	if _, err := excerpts.SaveExcerpt(pass.ID, "Apple", 0, 5, HashSpan("Apple")); err != nil {
		t.Fatalf("save apple: %v", err)
	}
	if _, err := excerpts.SaveExcerpt(pass.ID, "NeXT", 20, 24, HashSpan("NeXT")); err != nil {
		t.Fatalf("save next: %v", err)
	}

	all, err := excerpts.ListExcerptsByPass(pass.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 excerpts, got %d", len(all))
	}
}

// TestListExcerptsByLinkJoinsThroughPass verifies excerpts can be reached from
// a link even though excerpts only reference the pass (no direct link column).
func TestListExcerptsByLinkJoinsThroughPass(t *testing.T) {
	db := setupTestDB(t)
	passes := NewPassesRepository(db)
	excerpts := NewExcerptsRepository(db)
	linkID := insertLink(t, db)

	pass, err := passes.UpsertPass(linkID, "h", "{}")
	if err != nil {
		t.Fatalf("upsert pass: %v", err)
	}
	if _, err := excerpts.SaveExcerpt(pass.ID, "span", 0, 4, HashSpan("span")); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := excerpts.ListExcerptsByLink(linkID)
	if err != nil {
		t.Fatalf("list by link: %v", err)
	}
	if len(got) != 1 || got[0].Text != "span" {
		t.Errorf("expected one 'span' excerpt for link, got %+v", got)
	}
}
