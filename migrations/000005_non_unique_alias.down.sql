-- Re-create the original UNIQUE constraint on alias by rebuilding the table.
CREATE TABLE IF NOT EXISTS entity_aliases_new (
    entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    alias     TEXT    NOT NULL UNIQUE,
    raw_name  TEXT    NOT NULL
);
INSERT INTO entity_aliases_new (entity_id, alias, raw_name)
SELECT entity_id, alias, raw_name FROM entity_aliases;
DROP TABLE entity_aliases;
ALTER TABLE entity_aliases_new RENAME TO entity_aliases;
CREATE INDEX IF NOT EXISTS idx_entity_aliases_entity ON entity_aliases(entity_id);
