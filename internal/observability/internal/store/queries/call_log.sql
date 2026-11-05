-- name: InsertCallLogs :execrows
-- One statement writes a whole batch: the rows arrive as one JSON array whose members carry the column names, and the
-- table's own types and checks refuse a malformed value. A row that is already there, from a write whose outcome
-- was unknown and is retried, is left as it is, except that a settled record completes a pending one: it writes the
-- settlement's columns, which the pending row was written without (README「写入」). The batch holds each call once.
INSERT INTO observability.call_log (id, at, site_id, account_id, workspace_id, key_id, end_user_hash, ip, client_app,
    session_id, subagent_id, app_referer, app_title, app_categories, request_class, language, used_tools, has_image,
    has_file, model, endpoint_id, source_class, egress, attempt_count, route, usage, service_tier, price_version, price_nano,
    cost_nano, currency, free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind, consent_version,
    training_allowed, streamed, cache_hit, physical_input, billing_pending)
SELECT (r ->> 'id')::uuid, (r ->> 'at')::timestamptz, (r ->> 'site_id')::uuid, (r ->> 'account_id')::uuid,
    (r ->> 'workspace_id')::uuid, (r ->> 'key_id')::uuid, r ->> 'end_user_hash', (r ->> 'ip')::inet,
    r ->> 'client_app', r ->> 'session_id', r ->> 'subagent_id', r ->> 'app_referer', r ->> 'app_title',
    r ->> 'app_categories', r ->> 'request_class', r ->> 'language',
    (r ->> 'used_tools')::boolean, (r ->> 'has_image')::boolean, (r ->> 'has_file')::boolean, r ->> 'model',
    (r ->> 'endpoint_id')::uuid, r ->> 'source_class', r ->> 'egress', (r ->> 'attempt_count')::smallint,
    (r ->> 'route')::jsonb, r -> 'usage', r ->> 'service_tier',
    nullif(r ->> 'price_version', '')::uuid, (r ->> 'price_nano')::bigint, (r ->> 'cost_nano')::bigint, r ->> 'currency', (r ->> 'free_of_charge')::boolean,
    (r ->> 'latency_ms')::integer, (r ->> 'first_token_ms')::integer, r ->> 'finish_reason', r ->> 'error_kind',
    r ->> 'consent_version', (r ->> 'training_allowed')::boolean, (r ->> 'streamed')::boolean,
    coalesce((r ->> 'cache_hit')::boolean, false), (r ->> 'physical_input')::bigint,
    coalesce((r ->> 'billing_pending')::boolean, false)
FROM jsonb_array_elements(@rows::jsonb) AS r
ON CONFLICT (id, at) DO UPDATE SET usage = excluded.usage, service_tier = excluded.service_tier,
    price_version = excluded.price_version, price_nano = excluded.price_nano,
    free_of_charge = excluded.free_of_charge, physical_input = excluded.physical_input, billing_pending = false
WHERE call_log.billing_pending AND NOT excluded.billing_pending;

-- name: ListOwnCalls :many
-- Custom plans fold absent filters away and select an ordered index for the remaining filters.
-- Keep the row upper bound before the scalar window: PostgreSQL 18 uses the first as its
-- B-tree start key even when EXPLAIN lists both in Index Cond (README.md「读」, #408).
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, app_referer, app_title, app_categories, request_class, model, usage::text AS usage,
    service_tier, coalesce(price_version::text, '')::text AS price_version, price_nano, currency,
    free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind, streamed, cache_hit, physical_input
FROM observability.call_log
WHERE account_id = (@account_id::text)::uuid
    AND (at, id) < (@after_at::timestamptz, (@after_id::text)::uuid)
    AND at >= @since AND at < @until
    AND (@model::text = '' OR model = @model)
    AND (@client_app::text = '' OR client_app = @client_app)
    AND (@key_id::text = '' OR key_id = nullif(@key_id, '')::uuid)
    AND (@workspace_id::text = '' OR workspace_id = nullif(@workspace_id, '')::uuid)
    AND (@status::text = '' OR (error_kind IS DISTINCT FROM '') = (@status = 'error'))
    AND (@request_id::text = '' OR id = nullif(@request_id, '')::uuid)
-- Qualified names preserve UUID index order rather than sorting the output's id::text.
ORDER BY call_log.at DESC, call_log.id DESC
LIMIT @page_size;

-- name: CountOwnExportCalls :one
-- Count only a bounded ordered prefix, without loading call metadata into the export's memory.
SELECT count(*)::int AS count FROM (
    SELECT at, id
    FROM observability.call_log
    WHERE account_id = (@account_id::text)::uuid
        AND (at, id) < (@after_at::timestamptz, (@after_id::text)::uuid)
        AND at >= @since AND at < @until
        AND (@model::text = '' OR model = @model)
        AND (@client_app::text = '' OR client_app = @client_app)
        AND (@key_id::text = '' OR key_id = nullif(@key_id, '')::uuid)
        AND (@workspace_id::text = '' OR workspace_id = nullif(@workspace_id, '')::uuid)
        AND (@status::text = '' OR (error_kind IS DISTINCT FROM '') = (@status = 'error'))
        AND (@request_id::text = '' OR id = nullif(@request_id, '')::uuid)
    ORDER BY call_log.at DESC, call_log.id DESC
    LIMIT @row_limit
) AS bounded_calls;

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
    AND (@status::text = '' OR (error_kind IS DISTINCT FROM '') = (@status = 'error'))
    AND (@request_id::text = '' OR id = nullif(@request_id, '')::uuid)
GROUP BY bucket, currency
ORDER BY bucket, currency
LIMIT @group_limit;

-- name: GetOwnCall :one
SELECT id::text AS id, at, workspace_id::text AS workspace_id, key_id::text AS key_id, host(ip) AS ip, client_app,
    session_id, subagent_id, app_referer, app_title, app_categories, request_class, model, usage::text AS usage,
    service_tier, coalesce(price_version::text, '')::text AS price_version, price_nano, currency,
    free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind, streamed, cache_hit, physical_input
FROM observability.call_log
WHERE id = (@id::text)::uuid;

-- name: GetCallTrace :one
-- One call in full, for the chain read (README.md「追溯」): route included, and its request ID is the key the attempts,
-- the ledger and the supplier charges are read by. A call that has not been written yet is no rows, and the scope's
-- row-level security decides which calls exist for the reader.
SELECT id::text AS id, at, site_id::text AS site_id, account_id::text AS account_id, workspace_id::text AS workspace_id,
    key_id::text AS key_id, end_user_hash, host(ip) AS ip, client_app, session_id, subagent_id, app_referer, app_title,
    app_categories, request_class, language, used_tools, has_image, has_file, model, route::text AS route,
    usage::text AS usage, service_tier,
    coalesce(price_version::text, '')::text AS price_version, price_nano, cost_nano, currency, free_of_charge, latency_ms, first_token_ms, finish_reason, error_kind,
    consent_version, training_allowed
FROM observability.call_log
WHERE id = (@id::text)::uuid;

-- name: ActivityOwn :many
-- One row per UTC day, model and, when asked, workspace: the sums OpenRouter's /activity reports. The price is the
-- user's sale charge; the upstream's cost is not in this table's scope. A legacy row without a count contributes 0.
SELECT to_char(at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS date, model,
    count(*)::bigint AS requests,
    coalesce(sum(
        CASE WHEN physical_input >= 0 THEN physical_input ELSE
            coalesce((usage ->> 'input')::bigint, 0) + coalesce((usage ->> 'cache_read')::bigint, 0) +
            coalesce((usage ->> 'cache_write')::bigint, 0) + coalesce((usage ->> 'cache_write_1h')::bigint, 0) +
            coalesce((usage ->> 'audio_in')::bigint, 0) + coalesce((usage ->> 'image_in')::bigint, 0)
        END), 0)::bigint AS prompt_tokens,
    coalesce(sum(
        coalesce((usage ->> 'output')::bigint, 0) + coalesce((usage ->> 'reasoning')::bigint, 0) +
        coalesce((usage ->> 'audio_out')::bigint, 0) + coalesce((usage ->> 'image_out')::bigint, 0)), 0)::bigint AS completion_tokens,
    coalesce(sum(coalesce((usage ->> 'reasoning')::bigint, 0)), 0)::bigint AS reasoning_tokens,
    coalesce(sum(coalesce((usage ->> 'cache_read')::bigint, 0)), 0)::bigint AS cached_tokens,
    coalesce(sum(price_nano), 0)::bigint AS usage_nano,
    array_agg(id ORDER BY id)::uuid[] AS request_ids,
    array_agg(price_nano ORDER BY id)::bigint[] AS request_prices,
    CASE WHEN @group_workspace::boolean THEN workspace_id::text ELSE '' END AS workspace_id
FROM observability.call_log
WHERE account_id = (@account_id::text)::uuid
    AND at >= @since AND at < @until
    AND (@workspace_id::text = '' OR workspace_id = nullif(@workspace_id, '')::uuid)
    AND (@key_id::text = '' OR key_id = nullif(@key_id, '')::uuid)
GROUP BY date, model, CASE WHEN @group_workspace::boolean THEN workspace_id::text ELSE '' END
ORDER BY date, model, workspace_id;
