-- +goose Up

-- The administrator's segment settings for reservations (docs/domains/billing.md「预留与结算」): how many output tokens
-- a segment covers, and how much time the overshoot allowance covers. A column left NULL is the default in code
-- (segments.go); a row with neither column set is deleted, so the record holds only deliberate departures. Every node
-- reloads it within 30 seconds, and routing and billing read the same live policy.
CREATE TABLE billing.segment_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    output_tokens bigint CHECK (output_tokens BETWEEN 1 AND 1000000),
    overshoot_seconds integer CHECK (overshoot_seconds BETWEEN 1 AND 600)
);
ALTER TABLE billing.segment_settings ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform ON billing.segment_settings USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, UPDATE, DELETE ON billing.segment_settings TO axiom_app;

-- +goose Down
DROP TABLE billing.segment_settings;
