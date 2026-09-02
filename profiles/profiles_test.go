package profiles

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	sq "github.com/bokwoon95/sq"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"
	facts "meo.ru/rolodex/facts"
)

// setupTestDB spins up a fresh, migrated SQLite database in a temp dir and
// returns a handle to it, mirroring the facts package's test setup.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")

	m, err := migrate.New("file://../migrations", "sqlite3://"+dbPath)
	if err != nil {
		t.Fatalf("migrate new: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	facts.InitFTS(db)

	return db
}

// insertLink inserts a minimal link_queue row and returns its id.
func insertLink(t *testing.T, db *sql.DB) int {
	t.Helper()

	res, err := sq.Exec(db, sq.SQLite.Queryf(
		"INSERT INTO link_queue (url) VALUES ({})", "https://example.com/"+t.Name()))
	if err != nil {
		t.Fatalf("insert link: %v", err)
	}
	return int(res.LastInsertId)
}

// insertPass inserts a passes row for the given link/chunk text and returns it
// (its ID and LinkQueueID satisfy the claims foreign keys).
func insertPass(t *testing.T, db *sql.DB, linkID, chunkIndex int, chunkText string) facts.Pass {
	t.Helper()

	p, err := facts.NewPassesRepository(db).UpsertPass(linkID, "d", chunkIndex, 0, len([]byte(chunkText)), chunkText, "h", "{}")
	if err != nil {
		t.Fatalf("upsert pass: %v", err)
	}
	return p
}

// insertClaim inserts a claims row backed by the given pass and link.
func insertClaim(t *testing.T, db *sql.DB, src, dst int, typ, props, conf string, passID, linkID int) {
	t.Helper()

	_, err := sq.Exec(db, sq.SQLite.Queryf(
		"INSERT INTO claims (source_id, target_id, type, properties, confidence, pass_id, link_id, chunk_index) "+
			"VALUES ({}, {}, {}, {}, {}, {}, {}, 0)",
		src, dst, typ, props, conf, passID, linkID))
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}
}

// TestRebuildProfileRendersClaims verifies that BuildProfile produces a
// document containing the entity header, a parsed exact quote, and the source
// page URL for each claim the entity participates in. It seeds a FOUNDED
// claim and checks the rendered text reflects the outgoing direction.
func TestRebuildProfileRendersClaims(t *testing.T) {
	db := setupTestDB(t)
	repo := facts.NewEntitiesRepository(db, facts.NoopEntityPublisher{})
	profiles := NewProfilesRepository(db)
	linkID := insertLink(t, db)

	// One pass whose chunk text is "t", backing the claim below.
	p := insertPass(t, db, linkID, 0, "t")

	gorin, err := repo.CreateEntity("Евгений Горин", []string{"Person"}, []byte(`{"name":"Евгений Горин"}`))
	if err != nil {
		t.Fatal(err)
	}
	acme, err := repo.CreateEntity("Acme", []string{"Startup"}, []byte(`{"name":"Acme"}`))
	if err != nil {
		t.Fatal(err)
	}

	insertClaim(t, db, gorin.ID, acme.ID, "FOUNDED",
		`{"details":"co-founder of Acme","exact_quote":"Горин основал Acme","amount":"~","when":"2020","conf":"exact"}`,
		"exact", p.ID, linkID)

	text, err := profiles.BuildProfile(gorin.ID)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if text == "" {
		t.Fatal("BuildProfile returned empty document")
	}

	// The header names the entity, with the name wrapped in a red HTML span so
	// it renders colored in the dashboard.
	if !strings.Contains(text, "# <span style='color:#d33'>Евгений Горин</span>") {
		t.Errorf("profile missing red entity header: %q", text)
	}
	if !strings.Contains(text, "Person") {
		t.Errorf("profile missing type section")
	}
	// The claim appears in prose: entity FOUNDED the other. The Person name
	// is red, the Startup (non-Person) name is green, and the claim type is
	// rendered as a neutral Russian noun ("Основание" for FOUNDED).
	if !strings.Contains(text, "<span style='color:#d33'>Евгений Горин</span> Основание <span style='color:#2e7d32'>Acme</span>") {
		t.Errorf("profile missing colored outgoing claim sentence")
	}
	// The parsed exact quote is embedded.
	if !strings.Contains(text, "“Горин основал Acme”") {
		t.Errorf("profile missing exact quote")
	}
	// The metadata (when/confidence/source) is attributed inside one set of
	// parentheses, and the source page URL is part of that same grouped line.
	if !strings.Contains(text, "(when: 2020") {
		t.Errorf("profile metadata not wrapped in parentheses: %q", text)
	}
	grouped := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "(") &&
			strings.HasSuffix(strings.TrimSpace(line), ")") &&
			strings.Contains(line, "https://example.com/") {
			grouped = true
		}
	}
	if !grouped {
		t.Errorf("profile source URL not grouped with metadata in parentheses: %q", text)
	}
	// The "source" word carries a footnote reference, and the corresponding pass
	// chunk (here the chunk text is "t") is rendered as a numbered footnote at
	// the end of the profile.
	if !strings.Contains(text, `<sup><a href="#fn-1" id="fnref-1">[1]</a></sup>: https://example.com/`) {
		t.Errorf("profile source word missing footnote reference: %q", text)
	}
	if !strings.Contains(text, `<li id="fn-1">t <a href="#fnref-1" title="back">↩</a></li>`) {
		t.Errorf("profile missing footnote with chunk text: %q", text)
	}
}

// TestRebuildProfilePersistsAndReloads verifies the store path: rebuilding a
// profile writes an entity_profiles row that GetProfile can read back, and that
// the returned text is an exact match so pre-computation round-trips.
func TestRebuildProfilePersistsAndReloads(t *testing.T) {
	db := setupTestDB(t)
	repo := facts.NewEntitiesRepository(db, facts.NoopEntityPublisher{})
	profiles := NewProfilesRepository(db)

	alice, err := repo.CreateEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
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

// TestRebuildProfileEmptyClaimsNoEntities verifies a fresh entity with no
// claims renders a document carrying the "no claims" placeholder rather
// than failing or producing an empty body.
func TestRebuildProfileEmptyClaimsNoEntities(t *testing.T) {
	db := setupTestDB(t)
	repo := facts.NewEntitiesRepository(db, facts.NoopEntityPublisher{})
	profiles := NewProfilesRepository(db)

	alice, err := repo.CreateEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	if err != nil {
		t.Fatal(err)
	}

	text, err := profiles.BuildProfile(alice.ID)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !strings.Contains(text, "Связей не зарегистрировано") {
		t.Errorf("profile should note the absence of claims, got %q", text)
	}
}

// TestRebuildAllProfilesVerifyFullRebuild runs RebuildAll over a small graph and
// asserts it writes a profile row for every entity, returning the count.
func TestRebuildAllProfilesVerifyFullRebuild(t *testing.T) {
	db := setupTestDB(t)
	repo := facts.NewEntitiesRepository(db, facts.NoopEntityPublisher{})
	profiles := NewProfilesRepository(db)

	for _, name := range []string{"Alice", "Bob", "Carol"} {
		if _, err := repo.CreateEntity(name, []string{"Person"}, []byte(`{"name":"`+name+`"}`)); err != nil {
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

// TestRebuildProfileInverseClaim confirms an incoming claim (another
// entity pointing at the profiled entity) is rendered in the prose sentence
// with the correct direction ("Carol EMPLOYED_AT Alice").
func TestRebuildProfileInverseClaim(t *testing.T) {
	db := setupTestDB(t)
	repo := facts.NewEntitiesRepository(db, facts.NoopEntityPublisher{})
	profiles := NewProfilesRepository(db)

	alice, err := repo.CreateEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	carol, err := repo.CreateEntity("Carol", []string{"Person"}, []byte(`{"name":"Carol"}`))
	if err != nil {
		t.Fatal(err)
	}

	linkID := insertLink(t, db)
	p := insertPass(t, db, linkID, 0, "")
	// Carol -> Alice: for Alice this is an incoming claim.
	insertClaim(t, db, carol.ID, alice.ID, "EMPLOYED_AT", "{}", "", p.ID, linkID)

	text, err := profiles.BuildProfile(alice.ID)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	// Carol (Person) -> Alice (Person), an incoming claim for Alice. Both
	// names render red, and EMPLOYED_AT renders with its neutral Russian noun.
	if !strings.Contains(text, "<span style='color:#d33'>Carol</span> Сотрудничество/вовлечение <span style='color:#d33'>Alice</span>") {
		t.Errorf("profile missing incoming claim sentence, got %q", text)
	}
	if !strings.Contains(text, "## Также важно") {
		t.Errorf("profile missing incoming claim section header")
	}
}

// TestClaimTypeNoun verifies the raw claim type is rendered as a neutral
// Russian noun in profiles, and that unknown types fall back to the raw token.
func TestClaimTypeNoun(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{"INVESTED_IN", "Инвестиции"},
		{"FOUNDED", "Основание"},
		{"FOUNDED/COFOUNDED", "Основание"},
		{"EMPLOYED_AT", "Сотрудничество/вовлечение"},
		{"SEEDED", "Посевные инвестиции"},
		{"ACQUIRED", "Приобретение"},
		{"SOLD", "Продажа"},
		{"LAUNCHED", "Запуск"},
		{"VALUED", "Оценка"},
		{"ESTABLISHED_IN", "Создание"},
		{"MYSTERY_TYPE", "MYSTERY_TYPE"},
	}
	for _, c := range cases {
		if got := claimTypeNoun(c.typ); got != c.want {
			t.Errorf("claimTypeNoun(%q) = %q, want %q", c.typ, got, c.want)
		}
	}
}

// TestBuildProfileDedupesChunkFootnotes verifies that footnotes are emitted per
// unique pass chunk: two claims backed by the same chunk text share a single
// footnote reference, while a claim from a different chunk gets its own
// footnote. No duplicate footnote is rendered for the repeated chunk.
func TestBuildProfileDedupesChunkFootnotes(t *testing.T) {
	db := setupTestDB(t)
	repo := facts.NewEntitiesRepository(db, facts.NoopEntityPublisher{})
	profiles := NewProfilesRepository(db)

	linkID := insertLink(t, db)
	chunkShared := "Acme was founded by Alice in 2020."
	chunkOther := "Alice later became CEO of Acme."

	// Three distinct passes; two share the same chunk text.
	p1 := insertPass(t, db, linkID, 0, chunkShared)
	p2 := insertPass(t, db, linkID, 1, chunkShared)
	p3 := insertPass(t, db, linkID, 2, chunkOther)

	alice, _ := repo.CreateEntity("Alice", []string{"Person"}, []byte(`{"name":"Alice"}`))
	acme, _ := repo.CreateEntity("Acme", []string{"Startup"}, []byte(`{"name":"Acme"}`))

	props := `{"details":"x"}`
	insertClaim(t, db, alice.ID, acme.ID, "FOUNDED", props, "exact", p1.ID, linkID)
	insertClaim(t, db, alice.ID, acme.ID, "FOUNDED", props, "exact", p2.ID, linkID)
	insertClaim(t, db, alice.ID, acme.ID, "FOUNDED", props, "exact", p3.ID, linkID)

	text, err := profiles.BuildProfile(alice.ID)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}

	// The shared chunk is deduped: exactly two claims point at footnote [1]
	// (one per reference), so `[1]` appears twice and no second footnote for it.
	if got := strings.Count(text, `[1]`); got != 2 {
		t.Errorf("expected footnote [1] referenced by the two shared-chunk claims, got %d refs", got)
	}
	// Claim 3 differs only by chunk, so it references a distinct footnote [2].
	if got := strings.Count(text, `[2]`); got != 1 {
		t.Errorf("expected the other chunk to get a single footnote [2], got %d refs", got)
	}
	// The shared chunk's text appears exactly once (its footnote body), proving
	// no duplicate footnote was rendered; the other chunk appears once too.
	if got := strings.Count(text, chunkShared); got != 1 {
		t.Errorf("expected the shared chunk text once in the footnote list, got %d", got)
	}
	if got := strings.Count(text, chunkOther); got != 1 {
		t.Errorf("expected the other chunk text once in the footnote list, got %d", got)
	}
}
