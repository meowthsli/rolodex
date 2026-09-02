-- Rename the "relations" concept to "claims" across the schema and stored data.
--
-- 1. Table and its indexes: relations -> claims (column names are unchanged).
--    SQLite cannot rename an index (no ALTER INDEX), so the old indexes are
--    dropped and recreated under the new names.
-- 2. Every stored pass result is opaque model JSON that carries the extracted
--    relations under the top-level "relations" key. The LLM now emits "claims",
--    so each blob's "relations" array is re-keyed to "claims". Blobs without a
--    "relations" key (older or entity-only results) are left untouched.
ALTER TABLE relations RENAME TO claims;
DROP INDEX idx_relations_source;
DROP INDEX idx_relations_target;
DROP INDEX idx_relations_link;
CREATE INDEX idx_claims_source ON claims(source_id);
CREATE INDEX idx_claims_target ON claims(target_id);
CREATE INDEX idx_claims_link   ON claims(link_id);

UPDATE passes
SET result = json_set(
    json_remove(result, '$."relations"'),
    '$."claims"',
    json_extract(result, '$."relations"')
)
WHERE json_extract(result, '$."relations"') IS NOT NULL;
