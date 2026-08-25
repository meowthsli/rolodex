CREATE TABLE IF NOT EXISTS passes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    link_queue_id INTEGER NOT NULL REFERENCES link_queue(id) ON DELETE CASCADE,
    content_hash  TEXT    NOT NULL,
    result        TEXT    NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    error         TEXT,
    UNIQUE(link_queue_id)
);

CREATE TABLE IF NOT EXISTS excerpts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    pass_id      INTEGER NOT NULL REFERENCES passes(id) ON DELETE CASCADE,
    text         TEXT    NOT NULL,
    start_offset INTEGER NOT NULL,
    end_offset   INTEGER NOT NULL,
    span_hash    TEXT,
    UNIQUE(pass_id, start_offset, end_offset)
);

CREATE INDEX IF NOT EXISTS idx_passes_link_queue ON passes (link_queue_id);
CREATE INDEX IF NOT EXISTS idx_excerpts_pass ON excerpts (pass_id);
CREATE INDEX IF NOT EXISTS idx_excerpts_span_hash ON excerpts (span_hash);
