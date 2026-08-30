package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
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
	// relationDedupThreshold is the minimum token similarity between two
	// relations' property text blocks for them to count as duplicates.
	relationDedupThreshold = 0.5
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

// RELATIONS describes the relations table: directed edges between two canonical
// entities extracted from a pass. One row per asserted relation; duplicates are
// kept (no uniqueness constraint) so re-extraction stays idempotent and every
// occurrence is preserved as provenance.
type RELATIONS struct {
	sq.TableStruct
	ID         sq.NumberField `sq:"id"`
	SourceID   sq.NumberField `sq:"source_id"`
	TargetID   sq.NumberField `sq:"target_id"`
	Type       sq.StringField `sq:"type"`
	Properties sq.StringField `sq:"properties"`
	Confidence sq.StringField `sq:"confidence"`
	PassID     sq.NumberField `sq:"pass_id"`
	LinkID     sq.NumberField `sq:"link_id"`
	ChunkIndex sq.NumberField `sq:"chunk_index"`
	CreatedAt  sq.TimeField   `sq:"created_at"`
}

var RN = sq.New[RELATIONS]("r")

// Relation is the Go model for a row in the relations table.
type Relation struct {
	ID         int
	SourceID   int
	TargetID   int
	Type       string
	Properties string // JSON object of relation attributes (details/quote/amount/when)
	Confidence string
	PassID     int
	LinkID     int
	ChunkIndex int
	CreatedAt  time.Time
}

// RelationMapper scans a row from the relations table into a Relation. It is the
// single strict mapper used whenever a relation row is loaded.
func RelationMapper(row *sq.Row) Relation {
	var r Relation
	r.ID = row.Int("id")
	r.SourceID = row.Int("source_id")
	r.TargetID = row.Int("target_id")
	r.Type = row.String("type")
	r.Properties = row.String("properties")
	r.Confidence = row.String("confidence")
	r.PassID = row.Int("pass_id")
	r.LinkID = row.Int("link_id")
	r.ChunkIndex = row.Int("chunk_index")
	r.CreatedAt = row.Time("created_at")
	return r
}

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
	e.Types = UnmarshalTypes(row.String("types"))
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
	// pub broadcasts entity lifecycle events. Defaults to a no-op so the
	// repository works without a configured queue; callers opt in via
	// SetPublisher (e.g. the facts machine in production).
	pub EntityEventPublisher
}

// NewEntitiesRepository creates a repository. The FTS5 availability check is not
// performed here; it must be run exactly once at app start via InitFTS, so
// constructing (or recreating) repositories never re-runs the probe.
func NewEntitiesRepository(db *sql.DB, pub EntityEventPublisher) *EntitiesRepository {
	return &EntitiesRepository{db: db, pub: pub}
}

// publishEvent emits an entity lifecycle event, logging (but never failing) if
// the publisher errors.
func (r *EntitiesRepository) publishEvent(id int, name string) {
	if err := r.pub.PublishEntityEvent(id, name); err != nil {
		log.Printf("publish entity event (id=%d): %v", id, err)
	}
}

// InitFTS probes FTS5 support and provisions the full-text index. It must be
// called exactly once at app start (before any repository is used). Recreating
// repositories never calls it again, so no FTS check is made on reuse. If the
// driver was built without fts5 support, fuzzy matching is disabled and only
// exact (alias) matching is used.
func InitFTS(db *sql.DB) {
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

// lookupAlias resolves a canonical entity from up to two candidate alias keys
// (e.g. the mention name and the model id), matching either of them in a single
// joined query (the first key is preferred). It returns (entity, found). The
// join also drops the need for a second lookup: the matching entity row is read
// directly. A match that points at a deleted entity simply yields no row and is
// treated as not found (loser aliases are already removed by reconcile).
func (r *EntitiesRepository) lookupAlias(a, b string) (Entity, bool, error) {
	e, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM entities e JOIN entity_aliases aa ON aa.entity_id = e.id "+
			"WHERE aa.alias = {} OR aa.alias = {} ORDER BY aa.alias = {} DESC", a, b, a),
		EntityMapper)
	if err != nil {
		if err == sql.ErrNoRows {
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
// CreateEntity inserts a brand-new canonical entity with a single property
// object (or none when props is empty) and publishes a creation event.
func (r *EntitiesRepository) CreateEntity(displayName string, types []string, props json.RawMessage) (Entity, error) {
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
	e, err := r.GetEntity(int(res.LastInsertId))
	if err != nil {
		return Entity{}, err
	}
	// A brand-new entity is an entity lifecycle event.
	r.publishEvent(e.ID, e.DisplayName)
	return e, nil
}

// createEntity is a test-support alias for CreateEntity retained so the
// package's own tests can seed entities without importing the exported path.
func (r *EntitiesRepository) createEntity(displayName string, types []string, props json.RawMessage) (Entity, error) {
	return r.CreateEntity(displayName, types, props)
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
// tries an exact alias match (canonical key of the name or of the model id, in a
// single query), then falls back to fuzzy FTS5 matching above the threshold with
// a type-compatibility gate. Returns (entity, found).
func (r *EntitiesRepository) resolveEntity(name, modelID string, types []string) (Entity, bool, error) {
	if e, ok, err := r.lookupAlias(canonKey(name), canonKey(modelID)); err != nil {
		return Entity{}, false, err
	} else if ok {
		return e, true, nil
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

// ExtractPass runs both phases for a single pass: it first folds every entity
// into the canonical graph (ExtractPassEntities) and then wires up the
// relations between them (ExtractPassRelations), marking the pass extracted
// once relations are done. Callers that need finer control (e.g. reconcile's
// backfill) may run the two phases separately.
func (r *EntitiesRepository) ExtractPass(ctx context.Context, p Pass) error {
	if err := r.ExtractPassEntities(ctx, p); err != nil {
		return err
	}
	return r.ExtractPassRelations(ctx, p)
}

// ExtractPassEntities is phase one: it parses a single pass result and merges
// every entity it mentions into the canonical graph. It does NOT touch
// relations and does NOT mark the pass extracted, so relations (phase two) can
// be processed in a separate step once all entities for the pass are committed.
func (r *EntitiesRepository) ExtractPassEntities(ctx context.Context, p Pass) error {
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

// ExtractPassRelations is phase two: it parses the same pass result and creates
// a relation for every relation it declares, resolving the source/target names
// to their canonical entities. Because phase one has already committed all
// entities for this pass, every relation's endpoints resolve to an entity that
// actually exists (either just created or carried over from an earlier pass).
// The pass is marked extracted only after relations are processed, so a
// re-run that fails mid-way replays the whole pass rather than leaving it
// half-wired. Parse failures are treated as "nothing to extract".
func (r *EntitiesRepository) ExtractPassRelations(ctx context.Context, p Pass) error {
	defer r.markExtracted(p.ID)

	var res llmAnalysis
	if err := json.Unmarshal([]byte(p.Result), &res); err != nil {
		return nil
	}
	for _, rel := range res.Relations {
		if rel.Source == "" || rel.Target == "" || rel.Type == "" {
			continue
		}
		src, ok, err := r.lookupAlias(canonKey(rel.Source), "")
		if err != nil {
			return err
		}
		if !ok {
			log.Printf("relation source entity %q not found; skipping relation", rel.Source)
			continue
		}
		dst, ok, err := r.lookupAlias(canonKey(rel.Target), "")
		if err != nil {
			return err
		}
		if !ok {
			log.Printf("relation target entity %q not found; skipping relation", rel.Target)
			continue
		}
		propsJSON, _ := json.Marshal(rel.Properties)
		if err := r.insertRelation(src.ID, dst.ID, rel.Type, string(propsJSON), rel.Properties.Conf, p.ID, p.LinkQueueID, p.ChunkIndex); err != nil {
			return err
		}
	}
	return nil
}

// insertRelation persists one relation between two canonical entities and
// publishes an entity lifecycle event for each endpoint so downstream consumers
// learn that the entity's knowledge graph changed.
func (r *EntitiesRepository) insertRelation(sourceID, targetID int, relType, propsJSON, conf string, passID, linkID, chunk int) error {
	// Guard against duplicate relations: the relations phase may be replayed
	// (e.g. after a partial failure), so only insert when no relation with the
	// same endpoint pair, type and originating pass already exists.
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO relations (source_id, target_id, type, properties, confidence, pass_id, link_id, chunk_index) "+
			"SELECT {}, {}, {}, {}, {}, {}, {}, {} "+
			"WHERE NOT EXISTS ("+
			"SELECT 1 FROM relations "+
			"WHERE pass_id = {} AND source_id = {} AND target_id = {} AND type = {})",
		sourceID, targetID, relType, propsJSON, conf, passID, linkID, chunk,
		passID, sourceID, targetID, relType)); err != nil {
		return err
	}
	// Emit an entity event for both endpoints: the relation edits their graph.
	if src, err := r.GetEntity(sourceID); err == nil {
		r.publishEvent(src.ID, src.DisplayName)
	}
	if dst, err := r.GetEntity(targetID); err == nil {
		r.publishEvent(dst.ID, dst.DisplayName)
	}
	return nil
}

func (r *EntitiesRepository) markExtracted(passID int) {
	_, _ = sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE passes SET extracted_at = CURRENT_TIMESTAMP WHERE id = {}", passID))
}

// ResetGraph drops the entire canonical knowledge graph and unmarks every pass
// as extracted so the reconcile command can rebuild it from scratch. Entities
// cascade to aliases/mentions/relations; the standalone FTS index is cleared
// explicitly. Each step is logged so the operator can follow the wipe.
func (r *EntitiesRepository) ResetGraph(ctx context.Context) error {
	if FTSAvailable() {
		log.Println("reset: clearing entity FTS index")
		if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM entity_fts")); err != nil {
			return err
		}
	}
	log.Println("reset: deleting entities (cascades aliases, mentions, relations, queue)")
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM relations")); err != nil {
		return err
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM entity_mentions")); err != nil {
		return err
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM entity_aliases")); err != nil {
		return err
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM entities")); err != nil {
		return err
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM goqite")); err != nil {
		return err
	}
	log.Println("reset: unmarking every pass as extracted")
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("UPDATE passes SET extracted_at = NULL")); err != nil {
		return err
	}
	log.Println("reset: graph cleared, ready for re-extraction")
	return nil
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
	e, err := r.CreateEntity(name, types, props)
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
		e, err = r.reconcile(e, match, nil, false)
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

// findMergePair returns the first mergeable pair from an in-memory snapshot of
// the graph, or nil when the graph is stable. It performs NO database access.
// Two entities merge when their alias sets collide (exact) or when their display
// names are fuzzy-similar above the threshold with compatible types. The alias
// collision is detected via an inverted index (alias -> owners) in O(total
// aliases) instead of an O(n^2) pairwise intersection; the fuzzy name comparison
// stays pairwise because it must catch near-duplicates (e.g. a one-token
// difference at the end of the name) that an exact-alias index would miss.
func (r *EntitiesRepository) findMergePair(ents []Entity, aliases map[int][]string) (*Entity, *Entity, error) {
	byID := make(map[int]*Entity, len(ents))
	for i := range ents {
		byID[ents[i].ID] = &ents[i]
	}
	// Exact alias collision via inverted index (deterministic, O(total aliases)).
	seen := make(map[string]int)
	for _, e := range ents {
		for _, al := range aliases[e.ID] {
			if owner, ok := seen[al]; ok && owner != e.ID {
				return byID[owner], byID[e.ID], nil
			}
			if _, ok := seen[al]; !ok {
				seen[al] = e.ID
			}
		}
	}
	// Fuzzy name similarity fallback (pairwise, in-memory).
	for i := 0; i < len(ents); i++ {
		for j := i + 1; j < len(ents); j++ {
			if similarity(ents[i].DisplayName, ents[j].DisplayName) >= fuzzyThreshold &&
				typesCompatible(ents[i].Types, ents[j].Types) {
				return &ents[i], &ents[j], nil
			}
		}
	}
	return nil, nil, nil
}

// GlobalReconcile repeatedly merges duplicate entities until the graph is
// stable, returning the number of merges performed. The full graph is loaded
// once into an in-memory snapshot; each merge updates that snapshot
// incrementally (refreshing only the survivor's aliases with a single query)
// instead of re-reading the entire entities table on every pass.
func (r *EntitiesRepository) GlobalReconcile(ctx context.Context) (int, error) {
	ents, err := r.allEntities()
	if err != nil {
		return 0, err
	}
	aliases, err := r.allAliases()
	if err != nil {
		return 0, err
	}
	total := 0
	for {
		a, b, err := r.findMergePair(ents, aliases)
		if err != nil {
			return total, err
		}
		if a == nil || b == nil {
			break
		}
		survivor, err := r.reconcile(*a, *b, nil, false)
		if err != nil {
			return total, err
		}
		// The loser is whichever input was not chosen as the survivor.
		loserID := a.ID
		if survivor.ID == a.ID {
			loserID = b.ID
		}
		// Update the snapshot in place: drop the loser and replace the
		// survivor's entry with its post-merge value.
		updated := make([]Entity, 0, len(ents))
		for _, e := range ents {
			if e.ID == loserID {
				continue
			}
			if e.ID == survivor.ID {
				updated = append(updated, survivor)
				continue
			}
			updated = append(updated, e)
		}
		ents = updated
		delete(aliases, loserID)
		if aliases[survivor.ID], err = r.aliasesFor(survivor.ID); err != nil {
			return total, err
		}
		total++
	}
	return total, nil
}

// relationTextBlock flattens a relation's properties (details, exact quote,
// amount, when, conf) plus its confidence column into one normalized string so
// two relations can be compared field-by-field in a single token set.
func relationTextBlock(props, _ string) string {
	p := ParseRelationProperties(props)
	var parts []string
	if p.Details != "" {
		parts = append(parts, p.Details)
	}
	if p.ExactQuote != "" {
		parts = append(parts, p.ExactQuote)
	}
	return strings.Join(parts, " ")
}

// relationsClose reports whether two relations' property text blocks are close
// enough to be duplicates. Two empty blocks are duplicates (they carry no
// distinguishing detail), otherwise the blocks must pass the token-similarity
// threshold.
func relationsClose(a, b string) bool {
	if a == "" && b == "" {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	return len(a) > 100 && similarity(a, b) >= relationDedupThreshold
}

// relationDedupGroup keys relations that may duplicate: the same ordered pair of
// endpoints and the same relation type.
type relationDedupGroup struct {
	source, target int
	relType        string
}

// DedupeRelations drops duplicate relations: rows that share the same ordered
// entity pair and relation type and whose properties, flattened to a text block,
// are very close. For each such pair the row with the shorter text block is
// deleted (ties keep the earlier row). It returns the number of relations
// deleted and publishes an entity event for every affected endpoint so
// downstream consumers rebuild their state.
func (r *EntitiesRepository) DedupeRelations(ctx context.Context) (int, error) {
	rels, err := sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM relations ORDER BY id"), RelationMapper)
	if err != nil {
		return 0, err
	}

	groups := make(map[relationDedupGroup][]*Relation)
	for i := range rels {
		k := relationDedupGroup{min(rels[i].SourceID, rels[i].TargetID), max(rels[i].SourceID, rels[i].TargetID), rels[i].Type}
		groups[k] = append(groups[k], &rels[i])
	}

	affected := make(map[int]struct{})
	deleted := 0
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		// Map each relation to its text block and sort descending by block length
		// so the greedy pass always keeps the fullest (longest) variant and drops
		// any shorter one that is close enough to it.
		type candidate struct {
			rel *Relation
			blk string
		}
		cands := make([]candidate, 0, len(group))
		for _, rel := range group {
			cands = append(cands, candidate{rel, relationTextBlock(rel.Properties, rel.Confidence)})
		}
		sort.Slice(cands, func(i, j int) bool { return len(cands[i].blk) > len(cands[j].blk) })

		var kept []candidate
		for _, c := range cands {
			dup := false
			for _, k := range kept {
				if relationsClose(c.blk, k.blk) {
					dup = true
					break
				}
			}
			if dup {
				if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
					"DELETE FROM relations WHERE id = {}", c.rel.ID)); err != nil {
					return deleted, err
				}
				affected[c.rel.SourceID] = struct{}{}
				affected[c.rel.TargetID] = struct{}{}
				deleted++
				continue
			}
			kept = append(kept, c)
		}
	}

	for id := range affected {
		if e, err := r.GetEntity(id); err == nil {
			r.publishEvent(e.ID, e.DisplayName)
		}
	}
	return deleted, nil
}

// reconcile folds two entities into one. The survivor is chosen by pickSurvivor
// (known > higher promotion > earlier created) unless prefer is non-nil and
// points at a.ID or b.ID, in which case that entity is forced to survive; their
// types, properties and display name are unioned; the loser's mentions and
// aliases are redirected to the survivor; a permanent redirect is recorded; and
// the loser is deleted. keepName forces the survivor's display name to be kept
// even when the loser's is strictly longer (used for manual merges). It is the
// single merge operation used both when ingesting a new mention and during
// GlobalReconcile. It returns the surviving entity.
func (r *EntitiesRepository) reconcile(a, b Entity, prefer *int, keepName bool) (Entity, error) {
	var survivor, loser Entity
	if prefer != nil && (*prefer == a.ID || *prefer == b.ID) {
		if *prefer == a.ID {
			survivor, loser = a, b
		} else {
			survivor, loser = b, a
		}
	} else {
		survivor, loser = pickSurvivor(a, b)
	}

	newTypes := unionStrings(survivor.Types, loser.Types)
	newProps := unionProperties(survivor.Properties, json.RawMessage(loser.Properties))
	// The survivor's display name is kept unless the loser's is strictly longer;
	// pass keepName to always keep the survivor's name (e.g. manual merges).
	display := survivor.DisplayName
	if !keepName && len(loser.DisplayName) > len(display) {
		display = loser.DisplayName
	}

	// Redirect loser's mentions to the survivor.
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE OR IGNORE entity_mentions SET entity_id = {} WHERE entity_id = {}",
		survivor.ID, loser.ID)); err != nil {
		return Entity{}, err
	}

	// Redirect relations that pointed at the loser to the survivor so the
	// knowledge graph stays consistent after a merge. A relation whose both
	// endpoints collapse onto the survivor becomes a self-loop and is dropped.
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE OR IGNORE relations SET source_id = {} WHERE source_id = {}",
		survivor.ID, loser.ID)); err != nil {
		return Entity{}, err
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE OR IGNORE relations SET target_id = {} WHERE target_id = {}",
		survivor.ID, loser.ID)); err != nil {
		return Entity{}, err
	}
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"DELETE FROM relations WHERE source_id = target_id")); err != nil {
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
		"DELETE FROM entities WHERE id = {}", loser.ID)); err != nil {
		return Entity{}, err
	}
	if err := r.syncFTS(survivor.ID); err != nil {
		return Entity{}, err
	}
	// A merge is an entity update: broadcast the surviving entity (with its
	// merged display name) so downstream consumers can react.
	r.publishEvent(survivor.ID, display)
	// Return the fresh survivor so callers (GlobalReconcile's snapshot) get the
	// updated fields rather than the pre-merge struct.
	return r.GetEntity(survivor.ID)
}

// MergeEntities folds the slave entity into the master entity, forcing the
// master to survive (regardless of known/promotion/created rules), and returns
// the surviving (master) entity. Both ids must correspond to existing rows, or
// an error is returned.
func (r *EntitiesRepository) MergeEntities(masterID, slaveID int) (Entity, error) {
	if masterID == slaveID {
		return Entity{}, fmt.Errorf("master and slave are the same entity (%d)", masterID)
	}
	master, err := r.GetEntity(masterID)
	if err != nil {
		return Entity{}, fmt.Errorf("load master %d: %w", masterID, err)
	}
	slave, err := r.GetEntity(slaveID)
	if err != nil {
		return Entity{}, fmt.Errorf("load slave %d: %w", slaveID, err)
	}
	return r.reconcile(master, slave, &masterID, true)
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

// allAliases loads every alias row in a single query and groups them by entity
// id. It replaces the per-entity aliasesFor lookups that GlobalReconcile used to
// issue on every pass.
func (r *EntitiesRepository) allAliases() (map[int][]string, error) {
	rows, err := sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT entity_id, alias FROM entity_aliases"),
		func(row *sq.Row) struct {
			EntityID int
			Alias    string
		} {
			return struct {
				EntityID int
				Alias    string
			}{EntityID: row.Int("entity_id"), Alias: row.String("alias")}
		})
	if err != nil {
		return nil, err
	}
	out := make(map[int][]string, len(rows))
	for _, rrow := range rows {
		out[rrow.EntityID] = append(out[rrow.EntityID], rrow.Alias)
	}
	return out, nil
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

// llmAnalysis is the subset of the model result we consume.
type llmAnalysis struct {
	Entities []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Properties struct {
			Name string `json:"name"`
		} `json:"properties"`
	} `json:"entities"`
	Relations []struct {
		Source     string `json:"source"`
		Target     string `json:"target"`
		Type       string `json:"type"`
		Properties struct {
			Details    string `json:"details"`
			ExactQuote string `json:"exact_quote"`
			Amount     string `json:"amount"`
			When       string `json:"when"`
			Conf       string `json:"conf"`
		} `json:"properties"`
	} `json:"relations"`
}
