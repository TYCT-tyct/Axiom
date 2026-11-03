-- +goose Up
-- Keeping prices to signed-in members is safe only while every gateway node reads the setting. Old nodes return
-- prices unconditionally, so they must keep the anonymous answer public; this flag lets a new node say it hides them.
ALTER TABLE platform.nodes ADD COLUMN IF NOT EXISTS supports_model_prices_public boolean NOT NULL DEFAULT false;
ALTER TABLE platform.nodes ADD COLUMN IF NOT EXISTS model_prices_public_session uuid;
GRANT UPDATE (supports_model_prices_public, model_prices_public_session) ON platform.nodes TO axiom_app;

-- One row, maintained by every new node's heartbeat: true only while every live node reports support. A table rather
-- than a security-definer function because the snapshot and the admin settings transaction read it in other scopes,
-- while platform.nodes is platform-only. It holds one boolean and no tenant's row.
CREATE TABLE IF NOT EXISTS platform.model_prices_public_readiness (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    enabled boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE platform.model_prices_public_readiness ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON platform.model_prices_public_readiness;
CREATE POLICY reads ON platform.model_prices_public_readiness FOR SELECT USING (true);
DROP POLICY IF EXISTS writes ON platform.model_prices_public_readiness;
CREATE POLICY writes ON platform.model_prices_public_readiness FOR ALL USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE ON platform.model_prices_public_readiness TO axiom_app;

-- +goose Down
DROP TABLE IF EXISTS platform.model_prices_public_readiness;
ALTER TABLE platform.nodes DROP COLUMN IF EXISTS model_prices_public_session;
ALTER TABLE platform.nodes DROP COLUMN IF EXISTS supports_model_prices_public;
