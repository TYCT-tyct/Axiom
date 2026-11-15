-- name: BackfillUsage :one
-- The expression and ordering match call_log_admin_time; the scalar bound also prunes old partitions.
WITH batch AS MATERIALIZED (
    SELECT c.id, c.at, c.site_id, c.account_id, c.workspace_id, c.key_id, c.model, c.currency,
        c.error_kind = '' AS success, c.usage, c.physical_input, c.price_nano, c.billing_pending
    FROM observability.call_log c
    WHERE c.at >= @after_at::timestamptz
        AND (c.at AT TIME ZONE 'UTC',c.id) > (timezone('UTC', @after_at::timestamptz),(@after_id::text)::uuid)
        AND (c.at AT TIME ZONE 'UTC',c.id) <= (
            SELECT until_at AT TIME ZONE 'UTC',until_id FROM observability.usage_backfill WHERE singleton)
        AND NOT c.usage_rollup_live
    ORDER BY c.at AT TIME ZONE 'UTC', c.id
    LIMIT @batch_size::int
), enqueued AS (
    INSERT INTO observability.usage_journal
        (site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano)
    SELECT site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano
    FROM batch WHERE NOT billing_pending
), last_row AS (
    SELECT at,id FROM batch ORDER BY at DESC,id DESC LIMIT 1
)
UPDATE observability.usage_backfill SET
    at = coalesce((SELECT at FROM last_row), usage_backfill.at),
    id = coalesce((SELECT id FROM last_row), usage_backfill.id),
    done = (SELECT count(*) FROM batch) < @batch_size::int
WHERE singleton
RETURNING done;

-- name: ConsumeUsageJournal :one
WITH batch AS (
    DELETE FROM observability.usage_journal
    WHERE id IN (SELECT id FROM observability.usage_journal ORDER BY id LIMIT @batch_size::int)
    RETURNING *
), rolled AS (
    INSERT INTO observability.usage_hourly AS h
        (site_id,account_id,hour,workspace_id,key_id,model,currency,requests,successes,
        input,output,cache_read,cache_write,cache_rate_requests,cache_rate_read,cache_rate_input,spend)
    SELECT site_id,account_id,date_trunc('hour',at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
        workspace_id,key_id,model,currency,count(*),count(*) FILTER (WHERE success),
        sum((usage->>'input')::numeric),sum((usage->>'output')::numeric),sum((usage->>'cache_read')::numeric),
        sum(CASE WHEN usage ? 'cache_write' OR usage ? 'cache_write_1h'
            THEN coalesce((usage->>'cache_write')::numeric,0) + coalesce((usage->>'cache_write_1h')::numeric,0) END),
        count(*) FILTER (WHERE usage ? 'cache_read' AND physical_input >= 0),
        coalesce(sum((usage->>'cache_read')::numeric) FILTER (WHERE physical_input >= 0),0),
        coalesce(sum(physical_input::numeric) FILTER (WHERE usage ? 'cache_read' AND physical_input >= 0),0),
        sum(price_nano::numeric)
    FROM batch
    GROUP BY site_id,account_id,date_trunc('hour',at AT TIME ZONE 'UTC'),workspace_id,key_id,model,currency
    ORDER BY account_id,date_trunc('hour',at AT TIME ZONE 'UTC'),site_id,workspace_id,key_id,model,currency
    ON CONFLICT (account_id,hour,site_id,workspace_id,key_id,model,currency) DO UPDATE SET
        requests=h.requests+excluded.requests,successes=h.successes+excluded.successes,
        input=CASE WHEN h.input IS NOT NULL OR excluded.input IS NOT NULL THEN coalesce(h.input,0)+coalesce(excluded.input,0) END,
        output=CASE WHEN h.output IS NOT NULL OR excluded.output IS NOT NULL THEN coalesce(h.output,0)+coalesce(excluded.output,0) END,
        cache_read=CASE WHEN h.cache_read IS NOT NULL OR excluded.cache_read IS NOT NULL THEN coalesce(h.cache_read,0)+coalesce(excluded.cache_read,0) END,
        cache_write=CASE WHEN h.cache_write IS NOT NULL OR excluded.cache_write IS NOT NULL THEN coalesce(h.cache_write,0)+coalesce(excluded.cache_write,0) END,
        cache_rate_requests=h.cache_rate_requests+excluded.cache_rate_requests,
        cache_rate_read=h.cache_rate_read+excluded.cache_rate_read,cache_rate_input=h.cache_rate_input+excluded.cache_rate_input,
        spend=h.spend+excluded.spend
    RETURNING 1
)
SELECT count(*)::int FROM batch;

-- name: ReadUsageStatistics :many
WITH selected AS (
    SELECT *, hour < @since::timestamptz AS previous,
        date_trunc(@granularity::text,hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket,
        CASE @group_by::text WHEN 'model' THEN model WHEN 'key' THEN key_id::text
            WHEN 'workspace' THEN workspace_id::text ELSE '' END::text AS group_name
    FROM observability.usage_hourly
    WHERE account_id = (@account_id::text)::uuid
        AND hour >= @start_at::timestamptz AND hour < @until::timestamptz
        AND (@model::text = '' OR model = @model)
        AND (@key_id::text = '' OR key_id = nullif(@key_id,'')::uuid)
        AND (@workspace_id::text = '' OR workspace_id = nullif(@workspace_id,'')::uuid)
)
SELECT previous, grouping(bucket)::int AS total,
    coalesce(bucket,'0001-01-01T00:00:00Z')::timestamptz AS bucket,
    coalesce(group_name,'')::text AS group_name, currency,
    sum(requests)::text AS requests,sum(successes)::text AS successes,sum(spend)::text AS spend,
    coalesce(sum(input)::text,'')::text AS input,coalesce(sum(output)::text,'')::text AS output,
    coalesce(sum(cache_read)::text,'')::text AS cache_read,coalesce(sum(cache_write)::text,'')::text AS cache_write,
    sum(cache_rate_requests)::text AS cache_rate_requests,
    sum(cache_rate_read)::text AS cache_rate_read,sum(cache_rate_input)::text AS cache_rate_input
FROM selected
GROUP BY GROUPING SETS ((previous,currency,bucket,group_name),(previous,currency))
HAVING NOT previous OR grouping(bucket)=1
ORDER BY previous,total DESC,bucket,group_name,currency
LIMIT @group_limit;
