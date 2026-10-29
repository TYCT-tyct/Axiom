-- name: InsertCallLogs :execrows
-- One statement writes a whole batch: the rows arrive as one JSON array whose members carry the column names, and the
-- table's own types and checks refuse a malformed value. A row that is already there, from a write whose outcome
-- was unknown and is retried, is left as it is.
INSERT INTO observability.call_log (id, at, site_id, account_id, workspace_id, key_id, end_user_hash, ip, client_app,
    session_id, subagent_id, request_class, language, used_tools, has_image, has_file, model, endpoint_id, source_class,
    egress, attempt_count, route, usage, service_tier, price_nano, cost_nano, currency, free_of_charge,
    latency_ms, first_token_ms, finish_reason, error_kind, consent_version, training_allowed)
SELECT (r ->> 'id')::uuid, (r ->> 'at')::timestamptz, (r ->> 'site_id')::uuid, (r ->> 'account_id')::uuid,
    (r ->> 'workspace_id')::uuid, (r ->> 'key_id')::uuid, r ->> 'end_user_hash', (r ->> 'ip')::inet,
    r ->> 'client_app', r ->> 'session_id', r ->> 'subagent_id', r ->> 'request_class', r ->> 'language',
    (r ->> 'used_tools')::boolean, (r ->> 'has_image')::boolean, (r ->> 'has_file')::boolean, r ->> 'model',
    (r ->> 'endpoint_id')::uuid, r ->> 'source_class', r ->> 'egress', (r ->> 'attempt_count')::smallint,
    (r ->> 'route')::jsonb, r -> 'usage', r ->> 'service_tier',
    (r ->> 'price_nano')::bigint, (r ->> 'cost_nano')::bigint, r ->> 'currency', (r ->> 'free_of_charge')::boolean,
    (r ->> 'latency_ms')::integer, (r ->> 'first_token_ms')::integer, r ->> 'finish_reason', r ->> 'error_kind',
    r ->> 'consent_version', (r ->> 'training_allowed')::boolean
FROM jsonb_array_elements(@rows::jsonb) AS r
ON CONFLICT (id, at) DO NOTHING;

-- name: ListOwnCalls :many
-- What a user may read of the account's own calls: no cost, no endpoint, no routing, no ledger. The scope's row-level
-- security limits the rows to the account, and the explicit account condition lets the planner walk the index
-- (account_id, at DESC, id DESC) in order instead of filtering every account's rows; the keyset (at, id) pages
-- stably while new calls arrive. Keep the row upper bound before the scalar time bounds: PostgreSQL 18 cannot
-- compare their tightness and uses the first as the B-tree start key. Starting at until walks newer index leaves
-- even when EXPLAIN lists the cursor in Index Cond (README.md「读」, #408).
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, request_class, model, usage::text AS usage, service_tier, price_nano, currency,
    free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind
FROM observability.call_log
WHERE account_id = (@account_id::text)::uuid
    AND (at, id) < (@after_at::timestamptz, (@after_id::text)::uuid)
    AND at >= @since AND at < @until
-- Qualified: a bare id would sort by the output column id::text, which is not the index order.
ORDER BY call_log.at DESC, call_log.id DESC
LIMIT @page_size;

-- name: ListOwnCallsByModel :many
-- The same list for one model, read from the index (account_id, model, at DESC, id DESC). A client application may be
-- given as well; it is then checked on the rows of the model, which are the fewer.
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, request_class, model, usage::text AS usage, service_tier, price_nano, currency,
    free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind
FROM observability.call_log
WHERE account_id = (@account_id::text)::uuid AND model = @model
    AND (at, id) < (@after_at::timestamptz, (@after_id::text)::uuid)
    AND at >= @since AND at < @until
    AND (@client_app::text = '' OR client_app = @client_app)
ORDER BY call_log.at DESC, call_log.id DESC
LIMIT @page_size;

-- name: ListOwnCallsByApp :many
-- The same list for one client application, read from the index (account_id, client_app, at DESC, id DESC).
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, request_class, model, usage::text AS usage, service_tier, price_nano, currency,
    free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind
FROM observability.call_log
WHERE account_id = (@account_id::text)::uuid AND client_app = @client_app
    AND (at, id) < (@after_at::timestamptz, (@after_id::text)::uuid)
    AND at >= @since AND at < @until
ORDER BY call_log.at DESC, call_log.id DESC
LIMIT @page_size;

-- name: GetOwnCall :one
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, request_class, model, usage::text AS usage, service_tier, price_nano, currency,
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
    price_nano, cost_nano, currency, free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind,
    consent_version, training_allowed
FROM observability.call_log
WHERE id = (@id::text)::uuid;
