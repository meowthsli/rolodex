## Required

Use only github.com/mattn/go-sqlite3 driver, never move to other sqlite drivers.

Always open sqlite connection with foreign key support ("PRAGMA foreign_keys = ON;", "?_foreign_keys=on" in connection string).

Each test should have clear and actual comment.

Always use FTS5 when asked with no fallbacks.

Always use SQ library when working with database, and not raw db interface.