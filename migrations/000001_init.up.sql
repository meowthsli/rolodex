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
