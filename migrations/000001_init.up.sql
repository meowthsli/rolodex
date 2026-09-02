CREATE TABLE IF NOT EXISTS link_queue (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	url TEXT NOT NULL,
	content BLOB,
	readable_text TEXT,
	last_scrapped_at DATETIME,
	added_at DATETIME,
	error TEXT,
	generation INTEGER NOT NULL DEFAULT 1
);

-- Partial index over links that still need scraping: those never scraped
-- (last_scrapped_at IS NULL) or whose stored content is older than when the
-- link was added/re-queued (last_scrapped_at < added_at). This keeps the index
-- small instead of indexing every (already-scraped) row.
CREATE INDEX IF NOT EXISTS idx_link_queue_pending ON link_queue (last_scrapped_at, added_at)
    WHERE last_scrapped_at IS NULL OR last_scrapped_at < added_at;

CREATE TABLE IF NOT EXISTS passes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    link_queue_id INTEGER NOT NULL REFERENCES link_queue(id) ON DELETE CASCADE,
    domain        TEXT    NOT NULL DEFAULT '',
    chunk_index   INTEGER NOT NULL DEFAULT 0,
    chunk_start   INTEGER NOT NULL DEFAULT 0,
    chunk_end     INTEGER NOT NULL DEFAULT 0,
    chunk_text    TEXT,
    content_hash  TEXT    NOT NULL,
    result        TEXT    NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    error         TEXT,
    UNIQUE(link_queue_id, domain, chunk_index)
);


CREATE INDEX IF NOT EXISTS idx_passes_link_queue ON passes (link_queue_id);

-- Canonical entities extracted from pass results. One row per real-world
-- entity; duplicates are merged into a single row. `types` and `properties`
-- are JSON: types is a string array, properties is a JSON array of every
-- property object seen (all values preserved on merge).
CREATE TABLE IF NOT EXISTS entities (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    display_name   TEXT    NOT NULL,
    types          TEXT    NOT NULL DEFAULT '[]',
    properties     TEXT    NOT NULL DEFAULT '[]',
    confidence     TEXT    NOT NULL DEFAULT '',
    promotion_score INTEGER NOT NULL DEFAULT 0,
    is_known       INTEGER NOT NULL DEFAULT 0,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Resolution table: every normalized alias (the model id and the computed
-- canonical key of each name variant) points at the canonical entity row.
-- `alias` is globally unique so a lookup unambiguously identifies an entity.
CREATE TABLE IF NOT EXISTS entity_aliases (
    entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    alias     TEXT    NOT NULL UNIQUE,
    raw_name  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entity_aliases_entity ON entity_aliases(entity_id);

-- Provenance: which pass/chunk mentioned an entity, so nothing is lost.
CREATE TABLE IF NOT EXISTS entity_mentions (
    entity_id   INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    pass_id     INTEGER REFERENCES passes(id) ON DELETE CASCADE,
    link_id     INTEGER REFERENCES link_queue(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (entity_id, pass_id, chunk_index)
);

-- Records entity merges so a future claim-reconcile step can redirect
-- old entity ids to their survivor (old_id is unique: an entity merges once).
CREATE TABLE IF NOT EXISTS entity_redirects (
    old_id INTEGER NOT NULL,
    new_id INTEGER NOT NULL,
    PRIMARY KEY (old_id)
);

-- Idempotency marker for entity extraction: NULL until a pass's result has
-- been parsed into entities, so the reconcile command can backfill safely.
ALTER TABLE passes ADD COLUMN extracted_at DATETIME;

