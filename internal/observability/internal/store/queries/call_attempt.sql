-- name: InsertCallAttempts :execrows
-- One statement writes every attempt of a batch: the rows arrive as one JSON array whose members carry the column
-- names, and the table's own types and checks refuse a malformed value. An attempt that is already there, from a write
-- whose outcome was unknown and is retried, is left as it is.
INSERT INTO observability.call_attempt (request_id, at, attempt_seq, site_id, account_id, endpoint_id, supplier_id,
    source_class, egress, outcome, error_kind, started_at, finished_at, first_token_ms)
SELECT (r ->> 'request_id')::uuid, (r ->> 'at')::timestamptz, (r ->> 'attempt_seq')::smallint,
    (r ->> 'site_id')::uuid, (r ->> 'account_id')::uuid, (r ->> 'endpoint_id')::uuid, (r ->> 'supplier_id')::uuid,
    r ->> 'source_class', r ->> 'egress', r ->> 'outcome', r ->> 'error_kind', (r ->> 'started_at')::timestamptz,
    (r ->> 'finished_at')::timestamptz, (r ->> 'first_token_ms')::integer
FROM jsonb_array_elements(@rows::jsonb) AS r
ON CONFLICT (request_id, at, attempt_seq) DO NOTHING;

-- name: ListTraceAttempts :many
-- Every attempt of a call, in the order they were made (README.md「追溯」). The call's start is given so only its month
-- partition is read, though the primary key's request_id prefix would find the rows without it.
SELECT request_id::text AS request_id, at, attempt_seq, site_id::text AS site_id, account_id::text AS account_id,
    endpoint_id::text AS endpoint_id, supplier_id::text AS supplier_id, source_class, egress, outcome, error_kind,
    started_at, finished_at, first_token_ms
FROM observability.call_attempt
WHERE request_id = (@request_id::text)::uuid AND at = @at
ORDER BY attempt_seq;
