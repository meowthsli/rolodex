-- Pre-computed long-text profiles for entities. Each row holds the rendered
-- document describing one canonical entity: its aliases and every claim it
-- participates in, written out in prose with the supporting quote and the
-- source page URL. Profiles are rebuilt on demand (from the entities and
-- claims tables) whenever the graph changes, so the document is always a
-- denormalized snapshot of the current knowledge graph.
CREATE TABLE IF NOT EXISTS entity_profiles (
    entity_id INTEGER PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    profile   TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
