CREATE TABLE IF NOT EXISTS entity_redirects (
    old_id INTEGER NOT NULL,
    new_id INTEGER NOT NULL,
    PRIMARY KEY (old_id)
);
