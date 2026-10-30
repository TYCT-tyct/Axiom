-- +goose Up
-- The detector's per-account state: one row while an account is anomalous, deleted when it is not. The row is the
-- incident identity, so a persistent anomaly notifies once and a later, separate burst notifies again.
CREATE TABLE observability.spend_anomalies (
    account_id uuid NOT NULL,
    kind text NOT NULL CHECK (char_length(kind) BETWEEN 1 AND 64),
    PRIMARY KEY (account_id, kind)
);
ALTER TABLE observability.spend_anomalies ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.spend_anomalies USING (platform.in_platform_scope());
GRANT SELECT, INSERT, DELETE ON observability.spend_anomalies TO axiom_app;

-- Thresholds are admin data. They live apart from alert_configuration.body so an older node replacing the rules
-- cannot silently drop them during a rolling release; that node still returns rules and channels without this table.
CREATE TABLE observability.spend_detection (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    body jsonb NOT NULL CHECK (jsonb_typeof(body) = 'object' AND octet_length(body::text) <= 4096)
);
ALTER TABLE observability.spend_detection ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.spend_detection USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE ON observability.spend_detection TO axiom_app;

-- The detector looks up the accounts with calls in its recent window before reading each one's history. Without this
-- index that lookup scans a monthly partition; the account index cannot serve a time range whose leading column it
-- does not have. The always-true predicate keeps the index out of account-scoped console queries: without it the
-- planner can prefer this time-led index and then sort a whole account's month, a regression the plan tests catch.
-- Pre-launch only: for a populated deployment build it per partition CONCURRENTLY and attach, as README.md's index
-- section describes, instead of taking the write lock this statement takes.
CREATE INDEX call_log_at_account ON observability.call_log (at, account_id) WHERE price_nano >= 0;

-- Bind the kind once so an administrator only has to choose a channel; an empty channel keeps the event visible
-- without sending it anywhere. Existing custom rules are preserved.
UPDATE observability.alert_configuration
SET rev = rev + 1,
    body = jsonb_set(body, '{rules}', (body->'rules') || jsonb_build_array(jsonb_build_object(
        'id', 'spend-anomaly',
        'kind', 'observability.spend_anomaly',
        'minimum_severity', 'warning',
        'threshold', 1,
        'duration_seconds', 0,
        'channel', '',
        'quiet_start', 0,
        'quiet_end', 0,
        'repeat_seconds', 0)))
WHERE NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(body->'rules') AS rule
    WHERE rule->>'kind' = 'observability.spend_anomaly');
