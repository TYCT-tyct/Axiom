-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

CREATE TABLE quality.benchmark_baselines (
    model_id uuid NOT NULL,
    version text NOT NULL,
    hash text NOT NULL,
    format smallint NOT NULL,
    endpoint_id uuid NOT NULL,
    run_at bigint NOT NULL,
    scores jsonb NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT benchmark_baselines_format_check CHECK (((format >= 0) AND (format <= 5))),
    CONSTRAINT benchmark_baselines_hash_check CHECK ((hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT benchmark_baselines_run_at_check CHECK ((run_at > 0)),
    CONSTRAINT benchmark_baselines_scores_check CHECK ((jsonb_typeof(scores) = 'object'::text)),
    CONSTRAINT benchmark_baselines_version_check CHECK ((version ~ '^[a-z0-9][a-z0-9._-]{0,63}$'::text))
);



ALTER TABLE quality.benchmark_baselines OWNER TO axiom_owner;

CREATE TABLE quality.benchmark_configs (
    endpoint_id uuid NOT NULL,
    model_id uuid NOT NULL,
    upstream text NOT NULL,
    formats integer NOT NULL,
    epoch bigint DEFAULT 1 NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT benchmark_configs_epoch_check CHECK ((epoch > 0)),
    CONSTRAINT benchmark_configs_formats_check CHECK (((formats >= 0) AND (formats <= 63)))
);



ALTER TABLE quality.benchmark_configs OWNER TO axiom_owner;

CREATE TABLE quality.benchmark_scores (
    endpoint_id uuid NOT NULL,
    version text NOT NULL,
    hash text NOT NULL,
    model_id uuid NOT NULL,
    upstream text NOT NULL,
    formats integer NOT NULL,
    score double precision NOT NULL,
    confidence double precision NOT NULL,
    run_at bigint NOT NULL,
    baseline_at bigint NOT NULL,
    epoch bigint NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT benchmark_scores_baseline_at_check CHECK ((baseline_at > 0)),
    CONSTRAINT benchmark_scores_confidence_check CHECK (((confidence >= (0)::double precision) AND (confidence <= (1)::double precision))),
    CONSTRAINT benchmark_scores_epoch_check CHECK ((epoch > 0)),
    CONSTRAINT benchmark_scores_formats_check CHECK (((formats >= 0) AND (formats <= 63))),
    CONSTRAINT benchmark_scores_hash_check CHECK ((hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT benchmark_scores_run_at_check CHECK ((run_at > 0)),
    CONSTRAINT benchmark_scores_score_check CHECK (((score >= (0)::double precision) AND (score <= (100)::double precision))),
    CONSTRAINT benchmark_scores_version_check CHECK ((version ~ '^[a-z0-9][a-z0-9._-]{0,63}$'::text))
);



ALTER TABLE quality.benchmark_scores OWNER TO axiom_owner;

CREATE TABLE quality.endpoint_health (
    endpoint_id uuid NOT NULL,
    epoch bigint NOT NULL,
    opened_at bigint DEFAULT 0 NOT NULL,
    restored_at bigint DEFAULT 0 NOT NULL,
    next_probe_at bigint DEFAULT 0 NOT NULL,
    last_probe_at bigint DEFAULT 0 NOT NULL,
    probe_lease_until bigint DEFAULT 0 NOT NULL,
    backoff integer DEFAULT 0 NOT NULL,
    failures integer DEFAULT 0 NOT NULL,
    sources integer DEFAULT 0 NOT NULL,
    cause text DEFAULT ''::text NOT NULL,
    probe_result text DEFAULT ''::text NOT NULL,
    request_id uuid NOT NULL,
    probe_owner uuid NOT NULL,
    probe_credential uuid NOT NULL,
    warned boolean DEFAULT false NOT NULL,
    mode text NOT NULL,
    CONSTRAINT endpoint_health_backoff_check CHECK (((backoff >= 0) AND (backoff <= 10))),
    CONSTRAINT endpoint_health_epoch_check CHECK ((epoch >= 0)),
    CONSTRAINT endpoint_health_mode_check CHECK ((mode = ANY (ARRAY['observe'::text, 'enforce'::text])))
);



ALTER TABLE quality.endpoint_health OWNER TO axiom_owner;

CREATE TABLE quality.health_events (
    endpoint_id uuid NOT NULL,
    epoch bigint NOT NULL,
    at bigint NOT NULL,
    kind text NOT NULL,
    cause text NOT NULL,
    failures integer NOT NULL,
    sources integer NOT NULL,
    request_id uuid NOT NULL,
    occurrences integer DEFAULT 1 NOT NULL,
    signal_epoch bigint NOT NULL,
    signal_kind text NOT NULL,
    mode text NOT NULL
);



ALTER TABLE quality.health_events OWNER TO axiom_owner;

CREATE TABLE quality.probe_settings (
    singleton boolean DEFAULT true NOT NULL,
    hourly_budget bigint,
    concurrency integer,
    verify_every_seconds integer,
    verify_max_age_seconds integer,
    verify_retry_seconds integer,
    probe_base_seconds integer,
    CONSTRAINT probe_settings_check CHECK (((verify_every_seconds IS NULL) OR (verify_max_age_seconds IS NULL) OR (verify_max_age_seconds >= verify_every_seconds))),
    CONSTRAINT probe_settings_check1 CHECK (((verify_every_seconds IS NULL) OR (verify_retry_seconds IS NULL) OR (verify_retry_seconds <= verify_every_seconds))),
    CONSTRAINT probe_settings_concurrency_check CHECK (((concurrency >= 1) AND (concurrency <= 64))),
    CONSTRAINT probe_settings_hourly_budget_check CHECK (((hourly_budget >= 1) AND (hourly_budget <= 100000))),
    CONSTRAINT probe_settings_probe_base_seconds_check CHECK (((probe_base_seconds >= 1) AND (probe_base_seconds <= 600))),
    CONSTRAINT probe_settings_singleton_check CHECK (singleton),
    CONSTRAINT probe_settings_verify_every_seconds_check CHECK (((verify_every_seconds >= 60) AND (verify_every_seconds <= 2592000))),
    CONSTRAINT probe_settings_verify_max_age_seconds_check CHECK (((verify_max_age_seconds >= 60) AND (verify_max_age_seconds <= 2592000))),
    CONSTRAINT probe_settings_verify_retry_seconds_check CHECK (((verify_retry_seconds >= 60) AND (verify_retry_seconds <= 86400)))
);



ALTER TABLE quality.probe_settings OWNER TO axiom_owner;

CREATE TABLE quality.source_classes (
    slug text NOT NULL,
    name text NOT NULL,
    name_zh text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    "position" integer NOT NULL,
    fallback boolean DEFAULT false NOT NULL,
    system boolean DEFAULT false NOT NULL,
    rules jsonb DEFAULT '{"match": "all", "conditions": []}'::jsonb NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    CONSTRAINT source_classes_check CHECK (((NOT fallback) OR (COALESCE(jsonb_array_length((rules -> 'conditions'::text)), 0) = 0))),
    CONSTRAINT source_classes_description_check CHECK ((length(description) <= 500)),
    CONSTRAINT source_classes_name_check CHECK (((length(name) >= 1) AND (length(name) <= 100))),
    CONSTRAINT source_classes_name_zh_check CHECK ((length(name_zh) <= 100)),
    CONSTRAINT source_classes_position_check CHECK ((("position" >= 0) AND ("position" <= 1000000))),
    CONSTRAINT source_classes_revision_check CHECK ((revision > 0)),
    CONSTRAINT source_classes_rules_check CHECK ((jsonb_typeof(rules) = 'object'::text)),
    CONSTRAINT source_classes_slug_check CHECK ((slug ~ '^[a-z][a-z0-9-]{0,31}$'::text))
);



ALTER TABLE quality.source_classes OWNER TO axiom_owner;

INSERT INTO quality.source_classes (slug, name, name_zh, description, "position", fallback, system, rules, revision, created_at, updated_at, deleted_at) VALUES ('official', 'Official', '官方直连', '', 0, false, true, '{"match": "all", "conditions": []}', 1, now(), now(), NULL);
INSERT INTO quality.source_classes (slug, name, name_zh, description, "position", fallback, system, rules, revision, created_at, updated_at, deleted_at) VALUES ('cloud', 'Cloud', '云厂商', '', 10, false, true, '{"match": "all", "conditions": []}', 1, now(), now(), NULL);
INSERT INTO quality.source_classes (slug, name, name_zh, description, "position", fallback, system, rules, revision, created_at, updated_at, deleted_at) VALUES ('relay', 'Relay', '中转站', '', 1000, true, true, '{"match": "all", "conditions": []}', 1, now(), now(), NULL);

ALTER TABLE ONLY quality.benchmark_baselines
    ADD CONSTRAINT benchmark_baselines_pkey PRIMARY KEY (model_id, version);

ALTER TABLE ONLY quality.benchmark_configs
    ADD CONSTRAINT benchmark_configs_pkey PRIMARY KEY (endpoint_id);

ALTER TABLE ONLY quality.benchmark_scores
    ADD CONSTRAINT benchmark_scores_pkey PRIMARY KEY (endpoint_id, version);

ALTER TABLE ONLY quality.endpoint_health
    ADD CONSTRAINT endpoint_health_pkey PRIMARY KEY (endpoint_id);

ALTER TABLE ONLY quality.health_events
    ADD CONSTRAINT health_events_pkey PRIMARY KEY (endpoint_id, epoch);

ALTER TABLE ONLY quality.probe_settings
    ADD CONSTRAINT probe_settings_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY quality.source_classes
    ADD CONSTRAINT source_classes_pkey PRIMARY KEY (slug);

CREATE INDEX health_events_at ON quality.health_events USING btree (at);

CREATE UNIQUE INDEX source_classes_fallback ON quality.source_classes USING btree ((true)) WHERE (fallback AND (deleted_at IS NULL));

CREATE UNIQUE INDEX source_classes_position ON quality.source_classes USING btree ("position") WHERE (deleted_at IS NULL);






CREATE POLICY platform ON quality.benchmark_baselines USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform ON quality.benchmark_configs USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform ON quality.benchmark_scores USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform ON quality.endpoint_health USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform ON quality.health_events USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform ON quality.probe_settings USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform ON quality.source_classes USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));



GRANT USAGE ON SCHEMA quality TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE quality.benchmark_baselines TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE quality.benchmark_configs TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE quality.benchmark_scores TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE quality.endpoint_health TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE quality.health_events TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE quality.probe_settings TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE quality.source_classes TO axiom_app;


ALTER TABLE ONLY quality.benchmark_baselines FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY quality.benchmark_configs FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY quality.benchmark_scores FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY quality.endpoint_health FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY quality.health_events FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY quality.probe_settings FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY quality.source_classes FORCE ROW LEVEL SECURITY;
ALTER TABLE quality.benchmark_baselines ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality.benchmark_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality.benchmark_scores ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality.endpoint_health ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality.health_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality.probe_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality.source_classes ENABLE ROW LEVEL SECURITY;
