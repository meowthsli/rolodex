package facts

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestCreateEntityUnderPool reproduces the "sql: no rows in result set" failure
// that occurred in the running app: when *sql.DB uses a connection pool, reading
// the new id via a separate "SELECT last_insert_rowid()" query can land on a
// different connection than the INSERT and return 0. The fix uses
// Result.LastInsertId(), which is tied to the executing statement. This test
// forces a multi-connection pool and inserts concurrently to surface the bug.
func TestCreateEntityUnderPool(t *testing.T) {
	db := setupTestDB(t)
	db.SetMaxOpenConns(8)

	r := NewEntitiesRepository(db)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	ids := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e, err := r.createEntity("Entity Name", []string{"Person"}, []byte(`{"name":"Entity Name"}`))
			if err != nil {
				errs <- err
				return
			}
			if e.ID <= 0 {
				errs <- fmt.Errorf("createEntity returned invalid id %d", e.ID)
				return
			}
			ids <- e.ID
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)

	for err := range errs {
		t.Fatalf("createEntity under pool: %v", err)
	}
	seen := make(map[int]bool)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate entity id %d under pool", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct entities, got %d", n, len(seen))
	}
}

func TestCanonKeyOrderInvariant(t *testing.T) {
	a := canonKey("Евгений Голанд")
	b := canonKey("Голанд Евгений")
	if a != b {
		t.Errorf("canonKey not order-invariant: %q vs %q", a, b)
	}
	// The model's uppercase id should align with the transliterated name.
	if got := canonKey("GORIN_EVGENIY"); got != "evgeni_gorin" {
		t.Errorf("canonKey(modelID) = %q, want evgeni_gorin", got)
	}
}

// TestCanonKeyNameVariants verifies that different transliterations of the same
// name, and a full name with its diminutive, collapse to one canonical key.
func TestCanonKeyNameVariants(t *testing.T) {
	cases := []struct {
		a, b string
	}{
		{"Алексей", "Alexey"},  // Cyrillic vs Latin transliteration
		{"Алексей", "Aleksei"}, // both Latin variants
		{"Alexey", "Aleksei"},  // pure Latin variants
		{"Алексей", "Aleksey"}, // x -> ks rewrite
		{"Пётр", "Петя"},       // full name vs diminutive (Cyrillic)
		{"Пётр", "Petya"},      // diminutive in Latin form
		{"Пётр", "Pyotr"},      // another Latin form
	}
	for _, c := range cases {
		if got, want := canonKey(c.a), canonKey(c.b); got != want {
			t.Errorf("canonKey(%q)=%q but canonKey(%q)=%q; want them equal", c.a, got, c.b, want)
		}
	}
}

// TestCanonKeyTranslitFold verifies the systematic transliteration normalizer
// collapses spelling variants of the same name onto one key.
func TestCanonKeyTranslitFold(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Алексей", "Alexey"},  // kh/x + ey/ei
		{"Алексей", "Aleksey"}, // x -> ks
		{"Михаил", "Mihail"},   // kh -> h
		{"Михаил", "Michail"},  // explicit variant
		{"Дмитрий", "Dmitry"},  // trailing y -> i
		{"Егор", "Yegor"},      // ye -> ie
		{"Василий", "Vasily"},  // trailing y -> i
		{"Сергей", "Sergei"},   // ey -> ei
	}
	for _, c := range cases {
		if got, want := canonKey(c.a), canonKey(c.b); got != want {
			t.Errorf("canonKey(%q)=%q but canonKey(%q)=%q; want equal", c.a, got, c.b, want)
		}
	}
}

// TestCanonKeyDiminutives verifies that Russian hypocoristics collapse onto their
// full name form in the canonical key.
func TestCanonKeyDiminutives(t *testing.T) {
	cases := []struct{ dim, full string }{
		{"Саша", "Александр"},
		{"Петя", "Пётр"},
		{"Ваня", "Иван"},
		{"Миша", "Михаил"},
		{"Катя", "Екатерина"},
		{"Маша", "Мария"},
		{"Женя", "Евгений"},
		{"Коля", "Николай"},
	}
	for _, c := range cases {
		if got, want := canonKey(c.dim), canonKey(c.full); got != want {
			t.Errorf("canonKey(%q)=%q but canonKey(%q)=%q; want equal", c.dim, got, c.full, want)
		}
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

// TestExtractPassUnifiesTranslitAndDiminutive checks that the same real-world
// person written with a different transliteration (Алексей vs Alexey) or as a
// diminutive (Пётр vs Петя) is folded into one canonical entity.
func TestExtractPassUnifiesTranslitAndDiminutive(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := NewEntitiesRepository(db)
	ctx := context.Background()

	// Aleksei (Cyrillic) and Alexey (Latin) for the same surname must merge.
	p1, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h", `{"entities":[{"id":"AK1","type":"Person","properties":{"name":"Алексей Кривенков"}}]}`)
	p2, _ := passes.UpsertPass(linkID, "d", 1, 0, 5, "t", "h", `{"entities":[{"id":"AK2","type":"Person","properties":{"name":"Alexey Кривенков"}}]}`)
	// Пётр (full) and Петя (diminutive) for the same surname must merge.
	p3, _ := passes.UpsertPass(linkID, "d", 2, 0, 5, "t", "h", `{"entities":[{"id":"PL1","type":"Person","properties":{"name":"Пётр Лисовин"}}]}`)
	p4, _ := passes.UpsertPass(linkID, "d", 3, 0, 5, "t", "h", `{"entities":[{"id":"PL2","type":"Person","properties":{"name":"Петя Лисовин"}}]}`)

	for i, p := range []Pass{p1, p2, p3, p4} {
		if err := repo.ExtractPass(ctx, p); err != nil {
			t.Fatalf("ExtractPass %d: %v", i+1, err)
		}
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 canonical entities (Кривенков, Лисовин), got %d", n)
	}

	// Each pair of spellings must resolve to a single entity.
	check := func(a, b string) {
		ea, oka, _ := repo.lookupAlias(canonKey(a))
		eb, okb, _ := repo.lookupAlias(canonKey(b))
		if !oka || !okb {
			t.Fatalf("expected both %q and %q to resolve: %v %v", a, b, oka, okb)
		}
		if ea.ID != eb.ID {
			t.Errorf("expected %q and %q to map to one entity, got %d vs %d", a, b, ea.ID, eb.ID)
		}
	}
	check("Алексей Кривенков", "Alexey Кривенков")
	check("Пётр Лисовин", "Петя Лисовин")
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
