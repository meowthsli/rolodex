CREATE TABLE IF NOT EXISTS link_queue (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	url TEXT NOT NULL,
	content BLOB,
	readable_text TEXT,
	last_scrapped DATETIME,
	error TEXT
);

-- Partial index covering only not-yet-scraped rows, which is exactly the set
-- queried by GetNextPendingLink (WHERE last_scrapped IS NULL). This keeps the
-- index small instead of indexing every (already-scraped) row.
CREATE INDEX IF NOT EXISTS idx_link_queue_pending ON link_queue (last_scrapped) WHERE last_scrapped IS NULL;
