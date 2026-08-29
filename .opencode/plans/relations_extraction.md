# Relations extraction — implementation plan

Status: APPROVED by user. Additional requirement added at approval time: **emit an
entity lifecycle event for each entity a relation refers to, both when the relation
is inserted during extraction and when it is edited (redirected) during a merge.**

The LLM already emits `relations` (see `facts/openai.go:69`), but nothing parses or
stores them. This plan closes that gap.

## Schema — new migration
File: `migrations/000003_relations.up.sql`
```sql
CREATE TABLE IF NOT EXISTS relations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id   INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    target_id   INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    properties  TEXT NOT NULL DEFAULT '{}',
    confidence  TEXT NOT NULL DEFAULT '',
    pass_id     INTEGER REFERENCES passes(id) ON DELETE CASCADE,
    link_id     INTEGER REFERENCES link_queue(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id);
CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id);
CREATE INDEX IF NOT EXISTS idx_relations_link   ON relations(link_id);
```
File: `migrations/000003_relations.down.sql`
```sql
DROP TABLE IF EXISTS relations;
```
No uniqueness constraint: duplicates are kept (per user decision). Indexes keep
source/target/link lookups fast. `setupTestDB` auto-applies every migration via
golang-migrate, so new tests immediately see the table.

## Code — `facts/entities.go`
1. Add a `RELATIONS` `sq.TableStruct` (fields: id, source_id, target_id, type,
   properties, confidence, pass_id, link_id, chunk_index, created_at), mirroring
   `ENTITIES`/`EN` (lines 66-79). Add `var RN = sq.New[RELATIONS]("r")`.
2. Add a `Relation` struct and a strict `RelationMapper` (mirror `Entity`/`EntityMapper`,
   lines 82-108). Fields: ID, SourceID, TargetID, Type, Properties(string JSON),
   Confidence, PassID, LinkID, ChunkIndex, CreatedAt.
3. Extend `llmAnalysis` (lines 710-718) with a `Relations` slice:
   ```go
   Relations []struct {
       Source string `json:"source"`
       Target string `json:"target"`
       Type   string `json:"type"`
       Properties struct {
           Details    string `json:"details"`
           ExactQuote string `json:"exact_quote"`
           Amount     string `json:"amount"`
           When       string `json:"when"`
           Conf       string `json:"conf"`
       } `json:"properties"`
   } `json:"relations"`
   ```
4. In `ExtractPass` (lines 360-382), after the existing entity loop, iterate
   `res.Relations`. For each relation with non-empty Source/Target/Type:
   - resolve `source` via `r.lookupAlias(canonKey(rel.Source), "")`;
   - resolve `target` via `r.lookupAlias(canonKey(rel.Target), "")`;
   - if either is missing → `log.Printf` a warning and `continue` (skip + log,
     per user decision);
   - marshal `rel.Properties` to JSON, then call `r.insertRelation(...)`.
   The entity aliases are already registered during the entity loop (both
   `canonKey(name)` and `canonKey(modelID)`), so same-pass and pre-existing
   entities both resolve.
5. Add helper:
   ```go
   // insertRelation persists one relation between two canonical entities and
   // publishes an entity lifecycle event for each endpoint so downstream
   // consumers learn the entity's graph changed.
   func (r *EntitiesRepository) insertRelation(sourceID, targetID int, relType, propsJSON, conf string, passID, linkID, chunk int) error {
       if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
           "INSERT INTO relations (source_id, target_id, type, properties, confidence, pass_id, link_id, chunk_index) "+
               "VALUES ({}, {}, {}, {}, {}, {}, {}, {})",
           sourceID, targetID, relType, propsJSON, conf, passID, linkID, chunk)); err != nil {
           return err
       }
       if src, err := r.GetEntity(sourceID); err == nil {
           r.publishEvent(src.ID, src.DisplayName)
       }
       if dst, err := r.GetEntity(targetID); err == nil {
           r.publishEvent(dst.ID, dst.DisplayName)
       }
       return nil
   }
   ```
6. In `reconcile` (lines 549-628), after the "Redirect loser's mentions" block
   (lines 568-573) and before the loser is deleted, redirect relations to the
   survivor and drop any self-loops introduced by the merge:
   ```go
   if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
       "UPDATE relations SET source_id = {} WHERE source_id = {}", survivor.ID, loser.ID)); err != nil {
       return Entity{}, err
   }
   if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
       "UPDATE relations SET target_id = {} WHERE target_id = {}", survivor.ID, loser.ID)); err != nil {
       return Entity{}, err
   }
   if _, err := sq.Exec(r.db, sq.SQLite.Queryf(
       "DELETE FROM relations WHERE source_id = target_id")); err != nil {
       return Entity{}, err
   }
   ```
   The entity event for the survivor is already published at the end of `reconcile`
   (line 624), which satisfies the "edit relation referring this entity" event for
   the merged-away side. No extra publish is needed there.

## Tests — new file `facts/relations_test.go`
Each test has a clear multi-sentence comment (per AGENTS.md). Use
`setupTestDB`, `insertLink`, `NewPassesRepository`, `newTestRepo`, `drainEntityEvents`
helpers already present.
- `TestExtractPassStoresRelation` — pass with two entities + one relation; assert a
  `relations` row with the resolved `source_id`/`target_id`, `type`, `properties` JSON,
  and provenance (pass_id, link_id, chunk_index).
- `TestExtractPassResolvesRelationToPreExistingEntity` — in pass 1 create entity
  `GORIN_EVGENIY`; in pass 2 emit a relation whose source/target is that id (no new
  entity of that id); assert the relation links to the existing entity id.
- `TestExtractPassSkipsRelationWithUnknownEntity` — relation referencing an id that
  was never extracted yields **no** `relations` row and logs.
- `TestReconcileRedirectsRelations` — create entities A and B, insert relation A→B,
  call `MergeEntities(A, B)`; assert the relation now points at the survivor id and
  the loser id no longer appears in `relations`, and that no self-loop remains.
- `TestExtractPassRelationPublishesEntityEvents` — after extracting a relation,
  `drainEntityEvents` contains an event for both the source and the target entity
  (the new requirement). Assert event ids include both endpoints.

## Verification
- `go build ./...`
- `go test ./facts/...` (relation tests run against the migrated temp DB; resolution
  is exact-alias based, so no `FTSAvailable()` gating is required).
- `go vet ./...`

## Out of scope (unchanged)
`machine.go`, `openai.go`, `cmd/reconcile` need no changes — relation extraction flows
through the existing `ExtractPass` call used by both the facts machine and the
backfill reconcile command.
