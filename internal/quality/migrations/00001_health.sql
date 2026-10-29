-- +goose Up
GRANT USAGE ON SCHEMA quality TO axiom_app;

CREATE TABLE quality.endpoint_health (
    endpoint_id uuid PRIMARY KEY,
    epoch bigint NOT NULL CHECK (epoch >= 0),
    opened_at bigint NOT NULL DEFAULT 0,
    restored_at bigint NOT NULL DEFAULT 0,
    next_probe_at bigint NOT NULL DEFAULT 0,
    last_probe_at bigint NOT NULL DEFAULT 0,
    probe_lease_until bigint NOT NULL DEFAULT 0,
    backoff integer NOT NULL DEFAULT 0 CHECK (backoff BETWEEN 0 AND 5),
    failures integer NOT NULL DEFAULT 0,
    sources integer NOT NULL DEFAULT 0,
    cause text NOT NULL DEFAULT '',
    probe_result text NOT NULL DEFAULT '',
    request_id uuid NOT NULL,
    probe_owner uuid NOT NULL,
    probe_credential uuid NOT NULL,
    warned boolean NOT NULL DEFAULT false,
    mode text NOT NULL CHECK (mode IN ('observe', 'enforce'))
);
CREATE TABLE quality.health_events (
    endpoint_id uuid NOT NULL,
    epoch bigint NOT NULL,
    at bigint NOT NULL,
    kind text NOT NULL,
    cause text NOT NULL,
    failures integer NOT NULL,
    sources integer NOT NULL,
    request_id uuid NOT NULL,
    occurrences integer NOT NULL DEFAULT 1,
    signal_epoch bigint NOT NULL,
    signal_kind text NOT NULL,
    mode text NOT NULL,
    PRIMARY KEY (endpoint_id, epoch)
);
CREATE INDEX health_events_at ON quality.health_events (at);
ALTER TABLE quality.endpoint_health ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE quality.health_events ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform ON quality.endpoint_health USING ((SELECT platform.in_platform_scope()));
CREATE POLICY platform ON quality.health_events USING ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, UPDATE ON quality.endpoint_health TO axiom_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON quality.health_events TO axiom_app;

-- +goose Down
DROP TABLE quality.health_events;
DROP TABLE quality.endpoint_health;
