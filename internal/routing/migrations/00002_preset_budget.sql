-- +goose Up

-- What every routing preset takes together, presets disabled included, and the current version's rules as PostgreSQL
-- prints them: the global budget a snapshot can hold (docs/domains/routing.md「滚动发布、失败与性能」). It is one row
-- and not a count of routing.presets because a tenant's scope cannot count other tenants' rows; every write that adds
-- a preset or changes rules takes the row first, so it is also the lock that orders those writes, and a tenant's own
-- limits are counted after it. The row holds totals only, nothing of any tenant, so every scope reads and writes it.
CREATE TABLE routing.preset_budget (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    presets bigint NOT NULL CHECK (presets >= 0),
    bytes bigint NOT NULL CHECK (bytes >= 0)
);
ALTER TABLE routing.preset_budget ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY totals ON routing.preset_budget USING (true) WITH CHECK (true);
GRANT SELECT, UPDATE (presets, bytes) ON routing.preset_budget TO axiom_app;
INSERT INTO routing.preset_budget (presets, bytes)
    SELECT count(*), coalesce(sum(octet_length(v.rules::text)), 0) FROM routing.presets p
    JOIN routing.preset_versions v ON v.preset_id = p.id AND v.version = p.current_version;

-- A tenant's limits count its presets, the disabled ones too, which the slug index leaves out.
CREATE INDEX presets_owner ON routing.presets (site_id, account_id, workspace_id);

-- +goose Down
DROP INDEX routing.presets_owner;
DROP TABLE routing.preset_budget;
