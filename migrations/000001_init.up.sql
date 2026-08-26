CREATE TABLE IF NOT EXISTS link_queue (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	url TEXT NOT NULL,
	content BLOB,
	readable_text TEXT,
	last_scrapped_at DATETIME,
	added_at DATETIME,
	error TEXT
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
    content_hash  TEXT    NOT NULL,
    result        TEXT    NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    error         TEXT,
    UNIQUE(link_queue_id, domain)
);


CREATE INDEX IF NOT EXISTS idx_passes_link_queue ON passes (link_queue_id);
