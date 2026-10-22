-- +goose Up

-- sync_state is what the scheduled dataset sync leaves for people and for alerting: when a round last read every
-- dataset, how many rounds have failed since, and how many held changes with a candidate price wait for confirmation. One row,
-- platform data. The row is inserted before row-level security is enabled, because a migration runs with no scope; its
-- timestamps start empty, so a database that has never synced says so. failures and failing_since are set together.
CREATE TABLE catalog.sync_state (
    singleton bool PRIMARY KEY DEFAULT true CHECK (singleton),
    last_success_at timestamptz,
    failing_since timestamptz,
    failures bigint NOT NULL DEFAULT 0 CHECK (failures >= 0),
    pending bigint NOT NULL DEFAULT 0 CHECK (pending >= 0),
    CHECK ((failing_since IS NULL) = (failures = 0))
);
INSERT INTO catalog.sync_state DEFAULT VALUES;
ALTER TABLE catalog.sync_state ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_read ON catalog.sync_state FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY platform_update ON catalog.sync_state FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());
GRANT SELECT, UPDATE (last_success_at, failing_since, failures, pending) ON catalog.sync_state TO axiom_app;
