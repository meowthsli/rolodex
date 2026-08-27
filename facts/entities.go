package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"
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

// EntitiesRepository provides entity/alias/mention operations and reconciliation.
type EntitiesRepository struct {
	db           *sql.DB
	ftsAvailable bool
}

// NewEntitiesRepository creates a repository and attempts to provision the FTS5
// virtual table used for fuzzy matching. If the driver was built without fts5
// support, fuzzy matching is disabled and only exact (alias) matching is used.
func NewEntitiesRepository(db *sql.DB) *EntitiesRepository {
	r := &EntitiesRepository{db: db}
	r.ensureFTS()
	return r
}

func (r *EntitiesRepository) ensureFTS() {
	_, err := r.db.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS entity_fts USING fts5(entity_id, name, tokenize='trigram')")
	if err != nil {
		log.Printf("fts5 unavailable, fuzzy matching disabled: %v", err)
		r.ftsAvailable = false
		return
	}
	r.ftsAvailable = true
}

// FTSAvailable reports whether fuzzy (FTS5) matching is active. Exposed so tests
// can skip fuzzy-dependent cases when built without -tags sqlite_fts5.
func (r *EntitiesRepository) FTSAvailable() bool { return r.ftsAvailable }

func (r *EntitiesRepository) scanEntity(row *sql.Row) (Entity, error) {
	var e Entity
	var types, props string
	var isKnown int
	if err := row.Scan(&e.ID, &e.DisplayName, &types, &props, &e.Confidence, &e.PromotionScore, &isKnown, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return Entity{}, err
	}
	e.Types = unmarshalTypes(types)
	e.Properties = props
	e.IsKnown = isKnown != 0
	return e, nil
}

func (r *EntitiesRepository) GetEntity(id int) (Entity, error) {
	return r.scanEntity(r.db.QueryRow(
		"SELECT id, display_name, types, properties, confidence, promotion_score, is_known, created_at, updated_at FROM entities WHERE id = ?", id))
}

// lookupAlias resolves a normalized alias to its canonical entity, if present.
func (r *EntitiesRepository) lookupAlias(alias string) (Entity, bool, error) {
	var id int
	err := r.db.QueryRow("SELECT entity_id FROM entity_aliases WHERE alias = ?", alias).Scan(&id)
	if err == sql.ErrNoRows {
		return Entity{}, false, nil
	}
	if err != nil {
		return Entity{}, false, err
	}
	e, err := r.GetEntity(id)
	if err != nil {
		if err == sql.ErrNoRows {
			// Alias points at an entity that was merged away or deleted: drop
			// the dangling alias and treat it as not found.
			_, _ = r.db.Exec("DELETE FROM entity_aliases WHERE alias = ?", alias)
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
	_, err := r.db.Exec("INSERT OR IGNORE INTO entity_aliases (entity_id, alias, raw_name) VALUES (?, ?, ?)", entityID, alias, rawName)
	return err
}

func (r *EntitiesRepository) addMention(entityID, passID, linkID, chunk int) error {
	_, err := r.db.Exec("INSERT OR IGNORE INTO entity_mentions (entity_id, pass_id, link_id, chunk_index) VALUES (?, ?, ?, ?)",
		entityID, passID, linkID, chunk)
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
	rows, err := r.db.Query("SELECT raw_name FROM entity_aliases WHERE entity_id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, nil
}

// createEntity inserts a brand-new canonical entity with a single property
// object and no aliases (aliases are added by the caller).
func (r *EntitiesRepository) createEntity(displayName string, types []string, props json.RawMessage) (Entity, error) {
	propArr := "[]"
	if len(props) > 0 {
		propArr = mustJSON([]json.RawMessage{props})
	}
	res, err := r.db.Exec(
		"INSERT INTO entities (display_name, types, properties) VALUES (?, ?, ?)",
		displayName, marshalTypes(types), propArr)
	if err != nil {
		return Entity{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Entity{}, err
	}
	return r.GetEntity(int(id))
}

// mergeEntityData folds a newly seen mention (name, model id, properties, type)
// into an existing entity: unions types and property objects, upgrades the
// display name to the longest variant, and persists the row.
func (r *EntitiesRepository) mergeEntityData(e Entity, name, modelID string, props json.RawMessage, types []string) (Entity, error) {
	newTypes := unionStrings(e.Types, types)
	newProps := unionProperties(e.Properties, props)
	display := e.DisplayName
	if name != "" && len(name) > len(display) {
		display = name
	}
	if _, err := r.db.Exec(
		"UPDATE entities SET display_name = ?, types = ?, properties = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		display, marshalTypes(newTypes), newProps, e.ID); err != nil {
		return e, err
	}
	return r.GetEntity(e.ID)
}

// bumpPromotion records a new "founding" of the entity and auto-promotes it to
// known once it reaches the promotion threshold.
func (r *EntitiesRepository) bumpPromotion(id int) error {
	_, err := r.db.Exec(
		"UPDATE entities SET promotion_score = promotion_score + 1, "+
			"is_known = CASE WHEN promotion_score + 1 >= ? THEN 1 ELSE is_known END, "+
			"updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		promotionThreshold, id)
	return err
}

// syncFTS refreshes the FTS5 index for an entity from its display name and all
// registered aliases. It is a no-op when FTS5 is unavailable.
func (r *EntitiesRepository) syncFTS(entityID int) error {
	if !r.ftsAvailable {
		return nil
	}
	if _, err := r.db.Exec("DELETE FROM entity_fts WHERE entity_id = ?", entityID); err != nil {
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
		if _, err := r.db.Exec("INSERT INTO entity_fts (entity_id, name) VALUES (?, ?)", entityID, n); err != nil {
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
	if !r.ftsAvailable {
		return nil, nil
	}
	q := "\"" + strings.ReplaceAll(name, "\"", "") + "\""
	rows, err := r.db.Query("SELECT entity_id, name FROM entity_fts WHERE name MATCH ?", q)
	if err != nil {
		return nil, nil // unparseable query -> no candidates
	}
	defer rows.Close()
	seen := make(map[int]bool)
	var out []fuzzyCandidate
	for rows.Next() {
		var eidStr, nm string
		if err := rows.Scan(&eidStr, &nm); err != nil {
			return nil, err
		}
		eid, perr := strconv.Atoi(eidStr)
		if perr != nil {
			continue
		}
		if seen[eid] {
			continue
		}
		seen[eid] = true
		e, err := r.GetEntity(eid)
		if err != nil {
			// The FTS index can reference an entity that was merged away or
			// deleted; skip the stale candidate instead of failing the pass.
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		out = append(out, fuzzyCandidate{Entity: e, Score: similarity(name, nm)})
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
	if r.ftsAvailable {
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
	_, _ = r.db.Exec("UPDATE passes SET extracted_at = CURRENT_TIMESTAMP WHERE id = ?", passID)
}

// resolveAndMerge is the single entry point for folding one mention into the
// graph: resolve to an existing entity (or create one), merge its data, register
// aliases, record the mention, bump promotion, and refresh the FTS index.
func (r *EntitiesRepository) resolveAndMerge(name, modelID string, props json.RawMessage, types []string, passID, linkID, chunk int) error {
	e, found, err := r.resolveEntity(name, modelID, types)
	if err != nil {
		return err
	}
	if !found {
		e, err = r.createEntity(name, types, props)
		if err != nil {
			return err
		}
	} else {
		e, err = r.mergeEntityData(e, name, modelID, props, types)
		if err != nil {
			return err
		}
	}
	if err := r.upsertAlias(e.ID, canonKey(name), name); err != nil {
		return err
	}
	if modelID != "" {
		if err := r.upsertAlias(e.ID, canonKey(modelID), modelID); err != nil {
			return err
		}
	}
	if err := r.bumpPromotion(e.ID); err != nil {
		return err
	}
	if err := r.addMention(e.ID, passID, linkID, chunk); err != nil {
		return err
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
		if err := r.mergeEntities(*a, *b); err != nil {
			return total, err
		}
		total++
	}
	return total, nil
}

// mergeEntities folds b into the chosen survivor (known > higher promotion >
// earlier created), preserving all aliases, mentions, properties and types, and
// records the redirect for a future relation-reconcile step.
func (r *EntitiesRepository) mergeEntities(a, b Entity) error {
	survivor, loser := pickSurvivor(a, b)

	newTypes := unionStrings(survivor.Types, loser.Types)
	newProps := unionProperties(survivor.Properties, json.RawMessage(loser.Properties))
	display := survivor.DisplayName
	if len(loser.DisplayName) > len(display) {
		display = loser.DisplayName
	}

	// Redirect loser's mentions to the survivor.
	if _, err := r.db.Exec("UPDATE OR IGNORE entity_mentions SET entity_id = ? WHERE entity_id = ?", survivor.ID, loser.ID); err != nil {
		return err
	}
	loserAliases, err := r.aliasesFor(loser.ID)
	if err != nil {
		return err
	}
	for _, al := range loserAliases {
		raw, rerr := r.rawNameForAlias(al)
		if rerr != nil {
			return rerr
		}
		if err := r.upsertAlias(survivor.ID, al, raw); err != nil {
			return err
		}
	}

	if _, err := r.db.Exec(
		"UPDATE entities SET display_name = ?, types = ?, properties = ?, promotion_score = promotion_score + ?, "+
			"is_known = CASE WHEN is_known = 1 OR ? = 1 THEN 1 ELSE 0 END, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		display, marshalTypes(newTypes), newProps, loser.PromotionScore, loser.IsKnown, survivor.ID); err != nil {
		return err
	}

	if _, err := r.db.Exec("DELETE FROM entity_aliases WHERE entity_id = ?", loser.ID); err != nil {
		return err
	}
	if r.ftsAvailable {
		if _, err := r.db.Exec("DELETE FROM entity_fts WHERE entity_id = ?", loser.ID); err != nil {
			return err
		}
	}
	if _, err := r.db.Exec("INSERT OR IGNORE INTO entity_redirects (old_id, new_id) VALUES (?, ?)", loser.ID, survivor.ID); err != nil {
		return err
	}
	if _, err := r.db.Exec("DELETE FROM entities WHERE id = ?", loser.ID); err != nil {
		return err
	}
	return r.syncFTS(survivor.ID)
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
	rows, err := r.db.Query("SELECT id, display_name, types, properties, confidence, promotion_score, is_known, created_at, updated_at FROM entities")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		var types, props string
		var isKnown int
		if err := rows.Scan(&e.ID, &e.DisplayName, &types, &props, &e.Confidence, &e.PromotionScore, &isKnown, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Types = unmarshalTypes(types)
		e.Properties = props
		e.IsKnown = isKnown != 0
		out = append(out, e)
	}
	return out, nil
}

func (r *EntitiesRepository) aliasesFor(id int) ([]string, error) {
	rows, err := r.db.Query("SELECT alias FROM entity_aliases WHERE entity_id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *EntitiesRepository) rawNameForAlias(alias string) (string, error) {
	var raw string
	err := r.db.QueryRow("SELECT raw_name FROM entity_aliases WHERE alias = ?", alias).Scan(&raw)
	return raw, err
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

// --- JSON helpers ---

func marshalTypes(t []string) string {
	if len(t) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(t)
	return string(b)
}

func unmarshalTypes(s string) []string {
	if s == "" {
		return nil
	}
	var t []string
	_ = json.Unmarshal([]byte(s), &t)
	return t
}

func unionStrings(a, b []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(a)+len(b))
	for _, x := range append(append([]string{}, a...), b...) {
		if x == "" {
			continue
		}
		k := strings.ToLower(x)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, x)
	}
	return out
}

// unionProperties concatenates two JSON arrays of property objects, dropping
// duplicate entries so all distinct values are preserved.
func unionProperties(existing string, add json.RawMessage) string {
	var a, b []json.RawMessage
	_ = json.Unmarshal([]byte(existing), &a)
	_ = json.Unmarshal(add, &b)
	seen := make(map[string]struct{})
	for _, x := range a {
		seen[string(x)] = struct{}{}
	}
	for _, x := range b {
		k := string(x)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		a = append(a, x)
	}
	out, _ := json.Marshal(a)
	return string(out)
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func aliasSetsIntersect(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := set[x]; ok {
			return true
		}
	}
	return false
}
