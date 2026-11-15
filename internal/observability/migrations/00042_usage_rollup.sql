-- +goose Up

-- The default changes only for future inserts. This separates the resumable historical scan from live events,
-- including inserts by old nodes and records whose request time precedes the backfill cursor.
ALTER TABLE observability.call_log ADD COLUMN usage_rollup_live boolean NOT NULL DEFAULT false;
ALTER TABLE observability.call_log ALTER COLUMN usage_rollup_live SET DEFAULT true;

CREATE TABLE observability.usage_hourly (
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    hour timestamptz NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    model text NOT NULL,
    currency text NOT NULL,
    requests numeric NOT NULL,
    successes numeric NOT NULL,
    input numeric, -- Non-cache input, including audio and image tokens.
    output numeric, -- Includes reasoning, audio and image tokens.
    cache_read numeric,
    cache_write numeric,
    cache_rate_requests numeric NOT NULL,
    cache_rate_read numeric NOT NULL,
    cache_rate_input numeric NOT NULL,
    spend numeric NOT NULL,
    PRIMARY KEY (account_id, hour, site_id, workspace_id, key_id, model, currency)
);
ALTER TABLE observability.usage_hourly ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON observability.usage_hourly FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]));
GRANT SELECT ON observability.usage_hourly TO axiom_app;
CREATE POLICY writer ON observability.usage_hourly FOR ALL USING ((SELECT platform.in_platform_scope()));
GRANT INSERT, UPDATE ON observability.usage_hourly TO axiom_app;

CREATE TABLE observability.usage_journal (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    model text NOT NULL,
    at timestamptz NOT NULL,
    currency text NOT NULL,
    success boolean NOT NULL,
    usage jsonb NOT NULL,
    physical_input bigint NOT NULL,
    price_nano bigint NOT NULL
);
ALTER TABLE observability.usage_journal ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.usage_journal USING ((SELECT platform.in_platform_scope()));
-- Preserve call_log's existing account-scoped INSERT permission. Only platform workers may read or consume events.
CREATE POLICY enqueue ON observability.usage_journal FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]));
GRANT SELECT, INSERT, DELETE ON observability.usage_journal TO axiom_app;
GRANT USAGE ON SEQUENCE observability.usage_journal_id_seq TO axiom_app;

CREATE TABLE observability.usage_backfill (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    at timestamptz NOT NULL DEFAULT '0001-01-01T00:00:00Z',
    id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    until_at timestamptz NOT NULL,
    until_id uuid NOT NULL,
    done boolean NOT NULL DEFAULT false
);
ALTER TABLE observability.usage_backfill ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reader ON observability.usage_backfill FOR SELECT USING ((SELECT platform.in_any_scope()));
CREATE POLICY writer ON observability.usage_backfill FOR ALL USING ((SELECT platform.in_platform_scope()));
GRANT SELECT, UPDATE ON observability.usage_backfill TO axiom_app;
INSERT INTO observability.usage_backfill(singleton,until_at,until_id)
SELECT true,coalesce(last.at,'0001-01-01T00:00:00Z'),
    coalesce(last.id,'00000000-0000-0000-0000-000000000000')::uuid
FROM (SELECT 1) seed LEFT JOIN LATERAL (
    SELECT at,id FROM observability.call_log ORDER BY at AT TIME ZONE 'UTC' DESC,id DESC LIMIT 1
) last ON true;

-- AFTER INSERT does not fire for ON CONFLICT DO NOTHING: recorder replay cannot enqueue a duplicate.
-- No SECURITY DEFINER: call records are already written in platform scope, as is the journal.
-- +goose StatementBegin
CREATE FUNCTION observability.journal_usage() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT NEW.billing_pending THEN
        INSERT INTO observability.usage_journal
            (site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano)
        VALUES (NEW.site_id,NEW.account_id,NEW.workspace_id,NEW.key_id,NEW.model,NEW.at,NEW.currency,
            NEW.error_kind = '',NEW.usage,NEW.physical_input,NEW.price_nano);
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER journal_usage AFTER INSERT ON observability.call_log
    FOR EACH ROW EXECUTE FUNCTION observability.journal_usage();

-- +goose Down
DROP TRIGGER journal_usage ON observability.call_log;
DROP FUNCTION observability.journal_usage();
DROP TABLE observability.usage_backfill, observability.usage_journal, observability.usage_hourly;
ALTER TABLE observability.call_log DROP COLUMN usage_rollup_live;
