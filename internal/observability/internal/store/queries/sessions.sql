-- name: ListOwnSessions :many
-- Filter before grouping: RLS and the account/time indexes bound every input, including default partitions.
WITH filtered AS MATERIALIZED (
    SELECT id, at, session_id, client_app, model, currency, price_nano
    FROM observability.call_log
    WHERE account_id = (@account_id::text)::uuid
        AND at >= @since AND at < @until
        AND session_id <> '' AND NOT billing_pending
        AND (@model::text = '' OR model = @model)
        AND (@client_app::text = '' OR client_app = @client_app)
        AND (@key_id::text = '' OR key_id = nullif(@key_id, '')::uuid)
        AND (@workspace_id::text = '' OR workspace_id = nullif(@workspace_id, '')::uuid)
        AND (@status::text = '' OR (error_kind IS DISTINCT FROM '') = (@status = 'error'))
        AND (@request_id::text = '' OR id = nullif(@request_id, '')::uuid)
        AND (NOT @fell_back::boolean OR attempt_count > 1)
), models AS (
    SELECT session_id, currency, model, count(*) AS calls, sum(price_nano) AS price,
        min(at) AS first_at
    FROM filtered GROUP BY session_id, currency, model
), totals AS (
    SELECT session_id, currency, sum(calls)::text AS calls, sum(price)::text AS price,
        min(first_at) AS first_at, (array_agg(model ORDER BY calls DESC, model COLLATE "C"))[1]::text AS main_model
    FROM models GROUP BY session_id, currency
), latest AS (
    SELECT DISTINCT ON (session_id, currency) session_id, currency, at AS last_at, id, client_app
    FROM filtered ORDER BY session_id, currency, at DESC, id DESC
)
SELECT totals.session_id, latest.client_app, totals.currency, totals.calls, totals.price, totals.main_model,
    totals.first_at::timestamptz AS first_at, latest.last_at, latest.id::text AS last_id
FROM totals JOIN latest USING (session_id, currency)
WHERE (latest.last_at, latest.id) < (@after_at::timestamptz, (@after_id::text)::uuid)
ORDER BY latest.last_at DESC, latest.id DESC
LIMIT @page_size::bigint;
