-- Reverse 000008: claims -> relations, and re-key "claims" back to "relations"
-- in every stored pass result.
UPDATE passes
SET result = json_set(
    json_remove(result, '$."claims"'),
    '$."relations"',
    json_extract(result, '$."claims"')
)
WHERE json_extract(result, '$."claims"') IS NOT NULL;

ALTER TABLE claims RENAME TO relations;
DROP INDEX idx_claims_source;
DROP INDEX idx_claims_target;
DROP INDEX idx_claims_link;
CREATE INDEX idx_relations_source ON relations(source_id);
CREATE INDEX idx_relations_target ON relations(target_id);
CREATE INDEX idx_relations_link   ON relations(link_id);
