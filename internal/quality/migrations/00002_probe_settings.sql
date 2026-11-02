-- +goose Up

-- The administrator's probe settings (docs/domains/quality.md「试探」): the hourly budget and concurrency of recovery
-- probes, the fingerprint check's intervals, and the first probe delay. A column left NULL is the default in code
-- (settings.go); a row with no column set is deleted, so the record holds only deliberate departures. Every node reloads
-- it within 30 seconds.
CREATE TABLE quality.probe_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    hourly_budget bigint CHECK (hourly_budget BETWEEN 1 AND 100000),
    concurrency integer CHECK (concurrency BETWEEN 1 AND 64),
    verify_every_seconds integer CHECK (verify_every_seconds BETWEEN 60 AND 2592000),
    verify_max_age_seconds integer CHECK (verify_max_age_seconds BETWEEN 60 AND 2592000),
    verify_retry_seconds integer CHECK (verify_retry_seconds BETWEEN 60 AND 86400),
    probe_base_seconds integer CHECK (probe_base_seconds BETWEEN 1 AND 600),
    CHECK (verify_every_seconds IS NULL OR verify_max_age_seconds IS NULL OR verify_max_age_seconds >= verify_every_seconds),
    CHECK (verify_every_seconds IS NULL OR verify_retry_seconds IS NULL OR verify_retry_seconds <= verify_every_seconds)
);
ALTER TABLE quality.probe_settings ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform ON quality.probe_settings USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, UPDATE, DELETE ON quality.probe_settings TO axiom_app;

-- +goose Down
DROP TABLE quality.probe_settings;
