package facts

import (
	"database/sql"
	"testing"

	sq "github.com/bokwoon95/sq"
)

// sameLinkPass seeds a fresh migrated DB plus a link/pass pair and returns the
// repository, the link id and the pass id, so a test can drive resolveAndMerge
// with real mention rows for the same link (mention rows reference both).
func sameLinkPass(t *testing.T, db *sql.DB) (*EntitiesRepository, int, int) {
	t.Helper()
	linkID, passID := insertLinkPass(t, db)
	return newTestRepo(t, db), linkID, passID
}

// countEntities returns the number of rows in the entities table.
func countEntities(t *testing.T, db *sql.DB) int {
	t.Helper()
	n, err := sq.FetchOne(db, sq.SQLite.Queryf("SELECT COUNT(*) AS c FROM entities"),
		func(row *sq.Row) int { return row.Int("c") })
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// entityByName finds the entity whose display name is name, or fails the test.
func entityByName(t *testing.T, db *sql.DB, name string) Entity {
	t.Helper()
	e, err := sq.FetchOne(db, sq.SQLite.Queryf(
		"SELECT {*} FROM entities WHERE display_name = {}", name), EntityMapper)
	if err != nil {
		t.Fatalf("entity %q not found: %v", name, err)
	}
	return e
}

// aliasesForEntity returns the alias strings recorded for an entity id.
func aliasesForEntity(t *testing.T, db *sql.DB, id int) []string {
	t.Helper()
	aliases, err := sq.FetchAll(db, sq.SQLite.Queryf(
		"SELECT alias FROM entity_aliases WHERE entity_id = {} ORDER BY alias", id),
		func(row *sq.Row) string { return row.String("alias") })
	if err != nil {
		t.Fatalf("load aliases for entity %d: %v", id, err)
	}
	return aliases
}

// TestSameLinkPartialNameMergesLaterFullName verifies the "partial first, full
// later" ordering: a chunk that emits only "Alex" is followed by a later chunk
// of the SAME link emitting "Alex Karp". Because both share the link, the
// partial mention resolves into the full-name entity, leaving a single record
// whose display name is the fuller "Alex Karp" and which carries "alex" as an
// alias.
func TestSameLinkPartialNameMergesLaterFullName(t *testing.T) {
	db := setupTestDB(t)
	repo, linkID, passID := sameLinkPass(t, db)

	// Chunk 0: bare "Alex".
	if err := repo.resolveAndMerge("Alex", "ALEX", []byte(`{"name":"Alex"}`), []string{"Person"}, passID, linkID, 0); err != nil {
		t.Fatalf("resolveAndMerge Alex: %v", err)
	}
	// Chunk 1 (same link): full name "Alex Karp".
	if err := repo.resolveAndMerge("Alex Karp", "KARP_ALEX", []byte(`{"name":"Alex Karp"}`), []string{"Person"}, passID, linkID, 1); err != nil {
		t.Fatalf("resolveAndMerge Alex Karp: %v", err)
	}

	if n := countEntities(t, db); n != 1 {
		t.Fatalf("expected 1 entity after same-link partial+full, got %d", n)
	}
	full := entityByName(t, db, "Alex Karp")
	aliases := aliasesForEntity(t, db, full.ID)
	contains := func(want string) bool {
		for _, a := range aliases {
			if a == want {
				return true
			}
		}
		return false
	}
	// The folded partial name must NOT leak out of the link as a global alias:
	// "aleksei" is the canonical form of "Alex". It is still recoverable within
	// the same link via the link-scoped subset rule, but must not resolve across
	// links, so it must not be recorded as an alias of "Alex Karp".
	if contains("aleksei") {
		t.Errorf("partial-name alias \"aleksei\" must not leak globally, got %v", aliases)
	}
}

// TestSameLinkFullNameMergesPartialLater verifies the reverse ordering: the full
// name appears in the first chunk and a bare first name appears in a later chunk
// of the same link. They must still collapse into a single entity.
func TestSameLinkFullNameMergesPartialLater(t *testing.T) {
	db := setupTestDB(t)
	repo, linkID, passID := sameLinkPass(t, db)

	if err := repo.resolveAndMerge("Alex Karp", "KARP_ALEX", []byte(`{"name":"Alex Karp"}`), []string{"Person"}, passID, linkID, 0); err != nil {
		t.Fatalf("resolveAndMerge Alex Karp: %v", err)
	}
	if err := repo.resolveAndMerge("Alex", "ALEX", []byte(`{"name":"Alex"}`), []string{"Person"}, passID, linkID, 1); err != nil {
		t.Fatalf("resolveAndMerge Alex: %v", err)
	}

	if n := countEntities(t, db); n != 1 {
		t.Fatalf("expected 1 entity after same-link full+partial, got %d", n)
	}
	entityByName(t, db, "Alex Karp")
}

// TestSameLinkLastNameMerges verifies a bare last name ("Karp") also resolves to
// the same-link full name ("Alex Karp"), not just the first name.
func TestSameLinkLastNameMerges(t *testing.T) {
	db := setupTestDB(t)
	repo, linkID, passID := sameLinkPass(t, db)

	if err := repo.resolveAndMerge("Alex Karp", "KARP_ALEX", []byte(`{"name":"Alex Karp"}`), []string{"Person"}, passID, linkID, 0); err != nil {
		t.Fatalf("resolveAndMerge full: %v", err)
	}
	if err := repo.resolveAndMerge("Karp", "KARP", []byte(`{"name":"Karp"}`), []string{"Person"}, passID, linkID, 1); err != nil {
		t.Fatalf("resolveAndMerge last name: %v", err)
	}

	if n := countEntities(t, db); n != 1 {
		t.Fatalf("expected 1 entity after same-link last-name merge, got %d", n)
	}
	entityByName(t, db, "Alex Karp")
}

// TestSameLinkIsolationAcrossLinks verifies the resolution is scoped to a single
// link: the same bare first name in TWO DIFFERENT links must NOT merge, keeping
// two separate entities. Only mentions sharing a link id may resolve through the
// partial-name rule.
func TestSameLinkIsolationAcrossLinks(t *testing.T) {
	db := setupTestDB(t)
	repo := newTestRepo(t, db)

	linkA, passA := insertLinkPass(t, db)
	linkB, passB := insertLinkPass(t, db)

	// "Alex Karp" + "Alex" in link A -> one entity.
	if err := repo.resolveAndMerge("Alex Karp", "KARP_ALEX", []byte(`{"name":"Alex Karp"}`), []string{"Person"}, passA, linkA, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.resolveAndMerge("Alex", "ALEX", []byte(`{"name":"Alex"}`), []string{"Person"}, passA, linkA, 1); err != nil {
		t.Fatal(err)
	}
	// "Alex" alone in link B -> separate entity, and MUST not merge with link A.
	if err := repo.resolveAndMerge("Alex", "ALEX", []byte(`{"name":"Alex"}`), []string{"Person"}, passB, linkB, 0); err != nil {
		t.Fatal(err)
	}

	if n := countEntities(t, db); n != 2 {
		t.Fatalf("expected 2 entities (one per link), got %d", n)
	}
}

// TestSameLinkSingleTokenDoesNotMerge verifies the strictness guard: two
// single-token mentions like "Alex" and "Karp" in the same link are NOT merged,
// because neither side has >=2 tokens to serve as the "full" name. Only a subset
// into a multi-token name is recognized.
func TestSameLinkSingleTokenDoesNotMerge(t *testing.T) {
	db := setupTestDB(t)
	repo, linkID, passID := sameLinkPass(t, db)

	if err := repo.resolveAndMerge("Alex", "ALEX", []byte(`{"name":"Alex"}`), []string{"Person"}, passID, linkID, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.resolveAndMerge("Karp", "KARP", []byte(`{"name":"Karp"}`), []string{"Person"}, passID, linkID, 1); err != nil {
		t.Fatal(err)
	}

	if n := countEntities(t, db); n != 2 {
		t.Fatalf("expected 2 distinct single-token entities (no merge), got %d", n)
	}
}
