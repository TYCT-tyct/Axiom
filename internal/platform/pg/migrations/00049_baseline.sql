-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;
GRANT CREATE ON SCHEMA platform TO axiom_lookup;

-- +goose StatementBegin
CREATE FUNCTION platform.allowed_accounts() RETURNS uuid[]
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN COALESCE((NULLIF(current_setting('axiom.scope_accounts'::text, true), ''::text))::uuid[], '{}'::uuid[]);


ALTER FUNCTION platform.allowed_accounts() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.allowed_sites() RETURNS uuid[]
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN COALESCE((NULLIF(current_setting('axiom.scope_sites'::text, true), ''::text))::uuid[], '{}'::uuid[]);


ALTER FUNCTION platform.allowed_sites() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.fence_envelope_key_version() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE
    key_version bigint := (to_jsonb(NEW) ->> TG_ARGV[0])::bigint;
BEGIN
    IF key_version IS NULL THEN
        RETURN NEW;
    END IF;
    -- Held to the end of this transaction. A retirement holds it exclusively until its tombstone and marker commit, so
    -- this write either commits first (and the retirement's reference check sees it) or waits here and is refused.
    PERFORM pg_advisory_xact_lock_shared(321, 9);
    IF EXISTS (SELECT 1 FROM platform.key_retirements r WHERE r.version = key_version) THEN
        RAISE EXCEPTION 'key version % is retired', key_version USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;


ALTER FUNCTION platform.fence_envelope_key_version() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.home_site() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN (NULLIF(current_setting('axiom.scope_home'::text, true), ''::text))::uuid;


ALTER FUNCTION platform.home_site() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.in_platform_scope() RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN (COALESCE(current_setting('axiom.scope_platform'::text, true), ''::text) = 'on'::text);


ALTER FUNCTION platform.in_platform_scope() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.in_any_scope() RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN (platform.in_platform_scope() OR (COALESCE(cardinality((NULLIF(current_setting('axiom.scope_sites'::text, true), ''::text))::uuid[]), 0) > 0) OR (COALESCE(cardinality((NULLIF(current_setting('axiom.scope_accounts'::text, true), ''::text))::uuid[]), 0) > 0));


ALTER FUNCTION platform.in_any_scope() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.in_scope(site uuid, account uuid) RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN ((COALESCE(current_setting('axiom.scope_platform'::text, true), ''::text) = 'on'::text) OR (site = ANY (COALESCE((NULLIF(current_setting('axiom.scope_sites'::text, true), ''::text))::uuid[], '{}'::uuid[]))) OR (account = ANY (COALESCE((NULLIF(current_setting('axiom.scope_accounts'::text, true), ''::text))::uuid[], '{}'::uuid[]))));


ALTER FUNCTION platform.in_scope(site uuid, account uuid) OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.mail_tenant(id uuid) RETURNS TABLE(site_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT m.site_id, m.account_id FROM platform.mail_outbox m WHERE m.id = mail_tenant.id
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.sees_site(site uuid) RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN COALESCE((platform.in_scope(site, NULL::uuid) OR (site = (NULLIF(current_setting('axiom.scope_home'::text, true), ''::text))::uuid)), false);


ALTER FUNCTION platform.sees_site(site uuid) OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE platform.alert_positions (
    source text NOT NULL,
    "position" jsonb DEFAULT '{}'::jsonb NOT NULL
);



ALTER TABLE platform.alert_positions OWNER TO axiom_owner;

CREATE TABLE platform.alert_signals (
    id uuid NOT NULL,
    seq bigint NOT NULL,
    body jsonb NOT NULL,
    quarantined boolean DEFAULT false NOT NULL,
    CONSTRAINT alert_signals_body_check CHECK ((octet_length((body)::text) <= 65536))
);



ALTER TABLE platform.alert_signals OWNER TO axiom_owner;

ALTER TABLE platform.alert_signals ALTER COLUMN seq ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME platform.alert_signals_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE platform.audit_log (
    id uuid DEFAULT uuidv7() NOT NULL,
    at timestamptz DEFAULT now() NOT NULL,
    actor_kind text NOT NULL,
    actor_id text NOT NULL,
    request_id uuid,
    action text NOT NULL,
    module text NOT NULL,
    target_kind text NOT NULL,
    target_id text NOT NULL,
    site_id uuid,
    account_id uuid,
    reason text,
    detail jsonb NOT NULL,
    tenant_id uuid GENERATED ALWAYS AS (COALESCE(account_id, site_id)) STORED,
    CONSTRAINT account_with_site CHECK (((account_id IS NULL) OR (site_id IS NOT NULL))),
    CONSTRAINT audit_log_action_check CHECK ((action ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'::text)),
    CONSTRAINT audit_log_actor_id_check CHECK ((actor_id ~ '^[a-z0-9][a-z0-9_.-]*$'::text)),
    CONSTRAINT audit_log_detail_check CHECK (((jsonb_typeof(detail) = 'object'::text) AND (octet_length((detail)::text) <= 8192))),
    CONSTRAINT audit_log_module_check CHECK ((module ~ '^[a-z]+$'::text)),
    CONSTRAINT audit_log_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 1000))),
    CONSTRAINT audit_log_target_id_check CHECK ((target_id <> ''::text)),
    CONSTRAINT audit_log_target_kind_check CHECK ((target_kind ~ '^[a-z][a-z0-9_]*$'::text))
);



ALTER TABLE platform.audit_log OWNER TO axiom_owner;

CREATE TABLE platform.billing_preference_readiness (
    id boolean DEFAULT true NOT NULL,
    enabled boolean NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT billing_preference_readiness_id_check CHECK (id)
);



ALTER TABLE platform.billing_preference_readiness OWNER TO axiom_owner;

CREATE TABLE platform.credential_share_departures (
    id text NOT NULL,
    share_view text[],
    expires_at timestamptz NOT NULL
);



ALTER TABLE platform.credential_share_departures OWNER TO axiom_owner;

CREATE TABLE platform.discarded_jobs (
    job_id bigint NOT NULL,
    kind text NOT NULL,
    attempt smallint NOT NULL,
    seen_at timestamptz DEFAULT now() NOT NULL,
    notified boolean DEFAULT false NOT NULL
);



ALTER TABLE platform.discarded_jobs OWNER TO axiom_owner;

CREATE TABLE platform.file_parser_config (
    id uuid NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    CONSTRAINT file_parser_config_id_check CHECK ((id = '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT file_parser_config_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT file_parser_config_nonce_check CHECK ((octet_length(nonce) = 12))
);



ALTER TABLE platform.file_parser_config OWNER TO axiom_owner;

CREATE TABLE platform.idempotency (
    key uuid NOT NULL,
    actor text NOT NULL,
    operation text NOT NULL,
    fingerprint bytea NOT NULL,
    response jsonb,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT idempotency_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT idempotency_fingerprint_check CHECK ((octet_length(fingerprint) = 32)),
    CONSTRAINT idempotency_operation_check CHECK ((operation ~ '^[A-Za-z][A-Za-z0-9]*$'::text))
);



ALTER TABLE platform.idempotency OWNER TO axiom_owner;

CREATE TABLE platform.key_retirements (
    version bigint NOT NULL,
    CONSTRAINT key_retirements_version_check CHECK (((version >= 1) AND (version <= '4294967295'::bigint)))
);



ALTER TABLE platform.key_retirements OWNER TO axiom_owner;

CREATE TABLE platform.key_versions (
    version bigint NOT NULL,
    fingerprint bytea NOT NULL,
    state text NOT NULL,
    CONSTRAINT key_versions_fingerprint_check CHECK ((octet_length(fingerprint) = 32)),
    CONSTRAINT key_versions_state_check CHECK ((state = ANY (ARRAY['readable'::text, 'active'::text, 'retired'::text]))),
    CONSTRAINT key_versions_version_check CHECK (((version >= 1) AND (version <= '4294967295'::bigint)))
);



ALTER TABLE platform.key_versions OWNER TO axiom_owner;

CREATE TABLE platform.mail_outbox (
    id uuid DEFAULT uuidv7() NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid,
    recipient text NOT NULL,
    subject text NOT NULL,
    text_body text NOT NULL,
    html_body text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT mail_outbox_html_body_check CHECK ((html_body <> ''::text)),
    CONSTRAINT mail_outbox_recipient_check CHECK ((recipient <> ''::text)),
    CONSTRAINT mail_outbox_subject_check CHECK ((subject <> ''::text)),
    CONSTRAINT mail_outbox_text_body_check CHECK ((text_body <> ''::text))
);



ALTER TABLE platform.mail_outbox OWNER TO axiom_owner;

CREATE TABLE platform.migration_checksums (
    module text NOT NULL,
    version_id bigint NOT NULL,
    filename text NOT NULL,
    checksum text NOT NULL,
    CONSTRAINT migration_checksums_checksum_check CHECK ((checksum ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT migration_checksums_version_id_check CHECK ((version_id > 0))
);



ALTER TABLE platform.migration_checksums OWNER TO axiom_owner;

CREATE TABLE platform.model_prices_public_readiness (
    id boolean DEFAULT true NOT NULL,
    enabled boolean NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT model_prices_public_readiness_id_check CHECK (id)
);



ALTER TABLE platform.model_prices_public_readiness OWNER TO axiom_owner;

CREATE TABLE platform.nodes (
    id text NOT NULL,
    seen_at timestamptz DEFAULT now() NOT NULL,
    share_session uuid,
    share_view text[],
    share_joining boolean DEFAULT true NOT NULL,
    key_versions bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    key_active bigint DEFAULT 0 NOT NULL,
    key_active_since timestamptz DEFAULT now() NOT NULL,
    supports_billing_preference boolean DEFAULT false NOT NULL,
    billing_preference_session uuid,
    supports_model_prices_public boolean DEFAULT false NOT NULL,
    model_prices_public_session uuid
);



ALTER TABLE platform.nodes OWNER TO axiom_owner;

CREATE TABLE platform.search_config (
    id uuid NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    CONSTRAINT search_config_id_check CHECK ((id = '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT search_config_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT search_config_nonce_check CHECK ((octet_length(nonce) = 12))
);



ALTER TABLE platform.search_config OWNER TO axiom_owner;

CREATE TABLE platform.search_engines (
    id uuid NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    url text NOT NULL,
    enabled boolean NOT NULL,
    is_default boolean NOT NULL,
    cost_search text NOT NULL,
    cost_result text NOT NULL,
    markup text NOT NULL,
    price_search text NOT NULL,
    price_result text NOT NULL,
    price_version bigint NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT search_engines_check CHECK (((NOT is_default) OR enabled)),
    CONSTRAINT search_engines_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT search_engines_kind_check CHECK ((kind = ANY (ARRAY['searxng'::text, 'firecrawl'::text]))),
    CONSTRAINT search_engines_name_check CHECK (((name ~ '^[a-z0-9][a-z0-9_-]{0,31}$'::text) AND (name <> 'native'::text))),
    CONSTRAINT search_engines_nonce_check CHECK ((octet_length(nonce) = 12)),
    CONSTRAINT search_engines_price_version_check CHECK ((price_version > 0)),
    CONSTRAINT search_engines_url_check CHECK (((char_length(url) >= 1) AND (char_length(url) <= 2048)))
);



ALTER TABLE platform.search_engines OWNER TO axiom_owner;

CREATE TABLE platform.smtp_config (
    id uuid NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    CONSTRAINT smtp_config_id_check CHECK ((id = '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT smtp_config_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT smtp_config_nonce_check CHECK ((octet_length(nonce) = 12))
);



ALTER TABLE platform.smtp_config OWNER TO axiom_owner;

CREATE TABLE platform.tenants (
    site_id uuid NOT NULL,
    tenant_id uuid NOT NULL
);



ALTER TABLE platform.tenants OWNER TO axiom_owner;

ALTER TABLE ONLY platform.alert_positions
    ADD CONSTRAINT alert_positions_pkey PRIMARY KEY (source);

ALTER TABLE ONLY platform.alert_signals
    ADD CONSTRAINT alert_signals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.alert_signals
    ADD CONSTRAINT alert_signals_seq_key UNIQUE (seq);

ALTER TABLE platform.audit_log
    ADD CONSTRAINT audit_log_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['user'::text, 'system'::text, 'management_key'::text, 'operations_agent'::text]))) NOT VALID;

ALTER TABLE ONLY platform.audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.billing_preference_readiness
    ADD CONSTRAINT billing_preference_readiness_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.credential_share_departures
    ADD CONSTRAINT credential_share_departures_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.discarded_jobs
    ADD CONSTRAINT discarded_jobs_pkey PRIMARY KEY (job_id);

ALTER TABLE ONLY platform.file_parser_config
    ADD CONSTRAINT file_parser_config_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.idempotency
    ADD CONSTRAINT idempotency_pkey PRIMARY KEY (key);

ALTER TABLE ONLY platform.key_retirements
    ADD CONSTRAINT key_retirements_pkey PRIMARY KEY (version);

ALTER TABLE ONLY platform.key_versions
    ADD CONSTRAINT key_versions_fingerprint_key UNIQUE (fingerprint);

ALTER TABLE ONLY platform.key_versions
    ADD CONSTRAINT key_versions_pkey PRIMARY KEY (version);

ALTER TABLE ONLY platform.mail_outbox
    ADD CONSTRAINT mail_outbox_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.migration_checksums
    ADD CONSTRAINT migration_checksums_pkey PRIMARY KEY (module, version_id);

ALTER TABLE ONLY platform.model_prices_public_readiness
    ADD CONSTRAINT model_prices_public_readiness_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.nodes
    ADD CONSTRAINT nodes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.search_config
    ADD CONSTRAINT search_config_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.search_engines
    ADD CONSTRAINT search_engines_name_key UNIQUE (name);

ALTER TABLE ONLY platform.search_engines
    ADD CONSTRAINT search_engines_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.smtp_config
    ADD CONSTRAINT smtp_config_pkey PRIMARY KEY (id);

ALTER TABLE ONLY platform.tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (site_id, tenant_id);

CREATE INDEX audit_log_account ON platform.audit_log USING btree (account_id, at DESC, id DESC);

CREATE INDEX audit_log_action ON platform.audit_log USING btree (action, at DESC, id DESC);

CREATE INDEX audit_log_actor ON platform.audit_log USING btree (actor_id, at DESC, id DESC);

CREATE INDEX audit_log_actor_kind ON platform.audit_log USING btree (actor_kind, at DESC, id DESC);

CREATE INDEX audit_log_module ON platform.audit_log USING btree (module, at DESC, id DESC);

CREATE INDEX audit_log_request ON platform.audit_log USING btree (request_id, at DESC, id DESC);

CREATE INDEX audit_log_site ON platform.audit_log USING btree (site_id, at DESC, id DESC);

CREATE INDEX audit_log_target ON platform.audit_log USING btree (target_id, at DESC, id DESC);

CREATE INDEX audit_log_target_kind ON platform.audit_log USING btree (target_kind, at DESC, id DESC);

CREATE INDEX audit_log_time ON platform.audit_log USING btree (at DESC, id DESC);

CREATE UNIQUE INDEX key_versions_one_active ON platform.key_versions USING btree (state) WHERE (state = 'active'::text);

CREATE UNIQUE INDEX search_engines_one_default ON platform.search_engines USING btree (is_default) WHERE is_default;

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.file_parser_config FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.search_config FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.search_engines FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.smtp_config FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

ALTER TABLE ONLY platform.audit_log
    ADD CONSTRAINT registered_tenant FOREIGN KEY (site_id, tenant_id) REFERENCES platform.tenants(site_id, tenant_id);







CREATE POLICY fence ON platform.key_retirements USING (true) WITH CHECK (true);



CREATE POLICY inserts ON platform.idempotency FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON platform.tenants FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (tenant_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));



CREATE POLICY lookup ON platform.mail_outbox FOR SELECT TO axiom_lookup USING (true);





CREATE POLICY platform_only ON platform.alert_positions USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.alert_signals USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.credential_share_departures USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());

CREATE POLICY platform_only ON platform.discarded_jobs USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.file_parser_config USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.key_versions USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.nodes USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.search_config USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.search_engines USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON platform.smtp_config USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON platform.billing_preference_readiness FOR SELECT USING (true);

CREATE POLICY reads ON platform.idempotency FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON platform.migration_checksums FOR SELECT USING (true);

CREATE POLICY reads ON platform.model_prices_public_readiness FOR SELECT USING (true);

CREATE POLICY reads ON platform.tenants FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY records ON platform.migration_checksums FOR INSERT WITH CHECK (platform.in_platform_scope());




CREATE POLICY tenant ON platform.audit_log USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON platform.mail_outbox USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));


CREATE POLICY updates ON platform.idempotency FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) AND (response IS NULL))) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY writes ON platform.billing_preference_readiness USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());

CREATE POLICY writes ON platform.model_prices_public_readiness USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());

GRANT USAGE ON SCHEMA platform TO axiom_app;
GRANT USAGE ON SCHEMA platform TO axiom_lookup;

REVOKE ALL ON FUNCTION platform.mail_tenant(id uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION platform.mail_tenant(id uuid) TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE platform.alert_positions TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE platform.alert_signals TO axiom_app;

GRANT UPDATE(quarantined) ON TABLE platform.alert_signals TO axiom_app;

GRANT USAGE ON SEQUENCE platform.alert_signals_seq_seq TO axiom_app;

GRANT SELECT ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(actor_kind) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(actor_id) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(request_id) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(action) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(module) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(target_kind) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(target_id) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(site_id) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(account_id) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(reason) ON TABLE platform.audit_log TO axiom_app;

GRANT INSERT(detail) ON TABLE platform.audit_log TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE platform.billing_preference_readiness TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE platform.credential_share_departures TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE platform.discarded_jobs TO axiom_app;

GRANT UPDATE(notified) ON TABLE platform.discarded_jobs TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE platform.file_parser_config TO axiom_app;

GRANT SELECT ON TABLE platform.idempotency TO axiom_app;

GRANT INSERT(key) ON TABLE platform.idempotency TO axiom_app;

GRANT INSERT(actor) ON TABLE platform.idempotency TO axiom_app;

GRANT INSERT(operation) ON TABLE platform.idempotency TO axiom_app;

GRANT INSERT(fingerprint) ON TABLE platform.idempotency TO axiom_app;

GRANT UPDATE(response) ON TABLE platform.idempotency TO axiom_app;

GRANT SELECT,INSERT ON TABLE platform.key_retirements TO axiom_app;

GRANT SELECT,INSERT ON TABLE platform.key_versions TO axiom_app;

GRANT UPDATE(state) ON TABLE platform.key_versions TO axiom_app;

GRANT SELECT,DELETE ON TABLE platform.mail_outbox TO axiom_app;

GRANT SELECT(id) ON TABLE platform.mail_outbox TO axiom_lookup;

GRANT INSERT(site_id) ON TABLE platform.mail_outbox TO axiom_app;
GRANT SELECT(site_id) ON TABLE platform.mail_outbox TO axiom_lookup;

GRANT INSERT(account_id) ON TABLE platform.mail_outbox TO axiom_app;
GRANT SELECT(account_id) ON TABLE platform.mail_outbox TO axiom_lookup;

GRANT INSERT(recipient) ON TABLE platform.mail_outbox TO axiom_app;

GRANT INSERT(subject) ON TABLE platform.mail_outbox TO axiom_app;

GRANT INSERT(text_body) ON TABLE platform.mail_outbox TO axiom_app;

GRANT INSERT(html_body) ON TABLE platform.mail_outbox TO axiom_app;

GRANT SELECT ON TABLE platform.migration_checksums TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE platform.model_prices_public_readiness TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE platform.nodes TO axiom_app;

GRANT UPDATE(supports_billing_preference) ON TABLE platform.nodes TO axiom_app;

GRANT UPDATE(billing_preference_session) ON TABLE platform.nodes TO axiom_app;

GRANT UPDATE(supports_model_prices_public) ON TABLE platform.nodes TO axiom_app;

GRANT UPDATE(model_prices_public_session) ON TABLE platform.nodes TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE platform.search_config TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE platform.search_engines TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE platform.smtp_config TO axiom_app;

GRANT SELECT,INSERT ON TABLE platform.tenants TO axiom_app;



ALTER FUNCTION platform.mail_tenant(id uuid) OWNER TO axiom_lookup;

REVOKE CREATE ON SCHEMA platform FROM axiom_lookup;

ALTER TABLE ONLY platform.alert_positions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.alert_signals FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.audit_log FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.billing_preference_readiness FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.credential_share_departures FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.discarded_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.file_parser_config FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.idempotency FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.key_retirements FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.key_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.mail_outbox FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.migration_checksums FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.model_prices_public_readiness FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.nodes FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.search_config FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.search_engines FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.smtp_config FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY platform.tenants FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.alert_positions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.alert_signals ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.billing_preference_readiness ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.credential_share_departures ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.discarded_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.file_parser_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.idempotency ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.key_retirements ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.key_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.mail_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.migration_checksums ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.model_prices_public_readiness ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.search_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.search_engines ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.smtp_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.tenants ENABLE ROW LEVEL SECURITY;
