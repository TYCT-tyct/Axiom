-- +goose Up
CREATE TABLE observability.alert_configuration (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    rev bigint NOT NULL DEFAULT 1,
    body jsonb NOT NULL
);
INSERT INTO observability.alert_configuration(body) VALUES ('{"rules":[
 {"id":"write-lag","kind":"observability.write_lag","minimum_severity":"warning","threshold":4000,"duration_seconds":60,"channel":"","quiet_start":0,"quiet_end":0,"repeat_seconds":0},
 {"id":"settlement","kind":"billing.settlement_anomaly","minimum_severity":"warning","threshold":1,"duration_seconds":0,"channel":"","quiet_start":0,"quiet_end":0,"repeat_seconds":0},
 {"id":"discarded","kind":"queue.discarded","minimum_severity":"warning","threshold":1,"duration_seconds":0,"channel":"","quiet_start":0,"quiet_end":0,"repeat_seconds":0},
 {"id":"records-lost","kind":"observability.records_lost","minimum_severity":"warning","threshold":1,"duration_seconds":0,"channel":"","quiet_start":0,"quiet_end":0,"repeat_seconds":0}],"channels":[]}');
CREATE TABLE observability.alert_events (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    key text NOT NULL,
    severity text NOT NULL,
    fields jsonb NOT NULL,
    occurrences bigint NOT NULL DEFAULT 1,
    state text NOT NULL CHECK (state IN ('firing','delivered','resolved')),
    fired_at timestamptz NOT NULL,
    pending_recovery boolean NOT NULL DEFAULT false,
    resolved_at timestamptz,
    checked_at timestamptz NOT NULL DEFAULT '-infinity'
);
CREATE UNIQUE INDEX alert_active ON observability.alert_events(kind,key) WHERE resolved_at IS NULL;
CREATE INDEX alert_evaluate ON observability.alert_events(checked_at,id) WHERE resolved_at IS NULL OR pending_recovery;
CREATE INDEX alert_retention ON observability.alert_events(resolved_at,id) WHERE resolved_at IS NOT NULL AND NOT pending_recovery;
CREATE TABLE observability.alert_receipts (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL REFERENCES observability.alert_events(id) ON DELETE CASCADE,
    rule_id text NOT NULL,
    sequence integer NOT NULL,
    channel jsonb NOT NULL,
    quiet_start integer NOT NULL,
    quiet_end integer NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','queued','delivered','failed')),
    at timestamptz NOT NULL,
    not_before timestamptz NOT NULL DEFAULT '-infinity',
    delivered_at timestamptz,
    CHECK ((state='delivered')=(delivered_at IS NOT NULL)),
    UNIQUE(event_id,rule_id,sequence)
);
CREATE INDEX alert_dispatch ON observability.alert_receipts(at,id) WHERE state='pending';
CREATE INDEX alert_receipt_page ON observability.alert_receipts(event_id,at DESC,id DESC);
ALTER TABLE observability.alert_configuration ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE observability.alert_events ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE observability.alert_receipts ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.alert_configuration USING (platform.in_platform_scope());
CREATE POLICY platform_only ON observability.alert_events USING (platform.in_platform_scope());
CREATE POLICY platform_only ON observability.alert_receipts USING (platform.in_platform_scope());
GRANT SELECT, UPDATE ON observability.alert_configuration TO axiom_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON observability.alert_events,observability.alert_receipts TO axiom_app;

CREATE TABLE observability.alert_windows (
    event_id uuid NOT NULL REFERENCES observability.alert_events(id) ON DELETE CASCADE,
    rule_id text NOT NULL,
    since timestamptz NOT NULL,
    PRIMARY KEY(event_id,rule_id)
);
ALTER TABLE observability.alert_windows ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.alert_windows USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE, DELETE ON observability.alert_windows TO axiom_app;
