-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

-- +goose StatementBegin
CREATE FUNCTION suppliers.keep_codename() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.codename IS NOT NULL AND NEW.codename IS DISTINCT FROM OLD.codename THEN
        RAISE EXCEPTION 'the codename of a group never changes' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;


ALTER FUNCTION suppliers.keep_codename() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION suppliers.unit_value_dimensions() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    SELECT s.settlement_currency, s.native_unit INTO NEW.currency, NEW.native_unit
    FROM suppliers.suppliers s WHERE s.id = NEW.supplier_id;
    RETURN NEW;
END
$$;


ALTER FUNCTION suppliers.unit_value_dimensions() OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE suppliers.attempt_costs (
    request_id uuid NOT NULL,
    attempt_seq smallint NOT NULL,
    site_id uuid,
    account_id uuid,
    supplier_id uuid NOT NULL,
    credential_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    cost_schedule_id uuid NOT NULL,
    charge_ref text,
    usage jsonb NOT NULL,
    priced_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    cost_flag text NOT NULL,
    estimated_native numeric NOT NULL,
    charge_record_id uuid,
    state text DEFAULT 'pending'::text NOT NULL,
    unit_value_id uuid,
    exchange_rate_id uuid,
    conversion_from text,
    conversion_to text,
    conversion_state text,
    test boolean DEFAULT false NOT NULL,
    CONSTRAINT attempt_costs_charge_ref_check CHECK ((charge_ref <> ''::text)),
    CONSTRAINT attempt_costs_conversion_state_check CHECK ((conversion_state = ANY (ARRAY['same_currency'::text, 'converted'::text, 'missing_rate'::text, 'overflow'::text, 'unpriced'::text, 'not_executed'::text]))),
    CONSTRAINT attempt_costs_cost_flag_check CHECK ((cost_flag = ANY (ARRAY['complete'::text, 'unpriced'::text, 'no_usage'::text]))),
    CONSTRAINT attempt_costs_estimated_native_check CHECK (((estimated_native >= (0)::numeric) AND (estimated_native <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])))),
    CONSTRAINT attempt_costs_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'matched'::text]))),
    CONSTRAINT attempt_costs_usage_check CHECK ((jsonb_typeof(usage) = 'object'::text)),
    CONSTRAINT matched_has_charge CHECK (((state = 'matched'::text) = (charge_record_id IS NOT NULL))),
    CONSTRAINT tenant_whole CHECK (((site_id IS NULL) = (account_id IS NULL)))
);



ALTER TABLE suppliers.attempt_costs OWNER TO axiom_owner;

CREATE TABLE suppliers.attempt_journal (
    request_id uuid NOT NULL,
    attempt_seq smallint NOT NULL,
    site_id uuid,
    account_id uuid,
    supplier_id uuid NOT NULL,
    credential_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    cost_schedule_id uuid NOT NULL,
    priced_at timestamptz NOT NULL,
    charge_ref text,
    committed boolean DEFAULT false NOT NULL,
    written_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT attempt_journal_attempt_seq_check CHECK (((attempt_seq >= 1) AND (attempt_seq <= 3))),
    CONSTRAINT attempt_journal_charge_ref_check CHECK ((charge_ref <> ''::text))
);



ALTER TABLE suppliers.attempt_journal OWNER TO axiom_owner;

CREATE TABLE suppliers.charge_events (
    id uuid DEFAULT uuidv7() NOT NULL,
    charge_record_id uuid NOT NULL,
    revision integer NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT charge_events_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT charge_events_revision_check CHECK ((revision >= 1))
);



ALTER TABLE suppliers.charge_events OWNER TO axiom_owner;

CREATE TABLE suppliers.charge_records (
    id uuid DEFAULT uuidv7() NOT NULL,
    supplier_id uuid NOT NULL,
    credential_id uuid,
    charge_ref text NOT NULL,
    occurred_at timestamptz NOT NULL,
    amount_native numeric NOT NULL,
    usage jsonb NOT NULL,
    funding text NOT NULL,
    upstream_model text NOT NULL,
    state text DEFAULT 'new'::text NOT NULL,
    attribution text,
    attempt_request_id uuid,
    attempt_seq smallint,
    lost_request_id uuid,
    revision integer DEFAULT 1 NOT NULL,
    CONSTRAINT attribution_shape CHECK (((((attribution IS NULL) OR (attribution = 'unattributed'::text)) AND (attempt_request_id IS NULL) AND (attempt_seq IS NULL) AND (lost_request_id IS NULL)) OR ((attribution = 'attempt'::text) AND (attempt_request_id IS NOT NULL) AND (attempt_seq IS NOT NULL) AND (lost_request_id IS NULL)) OR ((attribution = 'lost_request'::text) AND (lost_request_id IS NOT NULL) AND (attempt_request_id IS NULL) AND (attempt_seq IS NULL)))),
    CONSTRAINT charge_records_amount_native_check CHECK (((amount_native >= (0)::numeric) AND (amount_native <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])))),
    CONSTRAINT charge_records_attribution_check CHECK ((attribution = ANY (ARRAY['attempt'::text, 'lost_request'::text, 'unattributed'::text]))),
    CONSTRAINT charge_records_charge_ref_check CHECK ((charge_ref <> ''::text)),
    CONSTRAINT charge_records_funding_check CHECK ((funding = ANY (ARRAY['wallet'::text, 'other'::text]))),
    CONSTRAINT charge_records_revision_check CHECK ((revision >= 1)),
    CONSTRAINT charge_records_state_check CHECK ((state = ANY (ARRAY['new'::text, 'matched'::text, 'held'::text, 'assigned'::text]))),
    CONSTRAINT charge_records_usage_check CHECK ((jsonb_typeof(usage) = 'object'::text)),
    CONSTRAINT state_follows_attribution CHECK ((((state = ANY (ARRAY['new'::text, 'held'::text])) AND (attribution IS NULL)) OR ((state = 'matched'::text) AND (attribution = 'attempt'::text)) OR ((state = 'assigned'::text) AND (attribution = ANY (ARRAY['lost_request'::text, 'unattributed'::text])))))
);



ALTER TABLE suppliers.charge_records OWNER TO axiom_owner;

CREATE TABLE suppliers.cost_schedules (
    id uuid DEFAULT uuidv7() NOT NULL,
    supplier_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    native_unit text NOT NULL,
    schedule jsonb NOT NULL,
    basis text NOT NULL,
    fingerprint text NOT NULL,
    effective_from timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT cost_schedules_basis_check CHECK ((basis = ANY (ARRAY['published'::text, 'confirmed'::text]))),
    CONSTRAINT cost_schedules_fingerprint_check CHECK ((fingerprint <> ''::text)),
    CONSTRAINT cost_schedules_native_unit_check CHECK ((native_unit = ANY (ARRAY['quota'::text, 'usd'::text]))),
    CONSTRAINT cost_schedules_schedule_check CHECK ((jsonb_typeof(schedule) = 'object'::text))
);



ALTER TABLE suppliers.cost_schedules OWNER TO axiom_owner;

CREATE TABLE suppliers.credentials (
    id uuid NOT NULL,
    supplier_id uuid NOT NULL,
    supplier_group_id uuid NOT NULL,
    upstream_key_ref text NOT NULL,
    ciphertext bytea,
    nonce bytea,
    wrapped_dek bytea,
    key_version bigint,
    status text DEFAULT 'binding'::text NOT NULL,
    cooldown_until timestamptz,
    expires_at timestamptz,
    max_concurrency integer NOT NULL,
    egress_id text,
    requests_per_minute integer,
    allowed_models text[],
    hint text,
    CONSTRAINT credentials_ciphertext_check CHECK ((octet_length(ciphertext) >= 16)),
    CONSTRAINT credentials_egress_id_check CHECK ((egress_id <> ''::text)),
    CONSTRAINT credentials_hint_check CHECK (((char_length(hint) >= 4) AND (char_length(hint) <= 8))),
    CONSTRAINT credentials_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT credentials_max_concurrency_check CHECK ((max_concurrency >= 1)),
    CONSTRAINT credentials_nonce_check CHECK ((octet_length(nonce) = 12)),
    CONSTRAINT credentials_requests_per_minute_check CHECK ((requests_per_minute > 0)),
    CONSTRAINT credentials_status_check CHECK ((status = ANY (ARRAY['binding'::text, 'active'::text, 'upstream_disabled'::text, 'quota_exhausted'::text, 'expired'::text, 'disabled'::text, 'erased'::text, 'rejected'::text]))),
    CONSTRAINT credentials_upstream_key_ref_check CHECK ((upstream_key_ref <> ''::text)),
    CONSTRAINT credentials_wrapped_dek_check CHECK ((octet_length(wrapped_dek) > 0)),
    CONSTRAINT erased_is_empty CHECK (((status <> 'erased'::text) OR (ciphertext IS NULL))),
    CONSTRAINT sealed_together CHECK ((((ciphertext IS NULL) = (nonce IS NULL)) AND ((ciphertext IS NULL) = (wrapped_dek IS NULL)) AND ((ciphertext IS NULL) = (key_version IS NULL)))),
    CONSTRAINT sealed_unless_binding CHECK (((status = ANY (ARRAY['binding'::text, 'erased'::text])) OR (ciphertext IS NOT NULL)))
);



ALTER TABLE suppliers.credentials OWNER TO axiom_owner;

CREATE TABLE suppliers.ended_requests (
    request_id uuid NOT NULL,
    site_id uuid,
    account_id uuid,
    ended_at timestamptz NOT NULL,
    CONSTRAINT tenant_whole CHECK (((site_id IS NULL) = (account_id IS NULL)))
);



ALTER TABLE suppliers.ended_requests OWNER TO axiom_owner;

CREATE TABLE suppliers.endpoints (
    id uuid DEFAULT uuidv7() NOT NULL,
    supplier_id uuid NOT NULL,
    supplier_group_id uuid NOT NULL,
    model_id uuid NOT NULL,
    upstream_name text NOT NULL,
    formats text[] NOT NULL,
    cost_state text DEFAULT 'unknown'::text NOT NULL,
    cost_state_reason text,
    cost_schedule_id uuid,
    enabled boolean DEFAULT true NOT NULL,
    quantization text,
    CONSTRAINT endpoints_cost_state_check CHECK ((cost_state = ANY (ARRAY['known'::text, 'unknown'::text, 'conflict'::text]))),
    CONSTRAINT endpoints_cost_state_reason_check CHECK ((cost_state_reason <> ''::text)),
    CONSTRAINT endpoints_formats_check CHECK (((cardinality(formats) >= 1) AND (formats <@ ARRAY['messages'::text, 'responses'::text, 'chat'::text, 'embeddings'::text, 'rerank'::text, 'completions'::text]))),
    CONSTRAINT endpoints_quantization_check CHECK ((quantization = ANY (ARRAY['int4'::text, 'int8'::text, 'fp4'::text, 'mxfp4'::text, 'nvfp4'::text, 'fp6'::text, 'fp8'::text, 'mxfp8'::text, 'fp16'::text, 'bf16'::text, 'fp32'::text, 'unknown'::text]))),
    CONSTRAINT endpoints_upstream_name_check CHECK ((upstream_name <> ''::text)),
    CONSTRAINT known_has_schedule CHECK (((cost_state <> 'known'::text) OR (cost_schedule_id IS NOT NULL)))
);



ALTER TABLE suppliers.endpoints OWNER TO axiom_owner;

CREATE TABLE suppliers.lost_request_resolutions (
    request_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    resolved_at timestamptz DEFAULT now() NOT NULL
);



ALTER TABLE suppliers.lost_request_resolutions OWNER TO axiom_owner;

CREATE TABLE suppliers.lost_requests (
    request_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    route_endpoint_ids uuid[],
    recorded_at timestamptz DEFAULT now() NOT NULL
);



ALTER TABLE suppliers.lost_requests OWNER TO axiom_owner;

CREATE TABLE suppliers.rate_buckets (
    scope text NOT NULL,
    name text NOT NULL,
    window_start timestamptz,
    used integer DEFAULT 0 NOT NULL,
    blocked_until timestamptz,
    CONSTRAINT rate_buckets_name_check CHECK ((name <> ''::text)),
    CONSTRAINT rate_buckets_scope_check CHECK ((scope <> ''::text)),
    CONSTRAINT rate_buckets_used_check CHECK ((used >= 0))
);



ALTER TABLE suppliers.rate_buckets OWNER TO axiom_owner;

CREATE TABLE suppliers.supplier_alias_revisions (
    alias_id uuid NOT NULL,
    supplier_id uuid NOT NULL,
    revision bigint NOT NULL,
    previous_model_id uuid NOT NULL,
    model_id uuid NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT supplier_alias_revisions_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT supplier_alias_revisions_check CHECK ((model_id <> previous_model_id)),
    CONSTRAINT supplier_alias_revisions_revision_check CHECK ((revision >= 2))
);



ALTER TABLE suppliers.supplier_alias_revisions OWNER TO axiom_owner;

CREATE TABLE suppliers.supplier_aliases (
    id uuid DEFAULT uuidv7() NOT NULL,
    supplier_id uuid NOT NULL,
    supplier_group_id uuid,
    upstream_name text NOT NULL,
    model_id uuid NOT NULL,
    set_by text NOT NULL,
    formats text[],
    CONSTRAINT supplier_aliases_formats_check CHECK (((formats IS NULL) OR ((cardinality(formats) >= 1) AND (formats <@ ARRAY['messages'::text, 'responses'::text, 'chat'::text, 'embeddings'::text, 'rerank'::text, 'completions'::text])))),
    CONSTRAINT supplier_aliases_set_by_check CHECK ((set_by = ANY (ARRAY['user'::text, 'system'::text]))),
    CONSTRAINT supplier_aliases_upstream_name_check CHECK ((upstream_name <> ''::text))
);



ALTER TABLE suppliers.supplier_aliases OWNER TO axiom_owner;

CREATE TABLE suppliers.supplier_groups (
    id uuid DEFAULT uuidv7() NOT NULL,
    supplier_id uuid NOT NULL,
    upstream_ref text NOT NULL,
    name text NOT NULL,
    ratio numeric NOT NULL,
    facts jsonb DEFAULT '{}'::jsonb NOT NULL,
    selected boolean DEFAULT false NOT NULL,
    codename text,
    present boolean DEFAULT true NOT NULL,
    last_seen_at timestamptz,
    pool_accounts integer,
    pool_failed integer,
    pool_usable integer,
    pool_headroom integer,
    pool_capacity integer,
    pool_low boolean,
    pool_observed_at timestamptz,
    key_eligible boolean DEFAULT false NOT NULL,
    key_error text,
    key_retry_at timestamptz,
    CONSTRAINT pool_capacity_complete CHECK ((((pool_accounts IS NULL) AND (pool_failed IS NULL) AND (pool_usable IS NULL) AND (pool_headroom IS NULL) AND (pool_capacity IS NULL) AND (pool_low IS NULL) AND (pool_observed_at IS NULL)) OR ((pool_accounts > 0) AND ((pool_failed >= 0) AND (pool_failed <= pool_accounts)) AND ((pool_usable >= 0) AND (pool_usable <= pool_accounts)) AND ((pool_headroom IS NULL) = (pool_capacity IS NULL)) AND ((pool_headroom IS NULL) OR ((pool_headroom >= 0) AND (pool_headroom <= 100))) AND ((pool_capacity IS NULL) OR ((pool_capacity >= 0) AND (pool_capacity <= 100))) AND (pool_low IS NOT NULL) AND ((NOT pool_low) OR (pool_capacity IS NOT NULL) OR ((pool_failed * 2) >= pool_accounts)) AND (pool_observed_at IS NOT NULL)))),
    CONSTRAINT selected_has_codename CHECK (((NOT selected) OR (codename IS NOT NULL))),
    CONSTRAINT supplier_groups_codename_check CHECK ((codename <> ''::text)),
    CONSTRAINT supplier_groups_facts_check CHECK ((jsonb_typeof(facts) = 'object'::text)),
    CONSTRAINT supplier_groups_key_error_check CHECK ((key_error = ANY (ARRAY['not_permitted'::text, 'group_unavailable'::text, 'refused'::text]))),
    CONSTRAINT supplier_groups_key_error_complete CHECK (((key_error IS NULL) = (key_retry_at IS NULL))),
    CONSTRAINT supplier_groups_name_check CHECK ((name <> ''::text)),
    CONSTRAINT supplier_groups_ratio_check CHECK (((ratio >= (0)::numeric) AND (ratio <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])))),
    CONSTRAINT supplier_groups_upstream_ref_check CHECK ((upstream_ref <> ''::text))
);



ALTER TABLE suppliers.supplier_groups OWNER TO axiom_owner;

CREATE TABLE suppliers.supplier_logins (
    supplier_id uuid NOT NULL,
    kind text NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    expires_at timestamptz,
    CONSTRAINT supplier_logins_ciphertext_check CHECK ((octet_length(ciphertext) >= 16)),
    CONSTRAINT supplier_logins_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT supplier_logins_kind_check CHECK ((kind ~ '^[a-z][a-z0-9_]*$'::text)),
    CONSTRAINT supplier_logins_nonce_check CHECK ((octet_length(nonce) = 12)),
    CONSTRAINT supplier_logins_version_check CHECK ((version >= 1)),
    CONSTRAINT supplier_logins_wrapped_dek_check CHECK ((octet_length(wrapped_dek) > 0))
);



ALTER TABLE suppliers.supplier_logins OWNER TO axiom_owner;

CREATE TABLE suppliers.supplier_models (
    supplier_id uuid NOT NULL,
    supplier_group_id uuid NOT NULL,
    upstream_name text NOT NULL,
    model_id uuid,
    formats text[] NOT NULL,
    present boolean DEFAULT true NOT NULL,
    first_seen_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT supplier_models_formats_check CHECK (((cardinality(formats) >= 1) AND (formats <@ ARRAY['messages'::text, 'responses'::text, 'chat'::text, 'embeddings'::text, 'rerank'::text, 'completions'::text]))),
    CONSTRAINT supplier_models_upstream_name_check CHECK ((upstream_name <> ''::text))
);



ALTER TABLE suppliers.supplier_models OWNER TO axiom_owner;

CREATE TABLE suppliers.suppliers (
    id uuid DEFAULT uuidv7() NOT NULL,
    owner_scope text NOT NULL,
    site_id uuid,
    account_id uuid,
    name text NOT NULL,
    kind text NOT NULL,
    origin text NOT NULL,
    upstream_account text NOT NULL,
    generation text,
    detected_version text,
    native_unit text NOT NULL,
    settlement_currency text NOT NULL,
    mgmt_egress_id text NOT NULL,
    status text DEFAULT 'connecting'::text NOT NULL,
    status_reason text,
    config_gen bigint DEFAULT 0 NOT NULL,
    balance_native numeric,
    balance_observed_at timestamptz,
    low_balance_threshold numeric,
    created_at timestamptz DEFAULT now() NOT NULL,
    byok_fallback boolean DEFAULT true NOT NULL,
    CONSTRAINT owner_matches_scope CHECK ((((owner_scope = 'platform'::text) AND (site_id IS NULL) AND (account_id IS NULL)) OR ((owner_scope = 'site'::text) AND (site_id IS NOT NULL) AND (account_id IS NULL)) OR ((owner_scope = 'account'::text) AND (site_id IS NOT NULL) AND (account_id IS NOT NULL)))),
    CONSTRAINT suppliers_balance_native_check CHECK ((balance_native <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric]))),
    CONSTRAINT suppliers_config_gen_check CHECK ((config_gen >= 0)),
    CONSTRAINT suppliers_detected_version_check CHECK ((detected_version <> ''::text)),
    CONSTRAINT suppliers_generation_check CHECK ((generation <> ''::text)),
    CONSTRAINT suppliers_kind_check CHECK ((kind ~ '^[a-z][a-z0-9]*$'::text)),
    CONSTRAINT suppliers_low_balance_threshold_check CHECK (((low_balance_threshold >= (0)::numeric) AND (low_balance_threshold <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])))),
    CONSTRAINT suppliers_mgmt_egress_id_check CHECK ((mgmt_egress_id <> ''::text)),
    CONSTRAINT suppliers_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 200))),
    CONSTRAINT suppliers_native_unit_check CHECK ((native_unit = ANY (ARRAY['quota'::text, 'usd'::text]))),
    CONSTRAINT suppliers_origin_check CHECK ((origin ~ '^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$'::text)),
    CONSTRAINT suppliers_owner_scope_check CHECK ((owner_scope = ANY (ARRAY['platform'::text, 'site'::text, 'account'::text]))),
    CONSTRAINT suppliers_settlement_currency_check CHECK ((settlement_currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT suppliers_status_check CHECK ((status = ANY (ARRAY['connecting'::text, 'active'::text, 'low_balance'::text, 'needs_attention'::text, 'paused'::text, 'removing'::text, 'removed'::text, 'byok'::text]))),
    CONSTRAINT suppliers_status_reason_check CHECK (((status_reason IS NULL) OR ((char_length(status_reason) >= 1) AND (char_length(status_reason) <= 200)))),
    CONSTRAINT suppliers_upstream_account_check CHECK ((upstream_account <> ''::text))
);



ALTER TABLE suppliers.suppliers OWNER TO axiom_owner;

CREATE TABLE suppliers.sync_events (
    supplier_id uuid NOT NULL,
    version bigint NOT NULL,
    seq integer NOT NULL,
    kind text NOT NULL,
    subject text NOT NULL,
    old_value text NOT NULL,
    new_value text NOT NULL,
    why text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT sync_events_kind_check CHECK ((kind <> ''::text)),
    CONSTRAINT sync_events_seq_check CHECK ((seq >= 0)),
    CONSTRAINT sync_events_version_check CHECK ((version >= 1))
);



ALTER TABLE suppliers.sync_events OWNER TO axiom_owner;

CREATE TABLE suppliers.sync_plans (
    supplier_id uuid NOT NULL,
    version bigint NOT NULL,
    seq integer NOT NULL,
    endpoint text NOT NULL,
    fingerprint text NOT NULL,
    schedule jsonb NOT NULL,
    basis text NOT NULL,
    CONSTRAINT sync_plans_basis_check CHECK ((basis = ANY (ARRAY['published'::text, 'confirmed'::text]))),
    CONSTRAINT sync_plans_endpoint_check CHECK ((endpoint <> ''::text)),
    CONSTRAINT sync_plans_fingerprint_check CHECK ((fingerprint <> ''::text)),
    CONSTRAINT sync_plans_schedule_check CHECK ((jsonb_typeof(schedule) = 'object'::text)),
    CONSTRAINT sync_plans_seq_check CHECK ((seq >= 0)),
    CONSTRAINT sync_plans_version_check CHECK ((version >= 1))
);



ALTER TABLE suppliers.sync_plans OWNER TO axiom_owner;

CREATE TABLE suppliers.sync_states (
    supplier_id uuid NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    format smallint DEFAULT 1 NOT NULL,
    state jsonb DEFAULT '{}'::jsonb NOT NULL,
    next_due timestamptz NOT NULL,
    lease_holder text,
    lease_fence bigint DEFAULT 0 NOT NULL,
    lease_until timestamptz,
    listing jsonb,
    CONSTRAINT lease_whole CHECK (((lease_holder IS NULL) = (lease_until IS NULL))),
    CONSTRAINT sync_states_format_check CHECK ((format >= 1)),
    CONSTRAINT sync_states_lease_fence_check CHECK ((lease_fence >= 0)),
    CONSTRAINT sync_states_lease_holder_check CHECK ((lease_holder <> ''::text)),
    CONSTRAINT sync_states_listing_check CHECK (((listing IS NULL) OR (jsonb_typeof(listing) = 'object'::text))),
    CONSTRAINT sync_states_state_check CHECK ((jsonb_typeof(state) = 'object'::text)),
    CONSTRAINT sync_states_version_check CHECK ((version >= 0))
);



ALTER TABLE suppliers.sync_states OWNER TO axiom_owner;

CREATE TABLE suppliers.unit_values (
    id uuid DEFAULT uuidv7() NOT NULL,
    supplier_id uuid NOT NULL,
    value numeric NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    currency text NOT NULL,
    native_unit text NOT NULL,
    CONSTRAINT unit_values_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT unit_values_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 1000))),
    CONSTRAINT unit_values_value_check CHECK (((value > (0)::numeric) AND (value <= (1000)::numeric) AND (scale(value) <= 15) AND (value <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric]))))
);



ALTER TABLE suppliers.unit_values OWNER TO axiom_owner;

CREATE TABLE suppliers.usage_records (
    id uuid DEFAULT uuidv7() NOT NULL,
    supplier_id uuid NOT NULL,
    request_id text,
    received_at timestamptz DEFAULT now() NOT NULL,
    record jsonb NOT NULL,
    state text DEFAULT 'new'::text NOT NULL,
    reason text,
    CONSTRAINT usage_records_reason_check CHECK ((reason = 'unreadable'::text)),
    CONSTRAINT usage_records_record_check CHECK ((jsonb_typeof(record) = 'object'::text)),
    CONSTRAINT usage_records_request_id_check CHECK (((request_id IS NULL) OR ((request_id <> ''::text) AND (char_length(request_id) <= 200)))),
    CONSTRAINT usage_records_state_check CHECK ((state = ANY (ARRAY['new'::text, 'rejected'::text]))),
    CONSTRAINT usage_records_state_reason CHECK (((state = 'new'::text) = (reason IS NULL)))
);



ALTER TABLE suppliers.usage_records OWNER TO axiom_owner;

ALTER TABLE suppliers.attempt_costs
    ADD CONSTRAINT attempt_costs_attempt_seq_check CHECK (((attempt_seq >= 1) AND (attempt_seq <= 33))) NOT VALID;

ALTER TABLE ONLY suppliers.attempt_costs
    ADD CONSTRAINT attempt_costs_charge_record_id_key UNIQUE (charge_record_id);

ALTER TABLE ONLY suppliers.attempt_costs
    ADD CONSTRAINT attempt_costs_pkey PRIMARY KEY (request_id, attempt_seq);

ALTER TABLE ONLY suppliers.attempt_journal
    ADD CONSTRAINT attempt_journal_pkey PRIMARY KEY (request_id, attempt_seq);

ALTER TABLE ONLY suppliers.charge_events
    ADD CONSTRAINT charge_events_charge_record_id_revision_key UNIQUE (charge_record_id, revision);

ALTER TABLE ONLY suppliers.charge_events
    ADD CONSTRAINT charge_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.charge_records
    ADD CONSTRAINT charge_records_attempt_request_id_attempt_seq_key UNIQUE (attempt_request_id, attempt_seq);

ALTER TABLE ONLY suppliers.charge_records
    ADD CONSTRAINT charge_records_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.charge_records
    ADD CONSTRAINT charge_records_supplier_id_charge_ref_key UNIQUE (supplier_id, charge_ref);

ALTER TABLE ONLY suppliers.cost_schedules
    ADD CONSTRAINT cost_schedules_endpoint_id_fingerprint_key UNIQUE (endpoint_id, fingerprint);

ALTER TABLE ONLY suppliers.cost_schedules
    ADD CONSTRAINT cost_schedules_id_endpoint_id_key UNIQUE (id, endpoint_id);

ALTER TABLE ONLY suppliers.cost_schedules
    ADD CONSTRAINT cost_schedules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.credentials
    ADD CONSTRAINT credentials_id_supplier_key UNIQUE (id, supplier_id);

ALTER TABLE ONLY suppliers.credentials
    ADD CONSTRAINT credentials_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.credentials
    ADD CONSTRAINT credentials_supplier_id_upstream_key_ref_key UNIQUE (supplier_id, upstream_key_ref);

ALTER TABLE ONLY suppliers.ended_requests
    ADD CONSTRAINT ended_requests_pkey PRIMARY KEY (request_id);

ALTER TABLE ONLY suppliers.endpoints
    ADD CONSTRAINT endpoints_id_supplier_key UNIQUE (id, supplier_id);

ALTER TABLE ONLY suppliers.endpoints
    ADD CONSTRAINT endpoints_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.endpoints
    ADD CONSTRAINT endpoints_supplier_group_id_model_id_key UNIQUE (supplier_group_id, model_id);

ALTER TABLE ONLY suppliers.lost_request_resolutions
    ADD CONSTRAINT lost_request_resolutions_pkey PRIMARY KEY (request_id);

ALTER TABLE ONLY suppliers.lost_requests
    ADD CONSTRAINT lost_requests_pkey PRIMARY KEY (request_id);

ALTER TABLE ONLY suppliers.rate_buckets
    ADD CONSTRAINT rate_buckets_pkey PRIMARY KEY (scope, name);

ALTER TABLE ONLY suppliers.supplier_alias_revisions
    ADD CONSTRAINT supplier_alias_revisions_pkey PRIMARY KEY (alias_id, revision);

ALTER TABLE ONLY suppliers.supplier_aliases
    ADD CONSTRAINT supplier_aliases_id_supplier_id_key UNIQUE (id, supplier_id);

ALTER TABLE ONLY suppliers.supplier_aliases
    ADD CONSTRAINT supplier_aliases_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.supplier_aliases
    ADD CONSTRAINT supplier_aliases_supplier_id_supplier_group_id_upstream_nam_key UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name);

ALTER TABLE ONLY suppliers.supplier_groups
    ADD CONSTRAINT supplier_groups_codename_key UNIQUE (codename);

ALTER TABLE ONLY suppliers.supplier_groups
    ADD CONSTRAINT supplier_groups_id_supplier_id_key UNIQUE (id, supplier_id);

ALTER TABLE ONLY suppliers.supplier_groups
    ADD CONSTRAINT supplier_groups_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.supplier_groups
    ADD CONSTRAINT supplier_groups_supplier_id_upstream_ref_key UNIQUE (supplier_id, upstream_ref);

ALTER TABLE ONLY suppliers.supplier_logins
    ADD CONSTRAINT supplier_logins_pkey PRIMARY KEY (supplier_id);

ALTER TABLE ONLY suppliers.supplier_models
    ADD CONSTRAINT supplier_models_pkey PRIMARY KEY (supplier_group_id, upstream_name);

ALTER TABLE ONLY suppliers.suppliers
    ADD CONSTRAINT suppliers_origin_upstream_account_key UNIQUE (origin, upstream_account);

ALTER TABLE ONLY suppliers.suppliers
    ADD CONSTRAINT suppliers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.sync_events
    ADD CONSTRAINT sync_events_pkey PRIMARY KEY (supplier_id, version, seq);

ALTER TABLE ONLY suppliers.sync_plans
    ADD CONSTRAINT sync_plans_pkey PRIMARY KEY (supplier_id, version, seq);

ALTER TABLE ONLY suppliers.sync_states
    ADD CONSTRAINT sync_states_pkey PRIMARY KEY (supplier_id);

ALTER TABLE ONLY suppliers.unit_values
    ADD CONSTRAINT unit_values_pkey PRIMARY KEY (id);

ALTER TABLE ONLY suppliers.usage_records
    ADD CONSTRAINT usage_records_pkey PRIMARY KEY (id);

CREATE INDEX attempt_costs_charge_ref ON suppliers.attempt_costs USING btree (supplier_id, charge_ref) WHERE (charge_ref IS NOT NULL);

CREATE INDEX attempt_costs_pending ON suppliers.attempt_costs USING btree (finished_at, request_id, attempt_seq) WHERE ((state = 'pending'::text) AND (charge_ref IS NOT NULL));

CREATE INDEX charge_records_new ON suppliers.charge_records USING btree (occurred_at, id) WHERE (state = 'new'::text);

CREATE INDEX charge_records_unmatched ON suppliers.charge_records USING btree (id) WHERE (state <> 'matched'::text);

CREATE INDEX ended_requests_at ON suppliers.ended_requests USING btree (ended_at);

CREATE INDEX supplier_models_unmapped ON suppliers.supplier_models USING btree (first_seen_at, supplier_group_id, upstream_name) WHERE ((model_id IS NULL) AND present);

CREATE INDEX sync_states_next_due ON suppliers.sync_states USING btree (next_due);

CREATE INDEX unit_values_of_supplier ON suppliers.unit_values USING btree (supplier_id, id DESC);

CREATE INDEX usage_records_new ON suppliers.usage_records USING btree (supplier_id, id) WHERE (state = 'new'::text);

CREATE UNIQUE INDEX usage_records_request ON suppliers.usage_records USING btree (supplier_id, request_id) WHERE (request_id IS NOT NULL);

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON suppliers.credentials FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON suppliers.supplier_logins FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER keep_codename BEFORE UPDATE OF codename ON suppliers.supplier_groups FOR EACH ROW EXECUTE FUNCTION suppliers.keep_codename();

CREATE TRIGGER unit_value_dimensions BEFORE INSERT ON suppliers.unit_values FOR EACH ROW EXECUTE FUNCTION suppliers.unit_value_dimensions();

ALTER TABLE ONLY suppliers.attempt_costs
    ADD CONSTRAINT attempt_costs_cost_schedule_id_endpoint_id_fkey FOREIGN KEY (cost_schedule_id, endpoint_id) REFERENCES suppliers.cost_schedules(id, endpoint_id);

ALTER TABLE ONLY suppliers.attempt_costs
    ADD CONSTRAINT attempt_costs_credential_id_supplier_id_fkey FOREIGN KEY (credential_id, supplier_id) REFERENCES suppliers.credentials(id, supplier_id);

ALTER TABLE ONLY suppliers.attempt_costs
    ADD CONSTRAINT attempt_costs_endpoint_id_supplier_id_fkey FOREIGN KEY (endpoint_id, supplier_id) REFERENCES suppliers.endpoints(id, supplier_id);

ALTER TABLE ONLY suppliers.charge_events
    ADD CONSTRAINT charge_events_charge_record_id_fkey FOREIGN KEY (charge_record_id) REFERENCES suppliers.charge_records(id);

ALTER TABLE ONLY suppliers.charge_records
    ADD CONSTRAINT charge_records_attempt_request_id_attempt_seq_fkey FOREIGN KEY (attempt_request_id, attempt_seq) REFERENCES suppliers.attempt_costs(request_id, attempt_seq);

ALTER TABLE ONLY suppliers.charge_records
    ADD CONSTRAINT charge_records_credential_id_supplier_id_fkey FOREIGN KEY (credential_id, supplier_id) REFERENCES suppliers.credentials(id, supplier_id);

ALTER TABLE ONLY suppliers.charge_records
    ADD CONSTRAINT charge_records_lost_request_id_fkey FOREIGN KEY (lost_request_id) REFERENCES suppliers.lost_requests(request_id);

ALTER TABLE ONLY suppliers.charge_records
    ADD CONSTRAINT charge_records_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.cost_schedules
    ADD CONSTRAINT cost_schedules_endpoint_id_supplier_id_fkey FOREIGN KEY (endpoint_id, supplier_id) REFERENCES suppliers.endpoints(id, supplier_id);

ALTER TABLE ONLY suppliers.cost_schedules
    ADD CONSTRAINT cost_schedules_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.credentials
    ADD CONSTRAINT credentials_supplier_group_id_supplier_id_fkey FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups(id, supplier_id);

ALTER TABLE ONLY suppliers.credentials
    ADD CONSTRAINT credentials_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.endpoints
    ADD CONSTRAINT endpoints_cost_schedule_fkey FOREIGN KEY (cost_schedule_id, id) REFERENCES suppliers.cost_schedules(id, endpoint_id);

ALTER TABLE ONLY suppliers.endpoints
    ADD CONSTRAINT endpoints_supplier_group_id_supplier_id_fkey FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups(id, supplier_id);

ALTER TABLE ONLY suppliers.endpoints
    ADD CONSTRAINT endpoints_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.lost_request_resolutions
    ADD CONSTRAINT lost_request_resolutions_request_id_fkey FOREIGN KEY (request_id) REFERENCES suppliers.lost_requests(request_id);

ALTER TABLE ONLY suppliers.supplier_alias_revisions
    ADD CONSTRAINT supplier_alias_revisions_alias_id_supplier_id_fkey FOREIGN KEY (alias_id, supplier_id) REFERENCES suppliers.supplier_aliases(id, supplier_id);

ALTER TABLE ONLY suppliers.supplier_aliases
    ADD CONSTRAINT supplier_aliases_supplier_group_id_supplier_id_fkey FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups(id, supplier_id);

ALTER TABLE ONLY suppliers.supplier_aliases
    ADD CONSTRAINT supplier_aliases_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.supplier_groups
    ADD CONSTRAINT supplier_groups_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.supplier_logins
    ADD CONSTRAINT supplier_logins_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.supplier_models
    ADD CONSTRAINT supplier_models_supplier_group_id_supplier_id_fkey FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups(id, supplier_id);

ALTER TABLE ONLY suppliers.supplier_models
    ADD CONSTRAINT supplier_models_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.sync_events
    ADD CONSTRAINT sync_events_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.sync_plans
    ADD CONSTRAINT sync_plans_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.sync_states
    ADD CONSTRAINT sync_states_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.unit_values
    ADD CONSTRAINT unit_values_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);

ALTER TABLE ONLY suppliers.usage_records
    ADD CONSTRAINT usage_records_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES suppliers.suppliers(id);











CREATE POLICY owner ON suppliers.suppliers USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY platform_only ON suppliers.attempt_journal USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON suppliers.charge_events USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON suppliers.charge_records USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON suppliers.rate_buckets USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON suppliers.sync_events USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON suppliers.sync_plans USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON suppliers.sync_states USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON suppliers.usage_records USING (platform.in_platform_scope());







CREATE POLICY supplier_visible ON suppliers.cost_schedules USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = cost_schedules.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.credentials USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = credentials.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.endpoints USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = endpoints.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.supplier_alias_revisions USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = supplier_alias_revisions.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.supplier_aliases USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = supplier_aliases.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.supplier_groups USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = supplier_groups.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.supplier_logins USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = supplier_logins.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.supplier_models USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = supplier_models.supplier_id))));

CREATE POLICY supplier_visible ON suppliers.unit_values USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = unit_values.supplier_id))));





CREATE POLICY tenant ON suppliers.attempt_costs USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON suppliers.ended_requests USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON suppliers.lost_request_resolutions USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON suppliers.lost_requests USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));



GRANT USAGE ON SCHEMA suppliers TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.attempt_costs TO axiom_app;

GRANT UPDATE(charge_record_id) ON TABLE suppliers.attempt_costs TO axiom_app;

GRANT UPDATE(state) ON TABLE suppliers.attempt_costs TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE suppliers.attempt_journal TO axiom_app;

GRANT UPDATE(charge_ref) ON TABLE suppliers.attempt_journal TO axiom_app;

GRANT UPDATE(committed) ON TABLE suppliers.attempt_journal TO axiom_app;

GRANT UPDATE(written_at) ON TABLE suppliers.attempt_journal TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.charge_events TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.charge_records TO axiom_app;

GRANT UPDATE(credential_id) ON TABLE suppliers.charge_records TO axiom_app;

GRANT UPDATE(state) ON TABLE suppliers.charge_records TO axiom_app;

GRANT UPDATE(attribution) ON TABLE suppliers.charge_records TO axiom_app;

GRANT UPDATE(attempt_request_id) ON TABLE suppliers.charge_records TO axiom_app;

GRANT UPDATE(attempt_seq) ON TABLE suppliers.charge_records TO axiom_app;

GRANT UPDATE(lost_request_id) ON TABLE suppliers.charge_records TO axiom_app;

GRANT UPDATE(revision) ON TABLE suppliers.charge_records TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.cost_schedules TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(ciphertext) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(nonce) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(wrapped_dek) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(key_version) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(status) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(cooldown_until) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(expires_at) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(max_concurrency) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(egress_id) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(requests_per_minute) ON TABLE suppliers.credentials TO axiom_app;

GRANT UPDATE(allowed_models) ON TABLE suppliers.credentials TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.ended_requests TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.endpoints TO axiom_app;

GRANT UPDATE(upstream_name) ON TABLE suppliers.endpoints TO axiom_app;

GRANT UPDATE(formats) ON TABLE suppliers.endpoints TO axiom_app;

GRANT UPDATE(cost_state) ON TABLE suppliers.endpoints TO axiom_app;

GRANT UPDATE(cost_state_reason) ON TABLE suppliers.endpoints TO axiom_app;

GRANT UPDATE(cost_schedule_id) ON TABLE suppliers.endpoints TO axiom_app;

GRANT UPDATE(enabled) ON TABLE suppliers.endpoints TO axiom_app;

GRANT UPDATE(quantization) ON TABLE suppliers.endpoints TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.lost_request_resolutions TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.lost_requests TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.rate_buckets TO axiom_app;

GRANT UPDATE(window_start) ON TABLE suppliers.rate_buckets TO axiom_app;

GRANT UPDATE(used) ON TABLE suppliers.rate_buckets TO axiom_app;

GRANT UPDATE(blocked_until) ON TABLE suppliers.rate_buckets TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.supplier_alias_revisions TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.supplier_aliases TO axiom_app;

GRANT UPDATE(formats) ON TABLE suppliers.supplier_aliases TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(name) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(ratio) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(facts) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(selected) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(codename) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(present) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(last_seen_at) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(pool_accounts) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(pool_failed) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(pool_usable) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(pool_headroom) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(pool_capacity) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(pool_low) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(pool_observed_at) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(key_eligible) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(key_error) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT UPDATE(key_retry_at) ON TABLE suppliers.supplier_groups TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT UPDATE(kind) ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT UPDATE(ciphertext) ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT UPDATE(nonce) ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT UPDATE(wrapped_dek) ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT UPDATE(key_version) ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT UPDATE(version) ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT UPDATE(expires_at) ON TABLE suppliers.supplier_logins TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.supplier_models TO axiom_app;

GRANT UPDATE(model_id) ON TABLE suppliers.supplier_models TO axiom_app;

GRANT UPDATE(formats) ON TABLE suppliers.supplier_models TO axiom_app;

GRANT UPDATE(present) ON TABLE suppliers.supplier_models TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(name) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(generation) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(detected_version) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(native_unit) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(settlement_currency) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(mgmt_egress_id) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(status) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(status_reason) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(config_gen) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(balance_native) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(balance_observed_at) ON TABLE suppliers.suppliers TO axiom_app;

GRANT UPDATE(low_balance_threshold) ON TABLE suppliers.suppliers TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.sync_events TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.sync_plans TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(version) ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(format) ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(state) ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(next_due) ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(lease_holder) ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(lease_fence) ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(lease_until) ON TABLE suppliers.sync_states TO axiom_app;

GRANT UPDATE(listing) ON TABLE suppliers.sync_states TO axiom_app;

GRANT SELECT,INSERT ON TABLE suppliers.unit_values TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE suppliers.usage_records TO axiom_app;



ALTER TABLE ONLY suppliers.attempt_costs FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.attempt_journal FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.charge_events FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.charge_records FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.cost_schedules FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.ended_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.endpoints FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.lost_request_resolutions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.lost_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.rate_buckets FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.supplier_alias_revisions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.supplier_aliases FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.supplier_groups FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.supplier_logins FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.supplier_models FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.suppliers FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.sync_events FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.sync_plans FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.sync_states FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.unit_values FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.usage_records FORCE ROW LEVEL SECURITY;
ALTER TABLE suppliers.attempt_costs ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.attempt_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.charge_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.charge_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.cost_schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.ended_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.lost_request_resolutions ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.lost_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.rate_buckets ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.supplier_alias_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.supplier_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.supplier_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.supplier_logins ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.supplier_models ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.suppliers ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.sync_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.sync_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.sync_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.unit_values ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.usage_records ENABLE ROW LEVEL SECURITY;
