-- +goose Up

-- What the gateway's plugins did in the call: one object per plugin it ran (README.md「表」).
ALTER TABLE observability.call_log ADD COLUMN plugins jsonb DEFAULT '[]'::jsonb NOT NULL
    CONSTRAINT call_log_plugins_check CHECK (jsonb_typeof(plugins) = 'array' AND octet_length(plugins::text) <= 1024);

-- listAdminCalls with a plugin: few calls use one, so the time-ordered scan reads only those.
CREATE INDEX call_log_admin_plugin_time ON observability.call_log ((at AT TIME ZONE 'UTC') DESC, id DESC)
    WHERE plugins <> '[]'::jsonb;

-- The journal carries what the monitor rollups need as well; a pending record is journaled too, and the hourly usage
-- rollup leaves it out as before.
ALTER TABLE observability.usage_journal
    ADD COLUMN billing_pending boolean DEFAULT false NOT NULL,
    ADD COLUMN endpoint_id uuid DEFAULT '00000000-0000-0000-0000-000000000000' NOT NULL,
    ADD COLUMN error_kind text DEFAULT '' NOT NULL,
    ADD COLUMN attempt_count smallint DEFAULT 0 NOT NULL,
    ADD COLUMN latency_ms integer DEFAULT 0 NOT NULL,
    ADD COLUMN first_token_ms integer DEFAULT 0 NOT NULL,
    ADD COLUMN plugins jsonb DEFAULT '[]'::jsonb NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION observability.journal_usage() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO observability.usage_journal
        (site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano,
        billing_pending,endpoint_id,error_kind,attempt_count,latency_ms,first_token_ms,plugins)
    VALUES (NEW.site_id,NEW.account_id,NEW.workspace_id,NEW.key_id,NEW.model,NEW.at,NEW.currency,
        NEW.error_kind = '',NEW.usage,NEW.physical_input,NEW.price_nano,
        NEW.billing_pending,NEW.endpoint_id,NEW.error_kind,NEW.attempt_count,NEW.latency_ms,NEW.first_token_ms,NEW.plugins);
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- The operations monitor's rollups: one row per bucket (60 or 3600 seconds), last endpoint and error kind, and per
-- bucket, measure (1 first token, 2 completion), last endpoint and latency bin. Short-lived: the rollup deletes what
-- the monitor's windows no longer reach.
CREATE TABLE observability.monitor_counts (
    width integer NOT NULL CHECK (width IN (60, 3600)),
    bucket timestamptz NOT NULL,
    endpoint_id uuid NOT NULL,
    error_kind text NOT NULL,
    calls bigint NOT NULL,
    switched bigint NOT NULL,
    tokens numeric NOT NULL,
    PRIMARY KEY (width, bucket, endpoint_id, error_kind)
);

CREATE TABLE observability.monitor_latency (
    width integer NOT NULL CHECK (width IN (60, 3600)),
    bucket timestamptz NOT NULL,
    measure smallint NOT NULL CHECK (measure IN (1, 2)),
    endpoint_id uuid NOT NULL,
    bin smallint NOT NULL CHECK (bin >= 0),
    calls bigint NOT NULL,
    PRIMARY KEY (width, bucket, measure, endpoint_id, bin)
);

-- The dashboard's time to first token, by site and hour, kept like usage_hourly.
CREATE TABLE observability.first_token_hourly (
    site_id uuid NOT NULL,
    hour timestamptz NOT NULL,
    bin smallint NOT NULL CHECK (bin >= 0),
    calls bigint NOT NULL,
    PRIMARY KEY (site_id, hour, bin)
);

-- The plugins' use by hour; money is in the calls' currency, so each currency is its own row.
CREATE TABLE observability.plugin_hourly (
    hour timestamptz NOT NULL,
    plugin text NOT NULL,
    currency text NOT NULL,
    calls bigint NOT NULL,
    failures bigint NOT NULL,
    uses numeric NOT NULL,
    ms numeric NOT NULL,
    timed bigint NOT NULL,
    price numeric NOT NULL,
    PRIMARY KEY (hour, plugin, currency)
);

ALTER TABLE observability.monitor_counts OWNER TO axiom_owner;
ALTER TABLE observability.monitor_latency OWNER TO axiom_owner;
ALTER TABLE observability.first_token_hourly OWNER TO axiom_owner;
ALTER TABLE observability.plugin_hourly OWNER TO axiom_owner;

ALTER TABLE observability.monitor_counts ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.monitor_latency ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.first_token_hourly ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.plugin_hourly ENABLE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.monitor_counts FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.monitor_latency FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.first_token_hourly FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.plugin_hourly FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_only ON observability.monitor_counts USING (( SELECT platform.in_platform_scope() AS in_platform_scope));
CREATE POLICY platform_only ON observability.monitor_latency USING (( SELECT platform.in_platform_scope() AS in_platform_scope));
CREATE POLICY platform_only ON observability.first_token_hourly USING (( SELECT platform.in_platform_scope() AS in_platform_scope));
CREATE POLICY platform_only ON observability.plugin_hourly USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE observability.monitor_counts TO axiom_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE observability.monitor_latency TO axiom_app;
GRANT SELECT,INSERT,UPDATE ON TABLE observability.first_token_hourly TO axiom_app;
GRANT SELECT,INSERT,UPDATE ON TABLE observability.plugin_hourly TO axiom_app;

-- The dashboard reads one site's hours of usage_hourly, whose key leads with the account.
CREATE INDEX usage_hourly_site_hour ON observability.usage_hourly (site_id, hour);
