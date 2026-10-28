-- +goose Up
CREATE TABLE platform.alert_signals (
    id uuid PRIMARY KEY,
    seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    body jsonb NOT NULL CHECK (octet_length(body::text) <= 65536)
);
ALTER TABLE platform.alert_signals ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON platform.alert_signals USING (platform.in_platform_scope());
GRANT SELECT, INSERT, DELETE ON platform.alert_signals TO axiom_app;
GRANT USAGE ON SEQUENCE platform.alert_signals_seq_seq TO axiom_app;
ALTER TABLE platform.alert_signals ADD COLUMN quarantined boolean NOT NULL DEFAULT false;
GRANT UPDATE(quarantined) ON platform.alert_signals TO axiom_app;
-- Older scanners may claim a job before a new node can persist its notification.
ALTER TABLE platform.discarded_jobs ADD COLUMN notified boolean NOT NULL DEFAULT false;
GRANT UPDATE(notified) ON platform.discarded_jobs TO axiom_app;
