package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"

	sq "github.com/bokwoon95/sq"
)

// Tuning constants for the reconciliation pipeline.
const (
	// promotionThreshold is the number of distinct "foundings" (new mentions)
	// at which an entity is auto-promoted to a known (trusted) entity.
	promotionThreshold = 3
	// fuzzyThreshold is the minimum similarity score for a fuzzy (FTS5) match
	// to be treated as the same entity.
	fuzzyThreshold = 0.87
	// dateType is the only entity type that is exclusive: a Date entity may
	// only merge with another Date entity, never with Person/Investor/etc.
	dateType = "Date"
)

// typesCompatible reports whether two entities may be merged given their types.
// The only exclusive type is Date: a Date entity may only merge with another
// Date entity. Any other combination (including empty types) is allowed.
func typesCompatible(a, b []string) bool {
	aDate := hasType(a, dateType)
	bDate := hasType(b, dateType)
	if !aDate && !bDate {
		return true
	}
	// At least one side is a Date. An empty side is always allowed to merge.
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	// Both sides non-empty: compatible only if neither side carries a
	// non-Date type (i.e. both are purely Date entities).
	return allTypeIs(a, dateType) && allTypeIs(b, dateType)
}

func hasType(types []string, t string) bool {
	for _, x := range types {
		if strings.EqualFold(x, t) {
			return true
		}
	}
	return false
}

func allTypeIs(types []string, t string) bool {
	for _, x := range types {
		if !strings.EqualFold(x, t) {
			return false
		}
	}
	return true
}

// ENTITIES describes the entities table: one canonical row per real-world
// entity. Aliases, mentions and the FTS index fan out from it.
type ENTITIES struct {
	sq.TableStruct
	ID             sq.NumberField `sq:"id"`
	DisplayName    sq.StringField `sq:"display_name"`
	Types          sq.StringField `sq:"types"`
	Properties     sq.StringField `sq:"properties"`
	Confidence     sq.StringField `sq:"confidence"`
	PromotionScore sq.NumberField `sq:"promotion_score"`
	IsKnown        sq.NumberField `sq:"is_known"`
	CreatedAt      sq.TimeField   `sq:"created_at"`
	UpdatedAt      sq.TimeField   `sq:"updated_at"`
}

var EN = sq.New[ENTITIES]("e")

// Entity is the Go model for a canonical row in the entities table.
type Entity struct {
	ID             int
	DisplayName    string
	Types          []string
	Properties     string // JSON array of property objects (all values kept)
	Confidence     string
	PromotionScore int
	IsKnown        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// EntityMapper scans a row from the entities table into an Entity. It is the
// single, strict mapper used whenever an entity row is loaded.
func EntityMapper(row *sq.Row) Entity {
	var e Entity
	e.ID = row.Int("id")
	e.DisplayName = row.String("display_name")
	e.Types = unmarshalTypes(row.String("types"))
	e.Properties = row.String("properties")
	e.Confidence = row.String("confidence")
	e.PromotionScore = row.Int("promotion_score")
	e.IsKnown = row.Int("is_known") != 0
	e.CreatedAt = row.Time("created_at")
	e.UpdatedAt = row.Time("updated_at")
	return e
}

// ftsAvailable records whether the SQLite driver was built with FTS5 support.
// It is set by the FTS5 probe in ensureFTS, which is run once at app start via
// InitFTS; repositories never re-run it, so recreating a repository never
// repeats the check.
var ftsAvailable bool

// EntitiesRepository provides entity/alias/mention operations and reconciliation.
type EntitiesRepository struct {
	db *sql.DB
}

// NewEntitiesRepository creates a repository. The FTS5 availability check is not
// performed here; it must be run exactly once at app start via InitFTS, so
// constructing (or recreating) repositories never re-runs the probe.
func NewEntitiesRepository(db *sql.DB) *EntitiesRepository {
	return &EntitiesRepository{db: db}
}

// InitFTS probes FTS5 support and provisions the full-text index. It must be
// called exactly once at app start (before any repository is used). Recreating
// repositories never calls it again, so no FTS check is made on reuse. If the
// driver was built without fts5 support, fuzzy matching is disabled and only
// exact (alias) matching is used.
func InitFTS(db *sql.DB) {
	ensureFTS(db)
}

// ensureFTS attempts to create the FTS5 virtual table and records whether it
// succeeded. It is the single probe run at app startup via InitFTS.
func ensureFTS(db *sql.DB) {
	_, err := sq.Exec(db, sq.SQLite.Queryf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS entity_fts USING fts5(entity_id, name, tokenize='trigram')"))
	if err != nil {
		log.Printf("fts5 unavailable, fuzzy matching disabled: %v", err)
		ftsAvailable = false
		return
	}
	ftsAvailable = true
}

// FTSAvailable reports whether fuzzy (FTS5) matching is active. Exposed so tests
// can skip fuzzy-dependent cases when built without -tags sqlite_fts5.
func FTSAvailable() bool { return ftsAvailable }

// GetEntity loads a single entity by id via the strict EntityMapper.
func (r *EntitiesRepository) GetEntity(id int) (Entity, error) {
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM entities WHERE id = {}", id), EntityMapper)
}

// lookupAlias resolves a normalized alias to its canonical entity, if present.
func (r *EntitiesRepository) lookupAlias(alias string) (Entity, bool, error) {
	id, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT entity_id FROM entity_aliases WHERE alias = {}", alias),
		func(row *sq.Row) int { return row.Int("entity_id") })
	if err != nil {
		if err == sql.ErrNoRows {
			return Entity{}, false, nil
		}
		return Entity{}, false, err
	}
	e, err := r.GetEntity(id)
	if err != nil {
		if err == sql.ErrNoRows {
			// Alias points at an entity that was merged away or deleted: drop
			// the dangling alias and treat it as not found.
			_, _ = sq.Exec(r.db, sq.SQLite.Queryf(
				"DELETE FROM entity_aliases WHERE alias = {}", alias))
			return Entity{}, false, nil
		}
		return Entity{}, false, err
	}
	return e, true, nil
}

func (r *EntitiesRepository) upsertAlias(entityID int, alias, rawName string) error {
	if alias == "" {
		return nil
	}
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT OR IGNORE INTO entity_aliases (entity_id, alias, raw_name) VALUES ({}, {}, {})",
		entityID, alias, rawName))
	return err
}

func (r *EntitiesRepository) addMention(entityID, passID, linkID, chunk int) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT OR IGNORE INTO entity_mentions (entity_id, pass_id, link_id, chunk_index) VALUES ({}, {}, {}, {})",
		entityID, passID, linkID, chunk))
	return err
}

func (r *EntitiesRepository) entityTypes(id int) ([]string, error) {
	e, err := r.GetEntity(id)
	if err != nil {
		return nil, err
	}
	return e.Types, nil
}

func (r *EntitiesRepository) entityNames(id int) ([]string, error) {
	e, err := r.GetEntity(id)
	if err != nil {
		return nil, err
	}
	names := []string{e.DisplayName}
	raws, err := sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT raw_name FROM entity_aliases WHERE entity_id = {}", id),
		func(row *sq.Row) string { return row.String("raw_name") })
	if err != nil {
		return nil, err
	}
	return append(names, raws...), nil
}

// createEntity inserts a brand-new canonical entity with a single property
// object and no aliases (aliases are added by the caller).
func (r *EntitiesRepository) createEntity(displayName string, types []string, props json.RawMessage) (Entity, error) {
	propArr := "[]"
	if len(props) > 0 {
		propArr = mustJSON([]json.RawMessage{props})
	}
	res, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO entities (display_name, types, properties) VALUES ({}, {}, {})",
		displayName, marshalTypes(types), propArr))
	if err != nil {
		return Entity{}, err
	}
	return r.GetEntity(int(res.LastInsertId))
}

// bumpPromotion records a new "founding" of the entity and auto-promotes it to
// known once it reaches the promotion threshold.
func (r *EntitiesRepository) bumpPromotion(id int) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE entities SET promotion_score = promotion_score + 1, "+
			"is_known = CASE WHEN promotion_score + 1 >= {} THEN 1 ELSE is_known END, "+
			"updated_at = CURRENT_TIMESTAMP WHERE id = {}",
		promotionThreshold, id))
	return err
}

// syncFTS refreshes the FTS5 index for an entity from its display name and all
// registered aliases. It is a no-op when FTS5 is unavailable.
func (r *EntitiesRepository) syncFTS(entityID int) error {
	if !ftsAvailable {
		return nil
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"DELETE FROM entity_fts WHERE entity_id = {}", entityID)); err != nil {
		return err
	}
	names, err := r.entityNames(entityID)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
			"INSERT INTO entity_fts (entity_id, name) VALUES ({}, {})", entityID, n)); err != nil {
			return err
		}
	}
	return nil
}

// fuzzyCandidate is a near-name match returned by the FTS5 search.
type fuzzyCandidate struct {
	Entity Entity
	Score  float64
}

// fuzzyCandidates returns existing entities whose names are trigram-similar to
// the given name, scored by similarity. Empty when FTS5 is unavailable.
func (r *EntitiesRepository) fuzzyCandidates(name string) ([]fuzzyCandidate, error) {
	if !ftsAvailable {
		return nil, nil
	}
	q := "\"" + strings.ReplaceAll(name, "\"", "") + "\""
	rows, err := sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT entity_id, name FROM entity_fts WHERE name MATCH {}", q),
		func(row *sq.Row) struct {
			EntityID int
			Name     string
		} {
			return struct {
				EntityID int
				Name     string
			}{EntityID: row.Int("entity_id"), Name: row.String("name")}
		})
	if err != nil {
		return nil, nil // unparseable query -> no candidates
	}
	seen := make(map[int]bool)
	var out []fuzzyCandidate
	for _, h := range rows {
		if seen[h.EntityID] {
			continue
		}
		seen[h.EntityID] = true
		e, err := r.GetEntity(h.EntityID)
		if err != nil {
			// The FTS index can reference an entity that was merged away or
			// deleted; skip the stale candidate instead of failing the pass.
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		out = append(out, fuzzyCandidate{Entity: e, Score: similarity(name, h.Name)})
	}
	return out, nil
}

// resolveEntity decides whether a name refers to an existing entity. It first
// tries exact alias matches (canonical key of the name and of the model id),
// then falls back to fuzzy FTS5 matching above the threshold with a
// type-compatibility gate. Returns (entity, found).
func (r *EntitiesRepository) resolveEntity(name, modelID string, types []string) (Entity, bool, error) {
	if e, ok, err := r.lookupAlias(canonKey(name)); err != nil {
		return Entity{}, false, err
	} else if ok {
		return e, true, nil
	}
	if modelID != "" {
		if e, ok, err := r.lookupAlias(canonKey(modelID)); err != nil {
			return Entity{}, false, err
		} else if ok {
			return e, true, nil
		}
	}
	if ftsAvailable {
		cands, err := r.fuzzyCandidates(name)
		if err != nil {
			return Entity{}, false, err
		}
		for _, c := range cands {
			if c.Score >= fuzzyThreshold && typesCompatible(types, c.Entity.Types) {
				return c.Entity, true, nil
			}
		}
	}
	return Entity{}, false, nil
}

// ExtractPass parses a single pass result, merges every entity it contains into
// the canonical graph, and marks the pass extracted (idempotent). Parse failures
// are treated as "nothing to extract" so the pass is not retried endlessly.
func (r *EntitiesRepository) ExtractPass(ctx context.Context, p Pass) error {
	defer r.markExtracted(p.ID)

	var res llmAnalysis
	if err := json.Unmarshal([]byte(p.Result), &res); err != nil {
		return nil
	}
	for _, e := range res.Entities {
		name := e.Properties.Name
		if name == "" {
			name = e.ID
		}
		types := []string{}
		if e.Type != "" {
			types = []string{e.Type}
		}
		props, _ := json.Marshal(e.Properties)
		if err := r.resolveAndMerge(name, e.ID, props, types, p.ID, p.LinkQueueID, p.ChunkIndex); err != nil {
			return err
		}
	}
	return nil
}

func (r *EntitiesRepository) markExtracted(passID int) {
	_, _ = sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE passes SET extracted_at = CURRENT_TIMESTAMP WHERE id = {}", passID))
}

// resolveAndMerge is the single entry point for folding one mention into the
// graph. It always inserts a fresh entity for the mention (the "new" side),
// records its mention and aliases, then runs the standard reconciliation against
// any existing entity that matches (alias collision or fuzzy, type-compatible
// name). If a match is found the two are folded together via reconcile; the new
// entity may become the survivor or the loser. If no match exists the new entity
// stands on its own. This makes the per-mention path use the exact same merge
// operation as GlobalReconcile.
func (r *EntitiesRepository) resolveAndMerge(name, modelID string, props json.RawMessage, types []string, passID, linkID, chunk int) error {
	// Always create the entity for this mention first.
	e, err := r.createEntity(name, types, props)
	if err != nil {
		return err
	}
	if err := r.addMention(e.ID, passID, linkID, chunk); err != nil {
		return err
	}
	if err := r.bumpPromotion(e.ID); err != nil {
		return err
	}
	// Re-read so the in-memory promotion score / known flag reflect the bump
	// above; reconcile folds the loser's promotion into the survivor, so the
	// value must be current.
	if e, err = r.GetEntity(e.ID); err != nil {
		return err
	}
	// Find an existing entity to merge with. The new entity has no aliases or
	// FTS entry yet, so resolveEntity can only match pre-existing entities
	// (never the row we just inserted).
	match, found, err := r.resolveEntity(name, modelID, types)
	if err != nil {
		return err
	}
	if found {
		// Register the mention's names as aliases on the new entity so that
		// reconcile carries them over to the survivor.
		if err := r.upsertAlias(e.ID, canonKey(name), name); err != nil {
			return err
		}
		if modelID != "" {
			if err := r.upsertAlias(e.ID, canonKey(modelID), modelID); err != nil {
				return err
			}
		}
		e, err = r.reconcile(e, match)
		if err != nil {
			return err
		}
		return nil
	}
	if err := r.upsertAlias(e.ID, canonKey(name), name); err != nil {
		return err
	}
	if modelID != "" {
		if err := r.upsertAlias(e.ID, canonKey(modelID), modelID); err != nil {
			return err
		}
	}
	return r.syncFTS(e.ID)
}

// findMergePair scans the whole entities table for the first mergeable pair and
// returns it, or nil when the graph is stable. Two entities merge when their
// alias sets collide (exact) or when their display names are fuzzy-similar
// above the threshold with compatible types. Pairwise comparison is used so the
// global pass catches near-duplicates that the inline FTS5 phrase search may
// miss (e.g. a one-token difference at the end of the name).
func (r *EntitiesRepository) findMergePair() (*Entity, *Entity, error) {
	ents, err := r.allEntities()
	if err != nil {
		return nil, nil, err
	}
	aliases := make(map[int][]string, len(ents))
	for i := range ents {
		al, err := r.aliasesFor(ents[i].ID)
		if err != nil {
			return nil, nil, err
		}
		aliases[ents[i].ID] = al
	}
	for i := 0; i < len(ents); i++ {
		for j := i + 1; j < len(ents); j++ {
			if aliasSetsIntersect(aliases[ents[i].ID], aliases[ents[j].ID]) {
				return &ents[i], &ents[j], nil
			}
			if similarity(ents[i].DisplayName, ents[j].DisplayName) >= fuzzyThreshold &&
				typesCompatible(ents[i].Types, ents[j].Types) {
				return &ents[i], &ents[j], nil
			}
		}
	}
	return nil, nil, nil
}

// GlobalReconcile repeatedly merges duplicate entities until the graph is
// stable, returning the number of merges performed.
func (r *EntitiesRepository) GlobalReconcile(ctx context.Context) (int, error) {
	total := 0
	for {
		a, b, err := r.findMergePair()
		if err != nil {
			return total, err
		}
		if a == nil || b == nil {
			break
		}
		if _, err := r.reconcile(*a, *b); err != nil {
			return total, err
		}
		total++
	}
	return total, nil
}

// reconcile folds two entities into one. The survivor is chosen by pickSurvivor
// (known > higher promotion > earlier created); their types, properties and
// display name are unioned; the loser's mentions and aliases are redirected to
// the survivor; a permanent redirect is recorded; and the loser is deleted. It
// is the single merge operation used both when ingesting a new mention and
// during GlobalReconcile. It returns the surviving entity.
func (r *EntitiesRepository) reconcile(a, b Entity) (Entity, error) {
	survivor, loser := pickSurvivor(a, b)

	newTypes := unionStrings(survivor.Types, loser.Types)
	newProps := unionProperties(survivor.Properties, json.RawMessage(loser.Properties))
	display := survivor.DisplayName
	if len(loser.DisplayName) > len(display) {
		display = loser.DisplayName
	}

	// Redirect loser's mentions to the survivor.
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE OR IGNORE entity_mentions SET entity_id = {} WHERE entity_id = {}",
		survivor.ID, loser.ID)); err != nil {
		return Entity{}, err
	}
	loserAliases, err := r.aliasesFor(loser.ID)
	if err != nil {
		return Entity{}, err
	}
	for _, al := range loserAliases {
		raw, rerr := r.rawNameForAlias(al)
		if rerr != nil {
			return Entity{}, rerr
		}
		if err := r.upsertAlias(survivor.ID, al, raw); err != nil {
			return Entity{}, err
		}
	}

	loserKnown := 0
	if loser.IsKnown {
		loserKnown = 1
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE entities SET display_name = {}, types = {}, properties = {}, "+
			"promotion_score = promotion_score + {}, "+
			"is_known = CASE WHEN is_known = 1 OR {} = 1 OR promotion_score + {} >= {} THEN 1 ELSE 0 END, "+
			"updated_at = CURRENT_TIMESTAMP WHERE id = {}",
		display, marshalTypes(newTypes), newProps, loser.PromotionScore, loserKnown, loser.PromotionScore, promotionThreshold, survivor.ID)); err != nil {
		return Entity{}, err
	}

	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"DELETE FROM entity_aliases WHERE entity_id = {}", loser.ID)); err != nil {
		return Entity{}, err
	}
	if ftsAvailable {
		if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
			"DELETE FROM entity_fts WHERE entity_id = {}", loser.ID)); err != nil {
			return Entity{}, err
		}
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT OR IGNORE INTO entity_redirects (old_id, new_id) VALUES ({}, {})", loser.ID, survivor.ID)); err != nil {
		return Entity{}, err
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"DELETE FROM entities WHERE id = {}", loser.ID)); err != nil {
		return Entity{}, err
	}
	return survivor, r.syncFTS(survivor.ID)
}

func pickSurvivor(a, b Entity) (survivor, loser Entity) {
	if a.IsKnown != b.IsKnown {
		if a.IsKnown {
			return a, b
		}
		return b, a
	}
	if a.PromotionScore != b.PromotionScore {
		if a.PromotionScore > b.PromotionScore {
			return a, b
		}
		return b, a
	}
	if a.CreatedAt.Before(b.CreatedAt) {
		return a, b
	}
	return b, a
}

func (r *EntitiesRepository) allEntities() ([]Entity, error) {
	return sq.FetchAll(r.db, sq.SQLite.Queryf("SELECT {*} FROM entities"), EntityMapper)
}

func (r *EntitiesRepository) aliasesFor(id int) ([]string, error) {
	return sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT alias FROM entity_aliases WHERE entity_id = {}", id),
		func(row *sq.Row) string { return row.String("alias") })
}

func (r *EntitiesRepository) rawNameForAlias(alias string) (string, error) {
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT raw_name FROM entity_aliases WHERE alias = {}", alias),
		func(row *sq.Row) string { return row.String("raw_name") })
}

// llmAnalysis is the subset of the model result we consume for entities.
type llmAnalysis struct {
	Entities []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Properties struct {
			Name string `json:"name"`
		} `json:"properties"`
	} `json:"entities"`
}


