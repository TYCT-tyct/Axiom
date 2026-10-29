-- +goose Up

-- NULL identifies a node running the pre-share binary; it cannot acknowledge a handover.
ALTER TABLE platform.nodes
    ADD COLUMN share_session uuid,
    ADD COLUMN share_view text[];

-- Departed processes can still have requests in the upstream rolling minute. Keep their last partition until those
-- requests have aged out; this is membership metadata, never a request counter.
CREATE TABLE platform.credential_share_departures (
    id text PRIMARY KEY,
    share_view text[],
    expires_at timestamptz NOT NULL
);
ALTER TABLE platform.credential_share_departures ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON platform.credential_share_departures
    USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE, DELETE ON platform.credential_share_departures TO axiom_app;
