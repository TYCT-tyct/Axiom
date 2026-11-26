-- name: BackfillUsage :one
-- The expression and ordering match call_log_admin_time; the scalar bound also prunes old partitions.
WITH batch AS MATERIALIZED (
    SELECT c.id, c.at, c.site_id, c.account_id, c.workspace_id, c.key_id, c.model, c.currency,
        c.error_kind = '' AS success, c.usage, c.physical_input, c.price_nano, c.billing_pending,
        c.endpoint_id, c.error_kind, c.attempt_count, c.latency_ms, c.first_token_ms, c.plugins
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
        (site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano,
        billing_pending,endpoint_id,error_kind,attempt_count,latency_ms,first_token_ms,plugins,request_id)
    SELECT site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano,
        billing_pending,endpoint_id,error_kind,attempt_count,latency_ms,first_token_ms,plugins,id
    FROM batch
    RETURNING 1
), last_row AS (
    SELECT at,id FROM batch ORDER BY at DESC,id DESC LIMIT 1
)
UPDATE observability.usage_backfill SET
    at = coalesce((SELECT at FROM last_row), usage_backfill.at),
    id = coalesce((SELECT id FROM last_row), usage_backfill.id),
    done = (SELECT count(*) FROM batch) < @batch_size::int
WHERE singleton
RETURNING done, (SELECT count(*)::int FROM enqueued) AS enqueued;

-- name: ConsumeUsageJournal :one
WITH batch AS (
    DELETE FROM observability.usage_journal
    WHERE id IN (SELECT id FROM observability.usage_journal ORDER BY id LIMIT @batch_size::int)
    RETURNING *
), rolled AS (
    INSERT INTO observability.usage_hourly AS h
        (site_id,account_id,hour,workspace_id,key_id,model,currency,requests,successes,
        input,output,cache_read,cache_write,cache_rate_requests,cache_rate_read,cache_rate_input,spend,tokens)
    SELECT site_id,account_id,date_trunc('hour',at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
        workspace_id,key_id,model,currency,count(*),count(*) FILTER (WHERE success),
        sum(CASE WHEN usage ? 'input' OR usage ? 'audio_in' OR usage ? 'image_in'
            THEN coalesce((usage->>'input')::numeric,0) + coalesce((usage->>'audio_in')::numeric,0)
                + coalesce((usage->>'image_in')::numeric,0) END),
        sum(CASE WHEN usage ? 'output' OR usage ? 'reasoning' OR usage ? 'audio_out' OR usage ? 'image_out'
            THEN coalesce((usage->>'output')::numeric,0) + coalesce((usage->>'reasoning')::numeric,0)
                + coalesce((usage->>'audio_out')::numeric,0) + coalesce((usage->>'image_out')::numeric,0) END),
        sum((usage->>'cache_read')::numeric),
        sum(CASE WHEN usage ? 'cache_write' OR usage ? 'cache_write_1h'
            THEN coalesce((usage->>'cache_write')::numeric,0) + coalesce((usage->>'cache_write_1h')::numeric,0) END),
        count(*) FILTER (WHERE usage ? 'cache_read' AND physical_input >= 0),
        coalesce(sum((usage->>'cache_read')::numeric) FILTER (WHERE physical_input >= 0),0),
        coalesce(sum(physical_input::numeric) FILTER (WHERE usage ? 'cache_read' AND physical_input >= 0),0),
        sum(price_nano::numeric), sum(observability.call_tokens(usage, physical_input))
    FROM batch
    WHERE NOT billing_pending -- a pending call's usage and price were never known (README「写入」)
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
        spend=h.spend+excluded.spend,
        tokens=h.tokens+excluded.tokens -- a row without them, from before 00051, keeps none (docs/domains/observability.md「监控汇总」)
    RETURNING 1
), calls AS (
    -- A call switched when one of its attempts was on another endpoint than the last; a retry with another key on the
    -- same endpoint is not a switch. The attempts were written in the call's own transaction. A row journaled before
    -- 00051 names no call and stays out of the monitor's rollups.
    SELECT b.at, b.site_id, b.endpoint_id, b.error_kind, b.latency_ms, b.first_token_ms, b.currency, b.plugins,
        b.attempt_count > 1 AND EXISTS (SELECT 1 FROM observability.call_attempt a
            WHERE a.request_id = b.request_id AND a.at = b.at AND a.endpoint_id <> b.endpoint_id) AS switched,
        observability.call_tokens(b.usage, b.physical_input) AS tokens
    FROM batch b
    WHERE b.request_id IS NOT NULL
), bucketed AS (
    -- The monitor's windows reach back an hour of minutes and a day of hours; a record older than its width's keep
    -- (a spill replayed after an outage) is not rolled into it. PruneMonitor deletes on the same bounds.
    SELECT c.endpoint_id, c.error_kind, c.switched, c.latency_ms, c.first_token_ms, c.tokens, w AS width,
        date_bin(make_interval(secs => w), c.at, '1970-01-01T00:00:00Z'::timestamptz) AS bucket
    FROM calls c CROSS JOIN unnest(ARRAY[60, 3600]::int[]) AS w
    WHERE c.at >= now() - CASE w WHEN 60 THEN interval '2 hours' ELSE interval '48 hours' END
), counted AS (
    INSERT INTO observability.monitor_counts AS m (width,bucket,endpoint_id,error_kind,calls,switched,tokens)
    SELECT width, bucket, endpoint_id, error_kind, count(*), count(*) FILTER (WHERE switched), sum(tokens)
    FROM bucketed
    GROUP BY width, bucket, endpoint_id, error_kind
    ORDER BY width, bucket, endpoint_id, error_kind
    ON CONFLICT (width,bucket,endpoint_id,error_kind) DO UPDATE SET
        calls=m.calls+excluded.calls, switched=m.switched+excluded.switched, tokens=m.tokens+excluded.tokens
    RETURNING 1
), timed AS (
    -- Measure 1 is the time to first token of a call that had one, 2 the total time of a call that succeeded.
    INSERT INTO observability.monitor_latency AS m (width,bucket,measure,endpoint_id,bin,calls)
    SELECT b.width, b.bucket, v.measure, b.endpoint_id, width_bucket(v.ms, @bounds::int[])::smallint, count(*)
    FROM bucketed b CROSS JOIN LATERAL (VALUES (1::smallint, b.first_token_ms, b.first_token_ms > 0),
        (2::smallint, b.latency_ms, b.error_kind = '')) AS v(measure, ms, kept)
    WHERE v.kept
    GROUP BY 1, 2, 3, 4, 5
    ORDER BY 1, 2, 3, 4, 5
    ON CONFLICT (width,bucket,measure,endpoint_id,bin) DO UPDATE SET calls=m.calls+excluded.calls
    RETURNING 1
), first_tokens AS (
    INSERT INTO observability.first_token_hourly AS f (site_id,hour,bin,calls)
    SELECT site_id, date_trunc('hour',at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
        width_bucket(first_token_ms, @bounds::int[])::smallint, count(*)
    FROM calls WHERE first_token_ms > 0
    GROUP BY 1, 2, 3
    ORDER BY 1, 2, 3
    ON CONFLICT (site_id,hour,bin) DO UPDATE SET calls=f.calls+excluded.calls
    RETURNING 1
), plugin_runs AS (
    -- A successful run without a price is one whose charge the gateway could not tell apart (docs/domains/observability.md「监控汇总」).
    INSERT INTO observability.plugin_hourly AS p (hour,plugin,currency,calls,failures,uses,ms,timed,price,unpriced)
    SELECT date_trunc('hour',c.at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', r->>'plugin', c.currency, count(*),
        count(*) FILTER (WHERE NOT (r->>'ok')::boolean), coalesce(sum((r->>'uses')::numeric),0),
        coalesce(sum((r->>'ms')::numeric),0), count(r->'ms'), coalesce(sum((r->>'price_nano')::numeric),0),
        count(*) FILTER (WHERE (r->>'ok')::boolean AND NOT r ? 'price_nano')
    FROM calls c CROSS JOIN LATERAL jsonb_array_elements(c.plugins) AS r
    GROUP BY 1, 2, 3
    ORDER BY 1, 2, 3
    ON CONFLICT (hour,plugin,currency) DO UPDATE SET
        calls=p.calls+excluded.calls, failures=p.failures+excluded.failures, uses=p.uses+excluded.uses,
        ms=p.ms+excluded.ms, timed=p.timed+excluded.timed, price=p.price+excluded.price,
        unpriced=p.unpriced+excluded.unpriced
    RETURNING 1
)
SELECT count(*)::int FROM batch;

-- name: PruneMonitor :exec
-- The monitor's minute rows older than two hours and hour rows older than two days answer no window.
DELETE FROM observability.monitor_counts
WHERE (width = 60 AND bucket < now() - interval '2 hours') OR (width = 3600 AND bucket < now() - interval '48 hours');

-- name: PruneMonitorLatency :exec
DELETE FROM observability.monitor_latency
WHERE (width = 60 AND bucket < now() - interval '2 hours') OR (width = 3600 AND bucket < now() - interval '48 hours');

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
