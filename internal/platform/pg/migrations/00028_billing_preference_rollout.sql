-- +goose Up
-- A funding order other than subscription_first is safe only while every gateway node reads it. Old nodes do not read
-- the preference, so they must keep the default; this flag lets a new node say it does.
ALTER TABLE platform.nodes ADD COLUMN IF NOT EXISTS supports_billing_preference boolean NOT NULL DEFAULT false;
GRANT UPDATE (supports_billing_preference) ON platform.nodes TO axiom_app;

-- One row, maintained by every new node's heartbeat: true only while every live node reports support. A table rather
-- than a security-definer function because admission reads it in an account scope, while platform.nodes is
-- platform-only. It holds one boolean and no tenant's row.
CREATE TABLE IF NOT EXISTS platform.billing_preference_readiness (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    enabled boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE platform.billing_preference_readiness ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON platform.billing_preference_readiness;
CREATE POLICY reads ON platform.billing_preference_readiness FOR SELECT USING (true);
DROP POLICY IF EXISTS writes ON platform.billing_preference_readiness;
CREATE POLICY writes ON platform.billing_preference_readiness FOR ALL USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE ON platform.billing_preference_readiness TO axiom_app;

-- +goose Down
DROP TABLE IF EXISTS platform.billing_preference_readiness;
ALTER TABLE platform.nodes DROP COLUMN IF EXISTS supports_billing_preference;
