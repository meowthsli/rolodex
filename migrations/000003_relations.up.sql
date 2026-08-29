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
