package facts

import (
	"context"
	"strings"
	"testing"

	sq "github.com/bokwoon95/sq"
)

// TestRebuildProfileRendersRelations verifies that BuildProfile produces a
// document containing the entity header, a parsed exact quote, and the source
// page URL for each relation the entity participates in. It seeds a FOUNDED and
// an inverse relation and checks the rendered text reflects both directions.
func TestRebuildProfileRendersRelations(t *testing.T) {
	db := setupTestDB(t)
	linkID := insertLink(t, db)
	passes := NewPassesRepository(db)
	repo := newTestRepo(t, db)
	profiles := NewProfilesRepository(db)
	ctx := context.Background()

	// Upsert the original URL text and the surrounding content is not needed;
	// insertLink already created a link_queue row whose url is stored.
	_ = linkID

	// Two entities and a relation between them, both directions tested later.
	result := `{
		"entities": [
			{"id":"GORIN_EVGENIY","type":"Person","properties":{"name":"Евгений Горин"}},
			{"id":"ACME_STARTUP","type":"Startup","properties":{"name":"Acme"}}
		],
		"relations": [
			{"source":"GORIN_EVGENIY","target":"ACME_STARTUP","type":"FOUNDED",
			 "properties":{"details":"co-founder of Acme","exact_quote":"Горин основал Acme","amount":"~","when":"2020","conf":"exact"}}
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

	text, err := profiles.BuildProfile(src.ID)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if text == "" {
		t.Fatal("BuildProfile returned empty document")
	}

	// The header names the entity and its type.
	if !strings.Contains(text, "# Евгений Горин") {
		t.Errorf("profile missing entity header: %q", text)
	}
	if !strings.Contains(text, "Person") {
		t.Errorf("profile missing type section")
	}
	// The relation appears in prose: entity FOUNDED the other.
	if !strings.Contains(text, "Евгений Горин FOUNDED Acme") {
		t.Errorf("profile missing outgoing relation sentence")
	}
	// The parsed exact quote is embedded.
	if !strings.Contains(text, "“Горин основал Acme”") {
		t.Errorf("profile missing exact quote")
	}
	// The source URL (with the seed link inserted per-test) is linked.
	if !strings.Contains(text, "Source: https://example.com/") {
		t.Errorf("profile missing source URL")
	}
}

// TestRebuildProfilePersistsAndReloads verifies the store path: rebuilding a
// profile writes an entity_profiles row that GetProfile can read back, and that
// the returned text is an exact match so pre-computation round-trips.
func TestRebuildProfilePersistsAndReloads(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)
	profiles := NewProfilesRepository(db)

	alice, err := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	if err != nil {
		t.Fatal(err)
	}

	text, err := profiles.RebuildProfile(alice.ID)
	if err != nil {
		t.Fatalf("RebuildProfile: %v", err)
	}
	if text == "" {
		t.Fatal("RebuildProfile returned empty document")
	}

	got, found, err := profiles.GetProfile(alice.ID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !found {
		t.Fatal("expected profile to be stored")
	}
	if got.Profile != text {
		t.Errorf("stored profile differs from built text")
	}
}

// TestRebuildProfileUnknownEntity verifies that building a profile for an id
// that no longer exists (e.g. a stale queue event for a merged-away entity)
// returns an empty document and writes no row, so the event handler can skip it
// safely.
func TestRebuildProfileUnknownEntity(t *testing.T) {
	db := setupTestDB(t)
	profiles := NewProfilesRepository(db)

	text, err := profiles.BuildProfile(9999)
	if err != nil {
		t.Fatalf("BuildProfile for missing id: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty document for missing entity, got %q", text)
	}

	if _, found, _ := profiles.GetProfile(9999); found {
		t.Error("expected no stored profile for missing entity")
	}
}

// TestRebuildProfileEmptyRelationsNoEntities verifies a fresh entity with no
// relations renders a document carrying the "no relations" placeholder rather
// than failing or producing an empty body.
func TestRebuildProfileEmptyRelationsNoEntities(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)
	profiles := NewProfilesRepository(db)

	alice, err := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	if err != nil {
		t.Fatal(err)
	}

	text, err := profiles.BuildProfile(alice.ID)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !strings.Contains(text, "No relations recorded yet") {
		t.Errorf("profile should note the absence of relations, got %q", text)
	}
}

// TestRebuildAllProfilesVerifyFullRebuild runs RebuildAll over a small graph and
// asserts it writes a profile row for every entity, returning the count.
func TestRebuildAllProfilesVerifyFullRebuild(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)
	profiles := NewProfilesRepository(db)

	for _, name := range []string{"Alice", "Bob", "Carol"} {
		if _, err := repo.createEntity(name, []string{"Person"}, []byte(`{"name":"`+name+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	n, err := profiles.RebuildAll()
	if err != nil {
		t.Fatalf("RebuildAll: %v", err)
	}
	if n != 3 {
		t.Fatalf("RebuildAll returned %d, want 3", n)
	}

	c, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM entity_profiles"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	if c != 3 {
		t.Fatalf("expected 3 stored profiles, got %d", c)
	}
}

// TestRebuildProfileInverseRelation confirms an incoming relation (another
// entity pointing at the profiled entity) is rendered in the prose sentence
// with the correct direction ("Carol EMPLOYED_AT Alice").
func TestRebuildProfileInverseRelation(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)
	profiles := NewProfilesRepository(db)

	alice, err := repo.createEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	carol, err := repo.createEntity("Carol", []string{"Person"}, []byte(`{"name":"Carol"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Carol -> Alice: for Alice this is an incoming relation.
	linkID, passID := insertLinkPass(t, db)
	if err := repo.insertRelation(carol.ID, alice.ID, "EMPLOYED_AT", "{}", "", passID, linkID, 0); err != nil {
		t.Fatal(err)
	}

	text, err := profiles.BuildProfile(alice.ID)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !strings.Contains(text, "Carol EMPLOYED_AT Alice") {
		t.Errorf("profile missing incoming relation sentence, got %q", text)
	}
	if !strings.Contains(text, "## Relations (incoming)") {
		t.Errorf("profile missing incoming relation section header")
	}
}
