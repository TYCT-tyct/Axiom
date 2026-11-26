-- +goose Up

-- The journal names its call, so the monitor can read the call's attempts and tell an endpoint switch from a retry on
-- the same endpoint. A row journaled before this migration has none and goes only into usage_hourly: it was written
-- without the attempts' endpoints and, before 00050, without the error kind and timings
-- (docs/domains/observability.md「监控汇总」).
ALTER TABLE observability.usage_journal ADD COLUMN request_id uuid;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION observability.journal_usage() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO observability.usage_journal
        (site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano,
        billing_pending,endpoint_id,error_kind,attempt_count,latency_ms,first_token_ms,plugins,request_id)
    VALUES (NEW.site_id,NEW.account_id,NEW.workspace_id,NEW.key_id,NEW.model,NEW.at,NEW.currency,
        NEW.error_kind = '',NEW.usage,NEW.physical_input,NEW.price_nano,
        NEW.billing_pending,NEW.endpoint_id,NEW.error_kind,NEW.attempt_count,NEW.latency_ms,NEW.first_token_ms,NEW.plugins,
        NEW.id);
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- A call's tokens: its prompt as the upstream counted it, which already holds the cached tokens once, then its output.
-- Without a reported prompt total the input-side items are disjoint and add up to it. This is catalog.Usage's
-- InputTotal plus OutputTotal, the rule protocol and billing use; TestCallTokensFollowTheUsageRule holds them together.
-- +goose StatementBegin
CREATE FUNCTION observability.call_tokens(usage jsonb, physical_input bigint) RETURNS numeric
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    AS $$
    SELECT CASE WHEN physical_input >= 0 THEN physical_input::numeric
            ELSE coalesce((usage->>'input')::numeric,0) + coalesce((usage->>'cache_read')::numeric,0)
                + coalesce((usage->>'cache_write')::numeric,0) + coalesce((usage->>'cache_write_1h')::numeric,0)
                + coalesce((usage->>'audio_in')::numeric,0) + coalesce((usage->>'image_in')::numeric,0) END
        + coalesce((usage->>'output')::numeric,0) + coalesce((usage->>'reasoning')::numeric,0)
        + coalesce((usage->>'audio_out')::numeric,0) + coalesce((usage->>'image_out')::numeric,0)
$$;
-- +goose StatementEnd
ALTER FUNCTION observability.call_tokens(jsonb, bigint) OWNER TO axiom_owner;

-- The hour's tokens by call_tokens. A row written before this migration, or by a node that does not know the column,
-- has none, and its readers fall back to adding up its kinds.
ALTER TABLE observability.usage_hourly ADD COLUMN tokens numeric;

-- The successful runs whose charge could not be told apart from the call's other per-use items; while an hour has any,
-- the plugin's revenue over it is unknown. A row written before this migration summed such a run as zero, so every
-- successful run in it is unknown.
ALTER TABLE observability.plugin_hourly ADD COLUMN unpriced bigint DEFAULT 0 NOT NULL;
UPDATE observability.plugin_hourly SET unpriced = calls - failures;

-- The monitor's rows counted a key retry on one endpoint as a switch and an overlapping cache twice in their tokens.
-- They cover at most the last two days, and the calls they came from are no longer told apart from the journal's, so
-- they are dropped rather than left to mix two meanings; the monitor fills again from the next calls.
DELETE FROM observability.monitor_counts;
DELETE FROM observability.monitor_latency;
