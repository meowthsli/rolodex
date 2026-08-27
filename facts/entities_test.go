package facts

import (
	"context"
	"testing"
)

func TestCanonKeyOrderInvariant(t *testing.T) {
	a := canonKey("Евгений Голанд")
	b := canonKey("Голанд Евгений")
	if a != b {
		t.Errorf("canonKey not order-invariant: %q vs %q", a, b)
	}
	// The model's uppercase id should align with the transliterated name.
	if got := canonKey("GORIN_EVGENIY"); got != "evgeniy_gorin" {
		t.Errorf("canonKey(modelID) = %q, want evgeniy_gorin", got)
	}
}

func TestTypesCompatible(t *testing.T) {
	if typesCompatible([]string{"Person"}, []string{"Investor"}) != true {
		t.Error("Person and Investor should be mergeable")
	}
	if typesCompatible([]string{"Date"}, []string{"Person"}) != false {
		t.Error("Date must not merge with Person")
	}
	if typesCompatible([]string{"Date"}, []string{"Date"}) != true {
		t.Error("Date should merge with Date")
	}
	if typesCompatible([]string{}, []string{"Person"}) != true {
		t.Error("empty type side should always merge")
	}
	if typesCompatible([]string{"Date", "Person"}, []string{"Date"}) != false {
		t.Error("Date+Person must not merge with pure Date")
	}
}

func TestExtractPassMergesNameVariants(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := NewEntitiesRepository(db)
	ctx := context.Background()

	p1, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h", `{"entities":[{"id":"GORLAND_EVGENIY","type":"Person","properties":{"name":"Евгений Голанд"}}]}`)
	p2, _ := passes.UpsertPass(linkID, "d", 1, 0, 5, "t", "h", `{"entities":[{"id":"GORLAND_EVGENIY","type":"Person","properties":{"name":"Голанд Евгений"}}]}`)

	if err := repo.ExtractPass(ctx, p1); err != nil {
		t.Fatalf("ExtractPass 1: %v", err)
	}
	if err := repo.ExtractPass(ctx, p2); err != nil {
		t.Fatalf("ExtractPass 2: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 canonical entity, got %d", n)
	}

	var mentions int
	if err := db.QueryRow("SELECT COUNT(*) FROM entity_mentions").Scan(&mentions); err != nil {
		t.Fatal(err)
	}
	if mentions != 2 {
		t.Errorf("expected 2 mentions, got %d", mentions)
	}

	// Both spellings and the model id must resolve to the same entity.
	e1, ok1, _ := repo.lookupAlias(canonKey("Евгений Голанд"))
	e2, ok2, _ := repo.lookupAlias(canonKey("Голанд Евгений"))
	e3, ok3, _ := repo.lookupAlias(canonKey("GORLAND_EVGENIY"))
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("expected all three forms to resolve: %v %v %v", ok1, ok2, ok3)
	}
	if e1.ID != e2.ID || e1.ID != e3.ID {
		t.Errorf("expected all forms to map to one entity, got ids %d %d %d", e1.ID, e2.ID, e3.ID)
	}
}

func TestExtractionPromotesKnownAfterThreshold(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := NewEntitiesRepository(db)
	ctx := context.Background()

	const result = `{"entities":[{"id":"X1","type":"Startup","properties":{"name":"Одна И Та же Компания"}}]}`
	for i := 0; i < promotionThreshold; i++ {
		p, _ := passes.UpsertPass(linkID, "d", i, 0, 5, "t", "h", result)
		if err := repo.ExtractPass(ctx, p); err != nil {
			t.Fatalf("ExtractPass %d: %v", i, err)
		}
	}

	e, ok, err := repo.lookupAlias(canonKey("Одна И Та же Компания"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || e.ID == 0 {
		t.Fatal("entity not found")
	}
	// Re-fetch to read is_known after the promotion update.
	got, err := repo.GetEntity(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PromotionScore < promotionThreshold {
		t.Errorf("promotion_score = %d, want >= %d", got.PromotionScore, promotionThreshold)
	}
	if !got.IsKnown {
		t.Errorf("entity should be auto-promoted to known after %d foundings", promotionThreshold)
	}
}

// TestGlobalReconcileMergesFuzzy exercises the table-wide merge. It is skipped
// unless the driver was built with -tags sqlite_fts5 (fuzzy matching requires
// the fts5 module).
func TestGlobalReconcileMergesFuzzy(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := NewEntitiesRepository(db)
	if !repo.FTSAvailable() {
		t.Skip("fts5 not compiled; build with -tags sqlite_fts5 to exercise fuzzy merge")
	}
	ctx := context.Background()

	// Two near-identical startups (one is the other plus a single extra token)
	// -> similarity 8/9 ~0.89, above the 0.87 threshold, so they merge.
	pA, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h", `{"entities":[{"id":"SA","type":"Startup","properties":{"name":"А Б В Г Д Е Ж Петров"}}]}`)
	pB, _ := passes.UpsertPass(linkID, "d", 1, 0, 5, "t", "h", `{"entities":[{"id":"SB","type":"Startup","properties":{"name":"А Б В Г Д Е Ж Петров Доп"}}]}`)
	// A Date entity with a similar name -> must NOT merge with the startup.
	pC, _ := passes.UpsertPass(linkID, "d", 2, 0, 5, "t", "h", `{"entities":[{"id":"DT","type":"Date","properties":{"name":"А Б В Г Д Е Ж Петров Дата"}}]}`)

	for _, p := range []Pass{pA, pB, pC} {
		if err := repo.ExtractPass(ctx, p); err != nil {
			t.Fatalf("ExtractPass %s: %v", p.Result, err)
		}
	}

	merged, err := repo.GlobalReconcile(ctx)
	if err != nil {
		t.Fatalf("GlobalReconcile: %v", err)
	}
	if merged < 1 {
		t.Errorf("expected at least 1 fuzzy merge, got %d", merged)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&n); err != nil {
		t.Fatal(err)
	}
	// One startup survivor + one Date entity = 2.
	if n != 2 {
		t.Errorf("expected 2 entities after reconcile (1 startup + 1 date), got %d", n)
	}

	// The startup merge must have recorded a redirect.
	var redirects int
	if err := db.QueryRow("SELECT COUNT(*) FROM entity_redirects").Scan(&redirects); err != nil {
		t.Fatal(err)
	}
	if redirects < 1 {
		t.Errorf("expected at least 1 redirect recorded, got %d", redirects)
	}
}
