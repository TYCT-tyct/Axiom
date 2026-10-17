-- +goose Up

-- The egress of the final attempt. Every attempt keeps its own; this column saves a join for the common question
-- "which way did the call leave", and the writer derives it from the last attempt so the two cannot disagree.
ALTER TABLE observability.call_log
    ADD COLUMN egress text NOT NULL DEFAULT '' CHECK (char_length(egress) <= 64);

-- One row per attempt of a call, written by the same recorder in the same transaction as its call. It is separate from
-- the call's route JSON, which is routing's own record of why the attempts happened: this table is the typed, timed
-- account of what each attempt did, findable by (request_id, attempt_seq) together with suppliers' cost and
-- reconciliation key. No column holds a key, a credential or any part of the conversation.
--
-- Partitioned by `at`, the call's start, so retention drops an attempt's month together with its call's. The primary
-- key carries `at` for the partitioned unique index and makes a retried write harmless (ON CONFLICT DO NOTHING).
CREATE TABLE observability.call_attempt (
    request_id uuid NOT NULL,
    at timestamptz NOT NULL,
    attempt_seq smallint NOT NULL CHECK (attempt_seq BETWEEN 1 AND 3),
    -- Who, for row-level security; the call carries the same values.
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    -- Where it went: the endpoint that served or failed the attempt, its supplier, the class of supply, and the
    -- outbound route used, by name.
    endpoint_id uuid NOT NULL CHECK (endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    supplier_id uuid NOT NULL CHECK (supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    source_class text NOT NULL DEFAULT '' CHECK (char_length(source_class) <= 64),
    egress text NOT NULL DEFAULT '' CHECK (char_length(egress) <= 64),
    -- What came of it. Committed: content reached the client. Failed: it did not. Unknown: the supplier may have run
    -- the request.
    outcome text NOT NULL CHECK (outcome IN ('committed', 'failed', 'unknown')),
    error_kind text NOT NULL DEFAULT '' CHECK (char_length(error_kind) <= 64),
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL CHECK (finished_at >= started_at),
    first_token_ms integer NOT NULL CHECK (first_token_ms >= 0),
    PRIMARY KEY (request_id, at, attempt_seq)
) PARTITION BY RANGE (at);

-- A tenant reads the attempts of its own account; the writer runs in the platform scope.
ALTER TABLE observability.call_attempt ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON observability.call_attempt USING (platform.in_scope(site_id, account_id));

-- Append only, like the call log.
GRANT SELECT, INSERT ON observability.call_attempt TO axiom_app;

-- The same months as the call log, and a default partition so an attempt is never refused for lack of one. Row-level
-- security is not inherited by partitions, so each one gets the policy. Retention moves and drops the months of both
-- tables together (internal/observability/README.md「分区」).
-- +goose StatementBegin
DO $$
DECLARE
    month date;
    part text;
BEGIN
    FOR month IN SELECT generate_series(date '2026-10-01', date '2028-12-01', interval '1 month')::date LOOP
        part := 'call_attempt_' || to_char(month, 'YYYY_MM');
        EXECUTE format('CREATE TABLE observability.%I PARTITION OF observability.call_attempt FOR VALUES FROM (%L) TO (%L)',
            part, month, (month + interval '1 month')::date);
        EXECUTE format('ALTER TABLE observability.%I ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY', part);
        EXECUTE format('CREATE POLICY tenant ON observability.%I USING (platform.in_scope(site_id, account_id))', part);
    END LOOP;
END
$$;
-- +goose StatementEnd

CREATE TABLE observability.call_attempt_default PARTITION OF observability.call_attempt DEFAULT;
ALTER TABLE observability.call_attempt_default ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON observability.call_attempt_default USING (platform.in_scope(site_id, account_id));
