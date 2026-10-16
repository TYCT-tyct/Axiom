-- +goose Up

GRANT USAGE ON SCHEMA observability TO axiom_app;

-- The call log: one row per request, written once when the request has ended and never changed (internal/observability/
-- README.md「调用记录」). The attempts, the ledger entries and the lost-request records live with their owners
-- (suppliers, billing) under the same request ID; this row names them. No column holds a key, a credential or any
-- part of the conversation: the schema test refuses such names, and every JSON column is bounded.
--
-- Partitioned by `at`, the request's start, so that retention drops a whole month instead of deleting rows. The
-- primary key carries `at` because a partitioned table's unique index must; the same request ID with the same `at`
-- is the same row, which makes a write that is retried after an unknown outcome harmless (ON CONFLICT DO NOTHING).
-- Optional text is '' and an unknown first-token time is 0, so no column is NULL and the generated code needs no
-- nullable types.
CREATE TABLE observability.call_log (
    id uuid NOT NULL,
    at timestamptz NOT NULL,
    -- Who. site_id and account_id stay on the row for row-level security (internal/platform/README.md「范围」).
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    -- A keyed hash of the end user's identifier (hex): brute-forcing it needs the key.
    end_user_hash text NOT NULL DEFAULT '' CHECK (end_user_hash ~ '^([0-9a-f]{64})?$'),
    ip inet NOT NULL,
    -- With what.
    client_app text NOT NULL DEFAULT '' CHECK (char_length(client_app) <= 64),
    session_id text NOT NULL DEFAULT '' CHECK (char_length(session_id) <= 128),
    subagent_id text NOT NULL DEFAULT '' CHECK (char_length(subagent_id) <= 128),
    request_class text NOT NULL CHECK (request_class ~ '^[a-z][a-z0-9_.]{0,63}$'),
    -- Write labels: fixed when the request arrives.
    language text NOT NULL DEFAULT '' CHECK (char_length(language) <= 16),
    used_tools boolean NOT NULL,
    has_image boolean NOT NULL,
    has_file boolean NOT NULL,
    -- Where it went. model is the public name the client asked for. endpoint_id is the endpoint that served the
    -- final attempt (suppliers' ID, no key); the nil UUID when no attempt was made.
    model text NOT NULL CHECK (char_length(model) BETWEEN 1 AND 200),
    endpoint_id uuid NOT NULL,
    source_class text NOT NULL DEFAULT '' CHECK (char_length(source_class) <= 64),
    attempt_count smallint NOT NULL CHECK (attempt_count >= 0),
    route jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(route) = 'object' AND octet_length(route::text) <= 8192),
    ledger_entry_ids uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(ledger_entry_ids) <= 16),
    -- What came of it. usage maps a billing item's name to its count; amounts are billing's smallest unit
    -- (a billionth of a quota unit) in the site's currency.
    usage jsonb NOT NULL CHECK (jsonb_typeof(usage) = 'object' AND octet_length(usage::text) <= 8192),
    service_tier text NOT NULL CHECK (service_tier IN ('standard', 'flex', 'priority', 'fast')),
    price_nano bigint NOT NULL CHECK (price_nano >= 0),
    cost_nano bigint NOT NULL CHECK (cost_nano >= 0),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    free_of_charge boolean NOT NULL,
    latency_ms integer NOT NULL CHECK (latency_ms >= 0),
    first_token_ms integer NOT NULL CHECK (first_token_ms >= 0),
    finish_reason text NOT NULL DEFAULT '' CHECK (char_length(finish_reason) <= 64),
    error_kind text NOT NULL DEFAULT '' CHECK (char_length(error_kind) <= 64),
    -- Rights, fixed at the time of the request and impossible to recover later.
    consent_version text NOT NULL CHECK (char_length(consent_version) BETWEEN 1 AND 64),
    training_allowed boolean NOT NULL,
    PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);

-- A tenant reads and writes the rows of its own account; the writer runs in the platform scope.
ALTER TABLE observability.call_log ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON observability.call_log USING (platform.in_scope(site_id, account_id));

CREATE INDEX call_log_account_at ON observability.call_log (account_id, at DESC, id DESC);

-- Append only: there is no UPDATE, DELETE or TRUNCATE for the application; retention is the owner's, by partition.
GRANT SELECT, INSERT ON observability.call_log TO axiom_app;

-- Monthly partitions for the first two years, and a default one so that a write is never refused for lack of a
-- partition (the call record must not be dropped). Row-level security is not inherited by partitions, and the
-- startup audit wants it on every table, so each one gets the same policy. Creating later partitions and moving or
-- dropping old ones is the retention job's (internal/observability/README.md「分区」).
-- +goose StatementBegin
DO $$
DECLARE
    month date;
    part text;
BEGIN
    FOR month IN SELECT generate_series(date '2026-10-01', date '2028-12-01', interval '1 month')::date LOOP
        part := 'call_log_' || to_char(month, 'YYYY_MM');
        EXECUTE format('CREATE TABLE observability.%I PARTITION OF observability.call_log FOR VALUES FROM (%L) TO (%L)',
            part, month, (month + interval '1 month')::date);
        EXECUTE format('ALTER TABLE observability.%I ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY', part);
        EXECUTE format('CREATE POLICY tenant ON observability.%I USING (platform.in_scope(site_id, account_id))', part);
    END LOOP;
END
$$;
-- +goose StatementEnd

CREATE TABLE observability.call_log_default PARTITION OF observability.call_log DEFAULT;
ALTER TABLE observability.call_log_default ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON observability.call_log_default USING (platform.in_scope(site_id, account_id));
