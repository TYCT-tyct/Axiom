-- +goose Up

ALTER TABLE billing.exchange_rates DROP CONSTRAINT exchange_rates_source_check;
ALTER TABLE billing.exchange_rates ADD COLUMN source_at timestamptz,
    ADD CONSTRAINT exchange_rates_source_check CHECK (
        (source = 'manual' AND source_at IS NULL) OR (source = 'ecb' AND source_at IS NOT NULL));

CREATE TABLE billing.exchange_sync (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    started_at timestamptz,
    last_attempt timestamptz,
    last_success timestamptz,
    source_at timestamptz,
    alerted boolean NOT NULL DEFAULT false
);
INSERT INTO billing.exchange_sync(singleton) VALUES(true);
ALTER TABLE billing.exchange_sync ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform ON billing.exchange_sync USING (platform.in_platform_scope());
GRANT SELECT, UPDATE ON billing.exchange_sync TO axiom_app;
