-- +goose Up
CREATE TABLE platform.alert_positions (
    source text PRIMARY KEY,
    position jsonb NOT NULL DEFAULT '{}'
);
ALTER TABLE platform.alert_positions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON platform.alert_positions USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE ON platform.alert_positions TO axiom_app;
