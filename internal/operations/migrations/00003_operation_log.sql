-- +goose Up

-- Every call of the operations API, reads included (docs/domains/operations.md「审计」): the agent's replayable record of
-- what it asked, why and what it got, not the audit of who changed what. One row per request, written before the answer
-- leaves, so a call without its record is answered as failed. `input` is the request's path and query parameters as
-- canonical JSON, at most 8 KiB; `reason` is the agent's own words and is untrusted text, empty when a request without
-- one was refused. The columns the writes need
-- (operation, approval, reservation) arrive with them.
CREATE TABLE operations.operation_log (
    id uuid PRIMARY KEY,
    credential_id uuid NOT NULL,
    run_id text CHECK (run_id ~ '^[A-Za-z0-9._:-]{1,64}$'),
    tool text NOT NULL CHECK (tool ~ '^[a-z][a-z0-9_]{0,63}$'),
    reason text NOT NULL CHECK (char_length(reason) <= 1000),
    -- json, not jsonb: json keeps the text as written, so the database measures the same bytes RecordCall measured;
    -- jsonb's text adds spaces and would refuse an input the code let through.
    input json NOT NULL CHECK (octet_length(input::text) <= 8192),
    status smallint NOT NULL CHECK (status BETWEEN 100 AND 599),
    error_code text CHECK (error_code ~ '^[a-z][a-z0-9_]{0,63}$'),
    request_id text NOT NULL,
    started_at timestamptz NOT NULL,
    ended_at timestamptz NOT NULL CHECK (ended_at >= started_at)
);
-- A credential reads its own record newest first (IDs are UUIDv7).
CREATE INDEX operation_log_of_credential ON operations.operation_log (credential_id, id DESC);

-- Platform data: the operations listener writes and reads it in the platform scope; no tenant scope sees it.
ALTER TABLE operations.operation_log ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.operation_log FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.operation_log FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON operations.operation_log TO axiom_app;

-- +goose Down
DROP TABLE operations.operation_log;
