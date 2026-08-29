package facts

import (
	"context"
	"encoding/json"
	"testing"

	sq "github.com/bokwoon95/sq"
)

// TestExtractPassStoresRelation verifies that a pass whose result carries both
// entities and a relation yields exactly one relations row, with the source and
// target resolved to the canonical entity ids (not the raw model ids), the
// relation type preserved, the properties JSON stored verbatim, and the pass/link
// provenance attached.
func TestExtractPassStoresRelation(t *testing.T) {
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
		"relations": [
			{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED",
			 "properties":{"details":"co-founder","exact_quote":"Горин основал Acme","amount":"~","when":"2020","conf":"exact"}}
		]
	}`
	p, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h", result)
	if err := repo.ExtractPass(ctx, p); err != nil {
		t.Fatalf("ExtractPass: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM relations"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 relation, got %d", n)
	}

	src, ok, _ := repo.lookupAlias(canonKey("GORIN_EVGENIY"), "")
	if !ok {
		t.Fatal("source entity not found")
	}
	dst, ok, _ := repo.lookupAlias(canonKey("ACME_STARTUP"), "")
	if !ok {
		t.Fatal("target entity not found")
	}

	rel, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT {*} FROM relations LIMIT 1"), RelationMapper)
	if err != nil {
		t.Fatal(err)
	}
	if rel.SourceID != src.ID || rel.TargetID != dst.ID {
		t.Errorf("relation endpoints = %d->%d, want %d->%d", rel.SourceID, rel.TargetID, src.ID, dst.ID)
	}
	if rel.Type != "FOUNDED" {
		t.Errorf("relation type = %q, want FOUNDED", rel.Type)
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
		t.Fatalf("unmarshal relation properties: %v", err)
	}
	if props.ExactQuote != "Горин основал Acme" {
		t.Errorf("relation exact_quote = %q, want Горин основал Acme", props.ExactQuote)
	}
}

// TestExtractPassResolvesRelationToPreExistingEntity checks that a relation may
// reference an entity that already exists from a previous pass (not re-emitted in
// the current chunk). The alias registered when the entity was first extracted
// lets lookupAlias resolve the model id to the existing canonical entity, so the
// relation links to that id rather than creating a new one.
func TestExtractPassResolvesRelationToPreExistingEntity(t *testing.T) {
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
	existing, ok, _ := repo.lookupAlias(canonKey("GORIN_EVGENIY"), "")
	if !ok {
		t.Fatal("entity not found after pass 1")
	}

	// Pass 2: a new entity plus a relation to the pre-existing one.
	p2, _ := passes.UpsertPass(linkID, "d", 1, 0, 5, "t", "h",
		`{"entities":[{"id":"ACME_STARTUP","type":"Startup","properties":{"name":"Acme"}}],
		  "relations":[{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED","properties":{}}]}`)
	if err := repo.ExtractPass(ctx, p2); err != nil {
		t.Fatalf("ExtractPass 2: %v", err)
	}

	src, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT source_id FROM relations LIMIT 1"),
		func(row *sq.Row) int { return row.Int("source_id") })
	if err != nil {
		t.Fatal(err)
	}
	if src != existing.ID {
		t.Errorf("relation source = %d, want pre-existing entity %d", src, existing.ID)
	}
}

// TestExtractPassSkipsRelationWithUnknownEntity reproduces the "missing entity"
// policy: a relation whose source or target id was never extracted is dropped (no
// relations row) and a warning is logged, so a typo or cross-chunk reference that
// never materializes does not corrupt the graph.
func TestExtractPassSkipsRelationWithUnknownEntity(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := newTestRepo(t, db)
	ctx := context.Background()

	// The only entity present is ACME; the relation's source GORIN was never seen.
	p, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h",
		`{"entities":[{"id":"ACME_STARTUP","type":"Startup","properties":{"name":"Acme"}}],
		  "relations":[{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED","properties":{}}]}`)
	if err := repo.ExtractPass(ctx, p); err != nil {
		t.Fatalf("ExtractPass: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM relations"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 relations (unknown source skipped), got %d", n)
	}
}

// TestReconcileRedirectsRelations verifies that merging the loser entity rewires
// the relations that pointed at it onto the survivor. A third entity is used as
// the other endpoint so the redirected relation is not a self-loop. After merging
// Bob into Alice, a relation Carol->Bob must become Carol->Alice and Bob's id must
// no longer appear in the relations table.
func TestReconcileRedirectsRelations(t *testing.T) {
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
	if err := repo.insertRelation(carol.ID, bob.ID, "EMPLOYED_AT", "{}", "", 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	// Force-merge Bob into Alice (Alice is master -> survivor).
	if _, err := repo.MergeEntities(alice.ID, bob.ID); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM relations"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 relation after merge, got %d", n)
	}
	rel, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT {*} FROM relations LIMIT 1"), RelationMapper)
	if err != nil {
		t.Fatal(err)
	}
	srcID, dstID := rel.SourceID, rel.TargetID
	if srcID != carol.ID || dstID != alice.ID {
		t.Errorf("relation endpoints = %d->%d, want %d->%d (Carol -> surviving Alice)", srcID, dstID, carol.ID, alice.ID)
	}
	// The merged-away Bob id must not remain anywhere in the graph.
	if srcID == bob.ID || dstID == bob.ID {
		t.Errorf("loser id %d still present in relation %d->%d", bob.ID, srcID, dstID)
	}
}

// TestReconcileDropsRelationSelfLoop verifies that when both endpoints of a
// relation collapse onto the same survivor (e.g. Alice->Bob with Bob merged into
// Alice), the resulting self-loop is removed rather than kept as a degenerate
// edge in the knowledge graph.
func TestReconcileDropsRelationSelfLoop(t *testing.T) {
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
	if err := repo.insertRelation(alice.ID, bob.ID, "EMPLOYED_AT", "{}", "", 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.MergeEntities(alice.ID, bob.ID); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM relations"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected self-loop relation to be dropped, got %d rows", n)
	}
}

// TestExtractPassRelationPublishesEntityEvents verifies the entity-lifecycle
// requirement: when a relation is inserted, an event is published for BOTH the
// source and the target entity so downstream consumers learn their graph changed.
// The events are drained from the real goqite queue and checked against the two
// endpoint ids.
func TestExtractPassRelationPublishesEntityEvents(t *testing.T) {
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
		"relations": [
			{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED","properties":{}}
		]
	}`
	p, _ := passes.UpsertPass(linkID, "d", 0, 0, 5, "t", "h", result)
	if err := repo.ExtractPass(ctx, p); err != nil {
		t.Fatalf("ExtractPass: %v", err)
	}

	src, ok, _ := repo.lookupAlias(canonKey("GORIN_EVGENIY"), "")
	if !ok {
		t.Fatal("source entity not found")
	}
	dst, ok, _ := repo.lookupAlias(canonKey("ACME_STARTUP"), "")
	if !ok {
		t.Fatal("target entity not found")
	}

	events := drainEntityEvents(t, db)
	got := make(map[int]bool)
	for _, e := range events {
		got[e.ID] = true
	}
	if !got[src.ID] {
		t.Errorf("expected entity event for relation source entity %d; events=%+v", src.ID, events)
	}
	if !got[dst.ID] {
		t.Errorf("expected entity event for relation target entity %d; events=%+v", dst.ID, events)
	}
}
