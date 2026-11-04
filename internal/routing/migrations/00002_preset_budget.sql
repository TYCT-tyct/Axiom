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

-- A layer's default takes its version from one sequence, never from its own row: clearing a default deletes the row,
-- and a version counted per row would start again at 1 when the layer sets one again, so a page holding the version
-- it read before the clear could overwrite the new default. The rows the migrations wrote are at 1, so the sequence
-- starts above them.
CREATE SEQUENCE routing.preset_default_versions START 2;
GRANT USAGE ON SEQUENCE routing.preset_default_versions TO axiom_app;

-- A preset keeps its last 50 rule versions (docs/domains/routing.md「数据模型」): the writer of the preset deletes the
-- older ones when it adds a version. The current version and the 49 before it can never be deleted, so a snapshot or
-- a reader of a recent version never misses one.
CREATE POLICY prunes ON routing.preset_versions FOR DELETE USING (
    EXISTS (
        SELECT 1 FROM routing.presets p WHERE p.id = preset_id AND version <= p.current_version - 50 AND (
            (SELECT platform.in_platform_scope())
            OR NOT p.is_system AND p.scope = 'site' AND p.site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
            OR NOT p.is_system AND p.scope IN ('account', 'workspace') AND p.account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
        )
    )
);
GRANT DELETE ON routing.preset_versions TO axiom_app;

-- +goose Down
REVOKE DELETE ON routing.preset_versions FROM axiom_app;
DROP POLICY prunes ON routing.preset_versions;
DROP SEQUENCE routing.preset_default_versions;
DROP INDEX routing.presets_owner;
DROP TABLE routing.preset_budget;
