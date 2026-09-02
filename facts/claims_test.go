package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	sq "github.com/bokwoon95/sq"

	utils "meo.ru/rolodex/facts/utils"
)

// insertRawClaim inserts a claims row directly, bypassing insertClaim's
// idempotency guard, so a test can plant exact duplicate rows to exercise
// DedupeClaims. It returns the new row's id.
func insertRawClaim(t *testing.T, db *sql.DB, src, dst int, typ, props, conf string) int {
	t.Helper()
	res, err := sq.Exec(db, sq.SQLite.Queryf(
		"INSERT INTO claims (source_id, target_id, type, properties, confidence) VALUES ({}, {}, {}, {}, {})",
		src, dst, typ, props, conf))
	if err != nil {
		t.Fatalf("insert raw claim: %v", err)
	}
	return int(res.LastInsertId)
}

// TestExtractPassStoresClaim verifies that a pass whose result carries both
// entities and a claim yields exactly one claims row, with the source and
// target resolved to the canonical entity ids (not the raw model ids), the
// claim type preserved, the properties JSON stored verbatim, and the pass/link
// provenance attached.
func TestExtractPassStoresClaim(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	result := `{
		"entities": [
			{"id":"GORIN_EVGENIY","type":"Person","properties":{"name":"Евгений Горин"}},
			{"id":"ACME_STARTUP","type":"Startup","properties":{"name":"Acme"}}

		],
		"claims": [
			{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED",
			 "properties":{"details":"co-founder","exact_quote":"Горин основал Acme","amount":"~","when":"2020","conf":"exact"}}
		]
	}`
	p, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h", result)
	if err := repo.ExtractPass(ctx, p); err != nil {
		t.Fatalf("ExtractPass: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM claims"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 claim, got %d", n)
	}

	src, ok, _ := repo.lookupAlias(utils.CanonKey("GORIN_EVGENIY"), "")
	if !ok {
		t.Fatal("source entity not found")
	}
	dst, ok, _ := repo.lookupAlias(utils.CanonKey("ACME_STARTUP"), "")
	if !ok {
		t.Fatal("target entity not found")
	}

	rel, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT {*} FROM claims LIMIT 1"), ClaimMapper)
	if err != nil {
		t.Fatal(err)
	}
	if rel.SourceID != src.ID || rel.TargetID != dst.ID {
		t.Errorf("claim endpoints = %d->%d, want %d->%d", rel.SourceID, rel.TargetID, src.ID, dst.ID)
	}
	if rel.Type != "FOUNDED" {
		t.Errorf("claim type = %q, want FOUNDED", rel.Type)
	}
	if rel.PassID != p.ID || rel.LinkID != linkID || rel.ChunkIndex != 0 {
		t.Errorf("provenance = (pass=%d,link=%d,chunk=%d), want (pass=%d,link=%d,chunk=%d)",
			rel.PassID, rel.LinkID, rel.ChunkIndex, p.ID, linkID, 0)
	}
	var props struct {
		Details    string `json:"details"`
		ExactQuote string `json:"exact_quote"`
		When       string `json:"when"`
	}
	if err := json.Unmarshal([]byte(rel.Properties), &props); err != nil {
		t.Fatalf("unmarshal claim properties: %v", err)
	}
	if props.ExactQuote != "Горин основал Acme" {
		t.Errorf("claim exact_quote = %q, want Горин основал Acme", props.ExactQuote)
	}
}

// TestExtractPassResolvesClaimToPreExistingEntity checks that a claim may
// reference an entity that already exists from a previous pass (not re-emitted in
// the current chunk). The alias registered when the entity was first extracted
// lets lookupAlias resolve the model id to the existing canonical entity, so the
// claim links to that id rather than creating a new one.
func TestExtractPassResolvesClaimToPreExistingEntity(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	// Pass 1: defines the entity only.
	p1, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h",
		`{"entities":[{"id":"GORIN_EVGENIY","type":"Person","properties":{"name":"Евгений Горин"}}]}`)
	if err := repo.ExtractPass(ctx, p1); err != nil {
		t.Fatalf("ExtractPass 1: %v", err)
	}
	existing, ok, _ := repo.lookupAlias(utils.CanonKey("GORIN_EVGENIY"), "")
	if !ok {
		t.Fatal("entity not found after pass 1")
	}

	// Pass 2: a new entity plus a claim to the pre-existing one.
	p2, _ := passes.UpsertPass(linkID, "d", 1, 0, 5, "t", "h",
		`{"entities":[{"id":"ACME_STARTUP","type":"Startup","properties":{"name":"Acme"}}],
		  "claims":[{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED","properties":{}}]}`)
	if err := repo.ExtractPass(ctx, p2); err != nil {
		t.Fatalf("ExtractPass 2: %v", err)
	}

	src, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT source_id FROM claims LIMIT 1"),
		func(row *sq.Row) int { return row.Int("source_id") })
	if err != nil {
		t.Fatal(err)
	}
	if src != existing.ID {
		t.Errorf("claim source = %d, want pre-existing entity %d", src, existing.ID)
	}
}

// TestExtractPassSkipsClaimWithUnknownEntity reproduces the "missing entity"
// policy: a claim whose source or target id was never extracted is dropped (no
// claims row) and a warning is logged, so a typo or cross-chunk reference that
// never materializes does not corrupt the graph.
func TestExtractPassSkipsClaimWithUnknownEntity(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	// The only entity present is ACME; the claim's source GORIN was never seen.
	p, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h",
		`{"entities":[{"id":"ACME_STARTUP","type":"Startup","properties":{"name":"Acme"}}],
		  "claims":[{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED","properties":{}}]}`)
	if err := repo.ExtractPass(ctx, p); err != nil {
		t.Fatalf("ExtractPass: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM claims"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 claims (unknown source skipped), got %d", n)
	}
}

// TestReconcileRedirectsClaims verifies that merging the loser entity rewires
// the claims that pointed at it onto the survivor. A third entity is used as
// the other endpoint so the redirected claim is not a self-loop. After merging
// Bob into Alice, a claim Carol->Bob must become Carol->Alice and Bob's id must
// no longer appear in the claims table.
func TestReconcileRedirectsClaims(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)

	alice, err := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := repo.createEntity("Bob", []string{"Person"}, []byte(`{"name":"Bob"}`))
	if err != nil {
		t.Fatal(err)
	}
	carol, err := repo.createEntity("Carol", []string{"Person"}, []byte(`{"name":"Carol"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Carol -> Bob, with Bob as the future loser.
	linkID, passID := insertLinkPass(t, db)
	if err := repo.insertClaim(carol.ID, bob.ID, "EMPLOYED_AT", "{}", "", passID, linkID, 0); err != nil {
		t.Fatal(err)
	}

	// Force-merge Bob into Alice (Alice is master -> survivor).
	if _, err := repo.MergeEntities(alice.ID, bob.ID); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM claims"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 claim after merge, got %d", n)
	}
	rel, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT {*} FROM claims LIMIT 1"), ClaimMapper)
	if err != nil {
		t.Fatal(err)
	}
	srcID, dstID := rel.SourceID, rel.TargetID
	if srcID != carol.ID || dstID != alice.ID {
		t.Errorf("claim endpoints = %d->%d, want %d->%d (Carol -> surviving Alice)", srcID, dstID, carol.ID, alice.ID)
	}
	// The merged-away Bob id must not remain anywhere in the graph.
	if srcID == bob.ID || dstID == bob.ID {
		t.Errorf("loser id %d still present in claim %d->%d", bob.ID, srcID, dstID)
	}
}

// TestReconcileDropsClaimSelfLoop verifies that when both endpoints of a
// claim collapse onto the same survivor (e.g. Alice->Bob with Bob merged into
// Alice), the resulting self-loop is removed rather than kept as a degenerate
// edge in the knowledge graph.
func TestReconcileDropsClaimSelfLoop(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)

	alice, err := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := repo.createEntity("Bob", []string{"Person"}, []byte(`{"name":"Bob"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Alice -> Bob: merging Bob into Alice makes this a self-loop.
	linkID, passID := insertLinkPass(t, db)
	if err := repo.insertClaim(alice.ID, bob.ID, "EMPLOYED_AT", "{}", "", passID, linkID, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.MergeEntities(alice.ID, bob.ID); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM claims"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected self-loop claim to be dropped, got %d rows", n)
	}
}

// TestExtractPassClaimPublishesEntityEvents verifies the entity-lifecycle
// requirement: when a claim is inserted, an event is published for BOTH the
// source and the target entity so downstream consumers learn their graph changed.
// The events are drained from the real goqite queue and checked against the two
// endpoint ids.
func TestExtractPassClaimPublishesEntityEvents(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	result := `{
		"entities": [
			{"id":"GORIN_EVGENIY","type":"Person","properties":{"name":"Евгений Горин"}},
			{"id":"ACME_STARTUP","type":"Startup","properties":{"name":"Acme"}}
		],
		"claims": [
			{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED","properties":{}}
		]
	}`
	p, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h", result)
	if err := repo.ExtractPass(ctx, p); err != nil {
		t.Fatalf("ExtractPass: %v", err)
	}

	src, ok, _ := repo.lookupAlias(utils.CanonKey("GORIN_EVGENIY"), "")
	if !ok {
		t.Fatal("source entity not found")
	}
	dst, ok, _ := repo.lookupAlias(utils.CanonKey("ACME_STARTUP"), "")
	if !ok {
		t.Fatal("target entity not found")
	}

	events := drainEntityEvents(t, db)
	got := make(map[int]bool)
	for _, e := range events {
		got[e.ID] = true
	}
	if !got[src.ID] {
		t.Errorf("expected entity event for claim source entity %d; events=%+v", src.ID, events)
	}
	if !got[dst.ID] {
		t.Errorf("expected entity event for claim target entity %d; events=%+v", dst.ID, events)
	}
}

// TestDedupeClaimsDropsShorterNearIdentical verifies that two claims with
// the same ordered endpoint pair and type, whose properties flatten to nearly
// identical text blocks longer than 100 characters (the current dedup proximity
// guard), are deduplicated: the row with the shorter text block is deleted and
// only the more detailed one survives.
func TestDedupeClaimsDropsShorterNearIdentical(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	a, _ := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	b, _ := repo.createEntity("Acme", []string{"Startup"}, []byte(`{"name":"Acme"}`))
	repo.upsertAlias(a.ID, utils.CanonKey("Alice"), "Alice")
	repo.upsertAlias(b.ID, utils.CanonKey("Acme"), "Acme")

	// Two near-identical property blocks, each over 100 chars once flattened so
	// they clear the dedup proximity guard, with "when" omitted (ignored by the
	// text-block flattening). The first is the fuller variant, the second drops
	// a trailing clause and is therefore the shorter one.
	long := `{"details":"Founded Acme in 2020 and took over as Chief Executive Officer of the entire company, overseeing all product lines, engineering teams, sales operations and global expansion","exact_quote":"Alice founded Acme"}`
	short := `{"details":"Founded Acme in 2020 and took over as Chief Executive Officer of the entire company, overseeing all product lines, engineering teams and sales operations","exact_quote":"Alice founded Acme"}`
	insertRawClaim(t, db, a.ID, b.ID, "FOUNDED", long, "exact")
	shortID := insertRawClaim(t, db, a.ID, b.ID, "FOUNDED", short, "exact")

	deleted, err := repo.DedupeClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 duplicate dropped, got %d", deleted)
	}

	// The shorter variant is gone, the longer one survives.
	if _, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT id FROM claims WHERE id = {}", shortID),
		func(row *sq.Row) int { return row.Int("id") }); err == nil {
		t.Errorf("expected the shorter duplicate claim %d to be deleted", shortID)
	}
	count, _ := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM claims"),
		func(row *sq.Row) int { return row.Int("c") })
	if count != 1 {
		t.Errorf("expected 1 claim to remain, got %d", count)
	}
	// The surviving variant must be the longer, fuller one.
	rem, _ := sq.FetchOne(db, sq.SQLite.Queryf("SELECT {*} FROM claims LIMIT 1"), ClaimMapper)
	if rem.Properties != long {
		t.Errorf("expected the longer claim to survive, got %q", rem.Properties)
	}
}

// TestDedupeClaimsKeepsDifferentTypeAndDifferentText verifies that DedupeClaims
// leaves claims alone when they differ by type or when their property text
// blocks are not close, so the dedup only fires on genuine near-identical pairs.
func TestDedupeClaimsKeepsDifferentTypeAndDifferentText(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	a, _ := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	b, _ := repo.createEntity("Acme", []string{"Startup"}, []byte(`{"name":"Acme"}`))

	// Same pair but a different claim type -> not a duplicate.
	insertRawClaim(t, db, a.ID, b.ID, "FOUNDED", `{"details":"founded Acme"}`, "")
	insertRawClaim(t, db, a.ID, b.ID, "INVESTED_IN", `{"details":"invested in Acme"}`, "")
	// Same pair and type but clearly different text -> not a duplicate.
	insertRawClaim(t, db, b.ID, a.ID, "EMPLOYED_AT", `{"details":"works at Alice as a plumber"}`, "")
	insertRawClaim(t, db, b.ID, a.ID, "EMPLOYED_AT", `{"details":"serves as chief gardener"}`, "")

	deleted, err := repo.DedupeClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("expected nothing to be deduplicated, got %d deletions", deleted)
	}
	count, _ := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM claims"),
		func(row *sq.Row) int { return row.Int("c") })
	if count != 4 {
		t.Errorf("expected all 4 claims to remain, got %d", count)
	}
}

// TestDedupeClaimsEmptyBlocks verifies that claims with an identical (empty)
// property block are treated as duplicates for the same pair and type, since
// empty blocks carry no distinguishing detail.
func TestDedupeClaimsEmptyBlocks(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	a, _ := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	b, _ := repo.createEntity("Acme", []string{"Startup"}, []byte(`{"name":"Acme"}`))
	insertRawClaim(t, db, a.ID, b.ID, "FOUNDED", `{}`, "")
	insertRawClaim(t, db, a.ID, b.ID, "FOUNDED", `{}`, "")

	deleted, err := repo.DedupeClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 empty-block duplicate dropped, got %d", deleted)
	}
	count, _ := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM claims"),
		func(row *sq.Row) int { return row.Int("c") })
	if count != 1 {
		t.Errorf("expected 1 empty-block claim to remain, got %d", count)
	}
}
