-- +goose Up
ALTER TABLE observability.alert_receipts ADD COLUMN payload jsonb;
CREATE INDEX alert_queued_receipts ON observability.alert_receipts(at,id) WHERE state='queued';
CREATE TABLE observability.webhook_keys (
    id text PRIMARY KEY,
    channel_id text NOT NULL,
    destination text NOT NULL,
    expires_at timestamptz,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL
);
CREATE UNIQUE INDEX webhook_current_key ON observability.webhook_keys(channel_id,destination) WHERE expires_at IS NULL;
CREATE INDEX webhook_key_lookup ON observability.webhook_keys(channel_id,destination,expires_at);
CREATE TABLE observability.channel_tests (
    channel_id text PRIMARY KEY,
    destination text NOT NULL,
    receipt_id uuid REFERENCES observability.alert_receipts ON DELETE SET NULL,
    next_due timestamptz NOT NULL,
    reported_state text NOT NULL DEFAULT ''
);
ALTER TABLE observability.webhook_keys ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE observability.channel_tests ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.webhook_keys USING (platform.in_platform_scope());
CREATE POLICY platform_only ON observability.channel_tests USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE, DELETE ON observability.webhook_keys,observability.channel_tests TO axiom_app;
