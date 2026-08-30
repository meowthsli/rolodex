-- Each link carries the array of analysis domains it should be processed for.
-- Defaults to a single "venture" domain when not specified explicitly.
ALTER TABLE link_queue ADD COLUMN domains TEXT NOT NULL DEFAULT '["venture"]';
