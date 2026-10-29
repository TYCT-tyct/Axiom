-- name: InsertCallLogs :execrows
-- One statement writes a whole batch: the rows arrive as one JSON array whose members carry the column names, and the
-- table's own types and checks refuse a malformed value. A row that is already there, from a write whose outcome
-- was unknown and is retried, is left as it is.
INSERT INTO observability.call_log (id, at, site_id, account_id, workspace_id, key_id, end_user_hash, ip, client_app,
    session_id, subagent_id, request_class, language, used_tools, has_image, has_file, model, endpoint_id, source_class,
    egress, attempt_count, route, usage, service_tier, price_version, price_nano, cost_nano, currency, free_of_charge,
    latency_ms, first_token_ms, finish_reason, error_kind, consent_version, training_allowed)
SELECT (r ->> 'id')::uuid, (r ->> 'at')::timestamptz, (r ->> 'site_id')::uuid, (r ->> 'account_id')::uuid,
    (r ->> 'workspace_id')::uuid, (r ->> 'key_id')::uuid, r ->> 'end_user_hash', (r ->> 'ip')::inet,
    r ->> 'client_app', r ->> 'session_id', r ->> 'subagent_id', r ->> 'request_class', r ->> 'language',
    (r ->> 'used_tools')::boolean, (r ->> 'has_image')::boolean, (r ->> 'has_file')::boolean, r ->> 'model',
    (r ->> 'endpoint_id')::uuid, r ->> 'source_class', r ->> 'egress', (r ->> 'attempt_count')::smallint,
    (r ->> 'route')::jsonb, r -> 'usage', r ->> 'service_tier',
    nullif(r ->> 'price_version', '')::uuid, (r ->> 'price_nano')::bigint, (r ->> 'cost_nano')::bigint, r ->> 'currency', (r ->> 'free_of_charge')::boolean,
    (r ->> 'latency_ms')::integer, (r ->> 'first_token_ms')::integer, r ->> 'finish_reason', r ->> 'error_kind',
    r ->> 'consent_version', (r ->> 'training_allowed')::boolean
FROM jsonb_array_elements(@rows::jsonb) AS r
ON CONFLICT (id, at) DO NOTHING;

-- name: ListOwnCalls :many
-- Custom plans fold absent filters away and select an ordered index for the remaining filters.
-- Keep the row upper bound before the scalar window: PostgreSQL 18 uses the first as its
-- B-tree start key even when EXPLAIN lists both in Index Cond (README.md「读」, #408).
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, request_class, model, usage::text AS usage, service_tier, coalesce(price_version::text, '')::text AS price_version, price_nano, currency,
    free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind
FROM observability.call_log
WHERE account_id = (@account_id::text)::uuid
    AND (at, id) < (@after_at::timestamptz, (@after_id::text)::uuid)
    AND at >= @since AND at < @until
    AND (@model::text = '' OR model = @model)
    AND (@client_app::text = '' OR client_app = @client_app)
    AND (@key_id::text = '' OR key_id = nullif(@key_id, '')::uuid)
    AND (@workspace_id::text = '' OR workspace_id = nullif(@workspace_id, '')::uuid)
    AND (@status::text = '' OR (error_kind <> '') = (@status = 'error'))
    AND (@request_id::text = '' OR id = nullif(@request_id, '')::uuid)
-- Qualified names preserve UUID index order rather than sorting the output's id::text.
ORDER BY call_log.at DESC, call_log.id DESC
LIMIT @page_size;

-- name: AggregateOwnCalls :many
SELECT CASE @group_by::text
    WHEN 'day' THEN to_char(at AT TIME ZONE 'UTC', 'YYYY-MM-DD')
    WHEN 'model' THEN model
    WHEN 'key' THEN key_id::text
END::text AS bucket, currency, count(*)::text AS calls, sum(price_nano)::text AS price_nano
FROM observability.call_log
WHERE account_id = (@account_id::text)::uuid
    AND at >= @since AND at < @until
    AND (@model::text = '' OR model = @model)
    AND (@client_app::text = '' OR client_app = @client_app)
    AND (@key_id::text = '' OR key_id = nullif(@key_id, '')::uuid)
    AND (@workspace_id::text = '' OR workspace_id = nullif(@workspace_id, '')::uuid)
    AND (@status::text = '' OR (error_kind <> '') = (@status = 'error'))
    AND (@request_id::text = '' OR id = nullif(@request_id, '')::uuid)
GROUP BY bucket, currency
ORDER BY bucket, currency
LIMIT @group_limit;

-- name: GetOwnCall :one
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, request_class, model, usage::text AS usage, service_tier, coalesce(price_version::text, '')::text AS price_version, price_nano, currency,
    free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind
FROM observability.call_log
WHERE id = (@id::text)::uuid;

-- name: GetCallTrace :one
-- One call in full, for the chain read (README.md「追溯」): route included, and its request ID is the key the attempts,
-- the ledger and the supplier charges are read by. A call that has not been written yet is no rows, and the scope's
-- row-level security decides which calls exist for the reader.
SELECT id::text AS id, at, site_id::text AS site_id, account_id::text AS account_id, workspace_id::text AS workspace_id,
    key_id::text AS key_id, end_user_hash, host(ip) AS ip, client_app, session_id, subagent_id, request_class,
    language, used_tools, has_image, has_file, model, route::text AS route, usage::text AS usage, service_tier,
    coalesce(price_version::text, '')::text AS price_version, price_nano, cost_nano, currency, free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind,
    consent_version, training_allowed
FROM observability.call_log
WHERE id = (@id::text)::uuid;
