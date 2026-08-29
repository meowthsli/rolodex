-- Allow multiple entities (or the same entity via different raw forms) to share
-- an alias spelling. `alias` is no longer globally UNIQUE; lookups resolve via
-- the index below and callers pick the intended entity.
CREATE TABLE IF NOT EXISTS entity_aliases_new (
    entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    alias     TEXT    NOT NULL,
    raw_name  TEXT    NOT NULL,
    UNIQUE (entity_id, alias)
);
INSERT INTO entity_aliases_new (entity_id, alias, raw_name)
SELECT entity_id, alias, raw_name FROM entity_aliases;
DROP TABLE entity_aliases;
ALTER TABLE entity_aliases_new RENAME TO entity_aliases;
CREATE INDEX IF NOT EXISTS idx_entity_aliases_entity ON entity_aliases(entity_id);
CREATE INDEX IF NOT EXISTS idx_entity_aliases_alias ON entity_aliases(alias);
