-- +goose Up

-- The platform's kill switch on routing presets (docs/domains/routing.md「滚动发布、失败与性能」). While the row is
-- there, every node's snapshot holds the system preset alone, so a key bound to any other preset is refused 503
-- preset_unavailable instead of being routed past the sources and prices its preset allows, and no preset, default or
-- key's choice of a preset is written. Deleting the row resumes them. Every preset write reads it in the writer's
-- scope, so every scope reads it; only the platform's changes it.
CREATE TABLE routing.preset_suspension (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    since timestamptz NOT NULL DEFAULT statement_timestamp()
);
ALTER TABLE routing.preset_suspension ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON routing.preset_suspension FOR SELECT USING (true);
CREATE POLICY platform ON routing.preset_suspension USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, DELETE ON routing.preset_suspension TO axiom_app;

-- +goose Down
DROP TABLE routing.preset_suspension;
