-- +goose Up

-- The administrator's limits on routing presets (docs/domains/routing.md「滚动发布、失败与性能」): each layer's live
-- presets and all of them with the disabled ones, the rule versions a preset keeps, and the global budget of presets
-- and bytes of current rules every node's snapshot holds. A column left NULL is the default in code
-- (internal/routing/limits.go), so the record holds only deliberate departures. rev moves on every change, which names
-- the rev it read; the row stays when every value is cleared, so a rev is never handed out twice. The bounds repeat
-- Limits.Check. The kept versions cannot go below the 50 the delete policy of
-- preset_versions keeps (migration 00002), so that policy still holds whatever is saved here.
CREATE TABLE routing.preset_limits (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    rev bigint NOT NULL CHECK (rev >= 1),
    platform_active bigint CHECK (platform_active BETWEEN 1 AND 500),
    platform_total bigint CHECK (platform_total BETWEEN 1 AND 5000),
    site_active bigint CHECK (site_active BETWEEN 1 AND 500),
    site_total bigint CHECK (site_total BETWEEN 1 AND 5000),
    account_active bigint CHECK (account_active BETWEEN 1 AND 500),
    account_total bigint CHECK (account_total BETWEEN 1 AND 5000),
    workspace_active bigint CHECK (workspace_active BETWEEN 1 AND 500),
    workspace_total bigint CHECK (workspace_total BETWEEN 1 AND 5000),
    kept_versions bigint CHECK (kept_versions BETWEEN 50 AND 500),
    max_presets bigint CHECK (max_presets BETWEEN 1 AND 1000000),
    max_bytes bigint CHECK (max_bytes BETWEEN 1048576 AND 2147483648)
);
-- Every preset write reads it, in the writer's scope, so every scope reads it; only the platform's changes it.
ALTER TABLE routing.preset_limits ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON routing.preset_limits FOR SELECT USING (true);
CREATE POLICY platform ON routing.preset_limits USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, UPDATE ON routing.preset_limits TO axiom_app;

-- +goose Down
DROP TABLE routing.preset_limits;
