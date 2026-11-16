-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

-- +goose StatementBegin
CREATE FUNCTION operations.audit_chain_follows() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    IF NOT (
        (NEW.seq = 1 AND NEW.prev_hash = '\x0000000000000000000000000000000000000000000000000000000000000000'::bytea)
        OR EXISTS (SELECT 1 FROM operations.audit_chain c WHERE c.seq = NEW.seq - 1 AND c.hash = NEW.prev_hash)
    ) THEN
        RAISE EXCEPTION 'link % does not follow the chain', NEW.seq USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;


ALTER FUNCTION operations.audit_chain_follows() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION operations.refuse_autonomous_money_when_paused() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM operations.cap_policies p
        WHERE p.category = NEW.category AND p.site_id IS NOT DISTINCT FROM NEW.site_id AND p.paused_at IS NOT NULL
    ) AND NOT EXISTS (
        SELECT 1 FROM operations.operations o WHERE o.id = NEW.operation_id AND o.state = 'pending_approval'
    ) THEN
        RAISE EXCEPTION 'the caps of this scope are paused: a proposal waits for the owner' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;


ALTER FUNCTION operations.refuse_autonomous_money_when_paused() OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE operations.agent_limits (
    singleton boolean DEFAULT true NOT NULL,
    alerts_per_hour integer NOT NULL,
    rev bigint NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    requests_per_minute integer,
    money_writes_per_minute integer,
    money_writes_per_hour integer,
    CONSTRAINT agent_limits_alerts_per_hour_check CHECK (((alerts_per_hour >= 1) AND (alerts_per_hour <= 60))),
    CONSTRAINT agent_limits_money_writes_per_hour_check CHECK (((money_writes_per_hour >= 1) AND (money_writes_per_hour <= 300))),
    CONSTRAINT agent_limits_money_writes_per_minute_check CHECK (((money_writes_per_minute >= 1) AND (money_writes_per_minute <= 30))),
    CONSTRAINT agent_limits_requests_per_minute_check CHECK (((requests_per_minute >= 1) AND (requests_per_minute <= 600))),
    CONSTRAINT agent_limits_rev_check CHECK ((rev >= 1)),
    CONSTRAINT agent_limits_singleton_check CHECK (singleton)
);



ALTER TABLE operations.agent_limits OWNER TO axiom_owner;

CREATE TABLE operations.approvals (
    id uuid NOT NULL,
    operation_id uuid NOT NULL,
    tier text NOT NULL,
    state text NOT NULL,
    summary json NOT NULL,
    requested_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    decided_by text,
    decision_reason text,
    reasons text[],
    CONSTRAINT approvals_check CHECK ((expires_at > requested_at)),
    CONSTRAINT approvals_check1 CHECK (((state = 'pending'::text) = (decided_at IS NULL))),
    CONSTRAINT approvals_check2 CHECK (((state = 'pending'::text) = (decided_by IS NULL))),
    CONSTRAINT approvals_decided_by_check CHECK (((char_length(decided_by) >= 1) AND (char_length(decided_by) <= 128))),
    CONSTRAINT approvals_decision_reason_check CHECK (((char_length(decision_reason) >= 1) AND (char_length(decision_reason) <= 1000))),
    CONSTRAINT approvals_reasons_check CHECK (((cardinality(reasons) >= 1) AND (cardinality(reasons) <= 16))),
    CONSTRAINT approvals_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'expired'::text, 'canceled'::text]))),
    CONSTRAINT approvals_summary_check CHECK ((octet_length((summary)::text) <= 8192)),
    CONSTRAINT approvals_tier_check CHECK ((tier = 'human'::text))
);



ALTER TABLE operations.approvals OWNER TO axiom_owner;

CREATE TABLE operations.audit_chain (
    seq bigint NOT NULL,
    entry_id uuid NOT NULL,
    at timestamptz NOT NULL,
    prev_hash bytea NOT NULL,
    hash bytea NOT NULL,
    chained_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT audit_chain_hash_check CHECK ((octet_length(hash) = 32)),
    CONSTRAINT audit_chain_prev_hash_check CHECK ((octet_length(prev_hash) = 32)),
    CONSTRAINT audit_chain_seq_check CHECK ((seq >= 1))
);



ALTER TABLE operations.audit_chain OWNER TO axiom_owner;

CREATE TABLE operations.audit_chain_checks (
    singleton boolean DEFAULT true NOT NULL,
    target_seq bigint NOT NULL,
    next_seq bigint NOT NULL,
    started_at timestamptz DEFAULT now() NOT NULL,
    finished_at timestamptz,
    last_finished_at timestamptz,
    CONSTRAINT audit_chain_checks_next_seq_check CHECK ((next_seq >= 1)),
    CONSTRAINT audit_chain_checks_singleton_check CHECK (singleton),
    CONSTRAINT audit_chain_checks_target_seq_check CHECK ((target_seq >= 0))
);



ALTER TABLE operations.audit_chain_checks OWNER TO axiom_owner;

CREATE TABLE operations.audit_chain_faults (
    id uuid DEFAULT uuidv7() NOT NULL,
    found_at timestamptz DEFAULT now() NOT NULL,
    kind text NOT NULL,
    seq bigint NOT NULL,
    entry_id uuid NOT NULL,
    CONSTRAINT audit_chain_faults_kind_check CHECK ((kind = ANY (ARRAY['late'::text, 'missing'::text, 'mismatch'::text, 'broken'::text])))
);



ALTER TABLE operations.audit_chain_faults OWNER TO axiom_owner;

CREATE TABLE operations.audit_digests (
    day date NOT NULL,
    body jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);



ALTER TABLE operations.audit_digests OWNER TO axiom_owner;

CREATE TABLE operations.audit_replica (
    target text NOT NULL,
    replicated_seq bigint NOT NULL,
    object_key text,
    pending_last bigint,
    replicated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT audit_replica_check CHECK ((pending_last > replicated_seq)),
    CONSTRAINT audit_replica_object_key_check CHECK (((char_length(object_key) >= 1) AND (char_length(object_key) <= 1024))),
    CONSTRAINT audit_replica_replicated_seq_check CHECK ((replicated_seq >= 0)),
    CONSTRAINT audit_replica_target_check CHECK (((char_length(target) >= 1) AND (char_length(target) <= 2048)))
);



ALTER TABLE operations.audit_replica OWNER TO axiom_owner;

CREATE TABLE operations.cap_counters (
    id bigint NOT NULL,
    category text NOT NULL,
    window_kind text NOT NULL,
    window_start timestamptz NOT NULL,
    object_kind text DEFAULT ''::text NOT NULL,
    object_id text DEFAULT ''::text NOT NULL,
    site_id uuid,
    reserved bigint DEFAULT 0 NOT NULL,
    committed bigint DEFAULT 0 NOT NULL,
    count integer DEFAULT 0 NOT NULL,
    CONSTRAINT cap_counters_check CHECK (((object_kind = ''::text) OR (window_kind = 'day'::text))),
    CONSTRAINT cap_counters_committed_check CHECK ((committed >= 0)),
    CONSTRAINT cap_counters_count_check CHECK ((count >= 0)),
    CONSTRAINT cap_counters_reserved_check CHECK ((reserved >= 0)),
    CONSTRAINT cap_counters_window_kind_check CHECK ((window_kind = ANY (ARRAY['day'::text, 'week'::text])))
);



ALTER TABLE operations.cap_counters OWNER TO axiom_owner;

ALTER TABLE operations.cap_counters ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME operations.cap_counters_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE operations.cap_policies (
    site_id uuid,
    category text NOT NULL,
    currency text NOT NULL,
    single_max bigint NOT NULL,
    daily_max bigint NOT NULL,
    weekly_max bigint NOT NULL,
    object_daily_max bigint NOT NULL,
    daily_count_max integer NOT NULL,
    hard_single_max bigint NOT NULL,
    hard_daily_max bigint NOT NULL,
    hard_weekly_max bigint NOT NULL,
    rev bigint NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    paused_at timestamptz,
    CONSTRAINT cap_policies_category_check CHECK ((category = ANY (ARRAY['money.top_up'::text, 'money.credit'::text, 'money.refund'::text]))),
    CONSTRAINT cap_policies_check CHECK ((hard_single_max >= single_max)),
    CONSTRAINT cap_policies_check1 CHECK ((hard_daily_max >= daily_max)),
    CONSTRAINT cap_policies_check2 CHECK ((hard_weekly_max >= weekly_max)),
    CONSTRAINT cap_policies_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT cap_policies_daily_count_max_check CHECK ((daily_count_max >= 0)),
    CONSTRAINT cap_policies_daily_max_check CHECK ((daily_max >= 0)),
    CONSTRAINT cap_policies_object_daily_max_check CHECK ((object_daily_max >= 0)),
    CONSTRAINT cap_policies_rev_check CHECK ((rev >= 1)),
    CONSTRAINT cap_policies_single_max_check CHECK ((single_max >= 0)),
    CONSTRAINT cap_policies_weekly_max_check CHECK ((weekly_max >= 0))
);



ALTER TABLE operations.cap_policies OWNER TO axiom_owner;

CREATE TABLE operations.commercial_targets (
    site_id uuid NOT NULL,
    metric text NOT NULL,
    month date NOT NULL,
    amount numeric(38,0) NOT NULL,
    currency text,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT commercial_targets_amount_check CHECK ((amount >= (0)::numeric)),
    CONSTRAINT commercial_targets_check CHECK (((metric = 'monthly_revenue'::text) = (currency IS NOT NULL))),
    CONSTRAINT commercial_targets_check1 CHECK (((metric <> ALL (ARRAY['gross_margin'::text, 'retention'::text])) OR (amount <= (10000)::numeric))),
    CONSTRAINT commercial_targets_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT commercial_targets_metric_check CHECK ((metric = ANY (ARRAY['paid_users'::text, 'monthly_revenue'::text, 'gross_margin'::text, 'retention'::text]))),
    CONSTRAINT commercial_targets_month_check CHECK ((EXTRACT(day FROM month) = (1)::numeric))
);



ALTER TABLE operations.commercial_targets OWNER TO axiom_owner;

CREATE TABLE operations.money_actions (
    operation_id uuid NOT NULL,
    category text NOT NULL,
    site_id uuid,
    object_kind text NOT NULL,
    object_id text NOT NULL,
    amount bigint NOT NULL,
    currency text NOT NULL,
    basis json NOT NULL,
    policy_rev bigint NOT NULL,
    day_start timestamptz NOT NULL,
    week_start timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL,
    proof text,
    confirmed_by uuid,
    settled_at timestamptz,
    CONSTRAINT money_actions_amount_check CHECK ((amount > 0)),
    CONSTRAINT money_actions_basis_check CHECK ((octet_length((basis)::text) <= 4096)),
    CONSTRAINT money_actions_category_check CHECK ((category = ANY (ARRAY['money.top_up'::text, 'money.credit'::text, 'money.refund'::text]))),
    CONSTRAINT money_actions_check CHECK (((state = 'committed'::text) = ((proof IS NOT NULL) AND (confirmed_by IS NOT NULL)))),
    CONSTRAINT money_actions_check1 CHECK (((state = 'reserved'::text) = (settled_at IS NULL))),
    CONSTRAINT money_actions_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT money_actions_object_id_check CHECK (((char_length(object_id) >= 1) AND (char_length(object_id) <= 128))),
    CONSTRAINT money_actions_object_kind_check CHECK ((object_kind ~ '^[a-z][a-z0-9_]{0,63}$'::text)),
    CONSTRAINT money_actions_proof_check CHECK (((char_length(proof) >= 1) AND (char_length(proof) <= 500))),
    CONSTRAINT money_actions_state_check CHECK ((state = ANY (ARRAY['reserved'::text, 'committed'::text, 'released'::text])))
);



ALTER TABLE operations.money_actions OWNER TO axiom_owner;

CREATE TABLE operations.operation_log (
    id uuid NOT NULL,
    credential_id uuid NOT NULL,
    run_id text,
    tool text NOT NULL,
    reason text NOT NULL,
    input json NOT NULL,
    status smallint NOT NULL,
    error_code text,
    request_id text NOT NULL,
    started_at timestamptz NOT NULL,
    ended_at timestamptz NOT NULL,
    operation_id uuid,
    logged_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT operation_log_check CHECK ((ended_at >= started_at)),
    CONSTRAINT operation_log_error_code_check CHECK ((error_code ~ '^[a-z][a-z0-9_]{0,63}$'::text)),
    CONSTRAINT operation_log_input_check CHECK ((octet_length((input)::text) <= 8192)),
    CONSTRAINT operation_log_reason_check CHECK ((char_length(reason) <= 1000)),
    CONSTRAINT operation_log_run_id_check CHECK ((run_id ~ '^[A-Za-z0-9._:-]{1,64}$'::text)),
    CONSTRAINT operation_log_status_check CHECK (((status >= 100) AND (status <= 599))),
    CONSTRAINT operation_log_tool_check CHECK ((tool ~ '^[a-z][a-z0-9_]{0,63}$'::text))
);



ALTER TABLE operations.operation_log OWNER TO axiom_owner;

CREATE TABLE operations.operations (
    id uuid NOT NULL,
    credential_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash bytea NOT NULL,
    tool text NOT NULL,
    target_kind text NOT NULL,
    target_id text NOT NULL,
    params json NOT NULL,
    risk text NOT NULL,
    state text NOT NULL,
    reason text NOT NULL,
    run_id text,
    result json,
    error_code text,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT operations_error_code_check CHECK ((error_code ~ '^[a-z][a-z0-9_]{0,63}$'::text)),
    CONSTRAINT operations_params_check CHECK ((octet_length((params)::text) <= 8192)),
    CONSTRAINT operations_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 1000))),
    CONSTRAINT operations_request_hash_check CHECK ((octet_length(request_hash) = 32)),
    CONSTRAINT operations_result_check CHECK ((octet_length((result)::text) <= 8192)),
    CONSTRAINT operations_risk_check CHECK ((risk = ANY (ARRAY['routine'::text, 'delisting'::text, 'top_up'::text, 'price'::text, 'gift'::text, 'refund'::text]))),
    CONSTRAINT operations_run_id_check CHECK ((run_id ~ '^[A-Za-z0-9._:-]{1,64}$'::text)),
    CONSTRAINT operations_state_check CHECK ((state = ANY (ARRAY['pending_approval'::text, 'approved'::text, 'executing'::text, 'succeeded'::text, 'failed'::text, 'unknown'::text, 'canceled'::text, 'expired'::text, 'rejected'::text, 'reverted'::text]))),
    CONSTRAINT operations_target_id_check CHECK (((char_length(target_id) >= 1) AND (char_length(target_id) <= 128))),
    CONSTRAINT operations_target_kind_check CHECK ((target_kind ~ '^[a-z][a-z0-9_]{0,63}$'::text)),
    CONSTRAINT operations_tool_check CHECK ((tool ~ '^[a-z][a-z0-9_]{0,63}$'::text))
);



ALTER TABLE operations.operations OWNER TO axiom_owner;

CREATE TABLE operations.price_counters (
    site_id uuid NOT NULL,
    day_start timestamptz NOT NULL,
    model_id uuid,
    change_bps bigint DEFAULT 0 NOT NULL,
    count integer DEFAULT 0 NOT NULL,
    models integer DEFAULT 0 NOT NULL,
    CONSTRAINT price_counters_change_bps_check CHECK ((change_bps >= 0)),
    CONSTRAINT price_counters_check CHECK (((model_id IS NULL) OR (models = 0))),
    CONSTRAINT price_counters_check1 CHECK (((model_id IS NOT NULL) OR (change_bps = 0))),
    CONSTRAINT price_counters_count_check CHECK ((count >= 0)),
    CONSTRAINT price_counters_models_check CHECK ((models >= 0))
);



ALTER TABLE operations.price_counters OWNER TO axiom_owner;

CREATE TABLE operations.price_envelopes (
    site_id uuid NOT NULL,
    min_margin_bps integer NOT NULL,
    model_daily_change_bps bigint NOT NULL,
    daily_changes integer NOT NULL,
    daily_models integer NOT NULL,
    rev bigint NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT price_envelopes_daily_changes_check CHECK ((daily_changes >= 0)),
    CONSTRAINT price_envelopes_daily_models_check CHECK ((daily_models >= 0)),
    CONSTRAINT price_envelopes_min_margin_bps_check CHECK (((min_margin_bps >= 0) AND (min_margin_bps <= 10000))),
    CONSTRAINT price_envelopes_model_daily_change_bps_check CHECK (((model_daily_change_bps >= 0) AND (model_daily_change_bps <= 100000000))),
    CONSTRAINT price_envelopes_rev_check CHECK ((rev >= 1))
);



ALTER TABLE operations.price_envelopes OWNER TO axiom_owner;

CREATE TABLE operations.target_revisions (
    site_id uuid NOT NULL,
    month date NOT NULL,
    revision bigint NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT target_revisions_month_check CHECK ((EXTRACT(day FROM month) = (1)::numeric)),
    CONSTRAINT target_revisions_revision_check CHECK ((revision >= 1))
);



ALTER TABLE operations.target_revisions OWNER TO axiom_owner;

CREATE TABLE operations.targets (
    site_id uuid NOT NULL,
    metric text NOT NULL,
    month date NOT NULL,
    amount numeric(38,0) NOT NULL,
    currency text,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT targets_amount_check CHECK ((amount >= (0)::numeric)),
    CONSTRAINT targets_check CHECK (((metric = ANY (ARRAY['revenue'::text, 'cost'::text])) = (currency IS NOT NULL))),
    CONSTRAINT targets_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT targets_metric_check CHECK ((metric = ANY (ARRAY['calls'::text, 'errors'::text, 'revenue'::text, 'cost'::text]))),
    CONSTRAINT targets_month_check CHECK ((EXTRACT(day FROM month) = (1)::numeric))
);



ALTER TABLE operations.targets OWNER TO axiom_owner;

ALTER TABLE ONLY operations.agent_limits
    ADD CONSTRAINT agent_limits_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY operations.approvals
    ADD CONSTRAINT approvals_operation_id_key UNIQUE (operation_id);

ALTER TABLE ONLY operations.approvals
    ADD CONSTRAINT approvals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY operations.audit_chain_checks
    ADD CONSTRAINT audit_chain_checks_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY operations.audit_chain
    ADD CONSTRAINT audit_chain_entry_id_key UNIQUE (entry_id);

ALTER TABLE ONLY operations.audit_chain_faults
    ADD CONSTRAINT audit_chain_faults_pkey PRIMARY KEY (id);

ALTER TABLE ONLY operations.audit_chain
    ADD CONSTRAINT audit_chain_pkey PRIMARY KEY (seq);

ALTER TABLE ONLY operations.audit_digests
    ADD CONSTRAINT audit_digests_pkey PRIMARY KEY (day);

ALTER TABLE ONLY operations.audit_replica
    ADD CONSTRAINT audit_replica_pkey PRIMARY KEY (target);

ALTER TABLE ONLY operations.cap_counters
    ADD CONSTRAINT cap_counters_category_window_kind_window_start_object_kind__key UNIQUE NULLS NOT DISTINCT (category, window_kind, window_start, object_kind, object_id, site_id);

ALTER TABLE ONLY operations.cap_counters
    ADD CONSTRAINT cap_counters_pkey PRIMARY KEY (id);

ALTER TABLE ONLY operations.cap_policies
    ADD CONSTRAINT cap_policies_category_site_id_key UNIQUE NULLS NOT DISTINCT (category, site_id);

ALTER TABLE ONLY operations.commercial_targets
    ADD CONSTRAINT commercial_targets_pkey PRIMARY KEY (site_id, metric, month);

ALTER TABLE ONLY operations.money_actions
    ADD CONSTRAINT money_actions_pkey PRIMARY KEY (operation_id);

ALTER TABLE ONLY operations.operation_log
    ADD CONSTRAINT operation_log_pkey PRIMARY KEY (id);

ALTER TABLE ONLY operations.operations
    ADD CONSTRAINT operations_credential_id_idempotency_key_key UNIQUE (credential_id, idempotency_key);

ALTER TABLE ONLY operations.operations
    ADD CONSTRAINT operations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY operations.price_counters
    ADD CONSTRAINT price_counters_site_id_day_start_model_id_key UNIQUE NULLS NOT DISTINCT (site_id, day_start, model_id);

ALTER TABLE ONLY operations.price_envelopes
    ADD CONSTRAINT price_envelopes_pkey PRIMARY KEY (site_id);

ALTER TABLE ONLY operations.target_revisions
    ADD CONSTRAINT target_revisions_pkey PRIMARY KEY (site_id, month);

ALTER TABLE ONLY operations.targets
    ADD CONSTRAINT targets_pkey PRIMARY KEY (site_id, metric, month);

CREATE INDEX approvals_by_requested ON operations.approvals USING btree (requested_at);

CREATE INDEX approvals_due ON operations.approvals USING btree (expires_at) WHERE (state = 'pending'::text);

CREATE INDEX approvals_newest ON operations.approvals USING btree (id DESC);

CREATE INDEX commercial_targets_by_month ON operations.commercial_targets USING btree (site_id, month);

CREATE INDEX money_actions_due ON operations.money_actions USING btree (expires_at) WHERE (state = 'reserved'::text);

CREATE INDEX money_actions_week ON operations.money_actions USING btree (week_start);

CREATE INDEX operation_log_of_credential ON operations.operation_log USING btree (credential_id, id DESC);

CREATE INDEX operation_log_refused ON operations.operation_log USING btree (logged_at) WHERE ((status >= 400) AND (status <= 499));

CREATE INDEX operations_by_created ON operations.operations USING btree (created_at);

CREATE INDEX operations_executing ON operations.operations USING btree (tool, id) WHERE (state = 'executing'::text);

CREATE INDEX operations_of_credential ON operations.operations USING btree (credential_id, id DESC);

CREATE INDEX operations_pending ON operations.operations USING btree (credential_id) WHERE (state = 'pending_approval'::text);

CREATE INDEX targets_by_month ON operations.targets USING btree (site_id, month);

CREATE TRIGGER audit_chain_follows BEFORE INSERT ON operations.audit_chain FOR EACH ROW EXECUTE FUNCTION operations.audit_chain_follows();

CREATE TRIGGER refuse_autonomous_money_when_paused BEFORE INSERT ON operations.money_actions FOR EACH ROW EXECUTE FUNCTION operations.refuse_autonomous_money_when_paused();

ALTER TABLE ONLY operations.approvals
    ADD CONSTRAINT approvals_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES operations.operations(id);

ALTER TABLE ONLY operations.money_actions
    ADD CONSTRAINT money_actions_confirmed_by_fkey FOREIGN KEY (confirmed_by) REFERENCES operations.operations(id);

ALTER TABLE ONLY operations.money_actions
    ADD CONSTRAINT money_actions_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES operations.operations(id);











CREATE POLICY deletes ON operations.commercial_targets FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY deletes ON operations.targets FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON operations.agent_limits FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.approvals FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.audit_chain FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.audit_chain_checks FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.audit_chain_faults FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.audit_digests FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.audit_replica FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.cap_counters FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.cap_policies FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.commercial_targets FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON operations.money_actions FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.operation_log FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.operations FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.price_counters FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.price_envelopes FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON operations.target_revisions FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON operations.targets FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));






CREATE POLICY reads ON operations.agent_limits FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.approvals FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.audit_chain FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.audit_chain_checks FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.audit_chain_faults FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.audit_digests FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.audit_replica FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.cap_counters FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.cap_policies FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.commercial_targets FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON operations.money_actions FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.operation_log FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.operations FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.price_counters FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.price_envelopes FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON operations.target_revisions FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON operations.targets FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));



CREATE POLICY updates ON operations.agent_limits FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.approvals FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.audit_chain_checks FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.audit_replica FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.cap_counters FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.cap_policies FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.commercial_targets FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON operations.money_actions FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.operations FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.price_counters FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.price_envelopes FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON operations.target_revisions FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON operations.targets FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

GRANT USAGE ON SCHEMA operations TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.agent_limits TO axiom_app;

GRANT UPDATE(alerts_per_hour) ON TABLE operations.agent_limits TO axiom_app;

GRANT UPDATE(rev) ON TABLE operations.agent_limits TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE operations.agent_limits TO axiom_app;

GRANT UPDATE(requests_per_minute) ON TABLE operations.agent_limits TO axiom_app;

GRANT UPDATE(money_writes_per_minute) ON TABLE operations.agent_limits TO axiom_app;

GRANT UPDATE(money_writes_per_hour) ON TABLE operations.agent_limits TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.approvals TO axiom_app;

GRANT UPDATE(state) ON TABLE operations.approvals TO axiom_app;

GRANT UPDATE(decided_at) ON TABLE operations.approvals TO axiom_app;

GRANT UPDATE(decided_by) ON TABLE operations.approvals TO axiom_app;

GRANT UPDATE(decision_reason) ON TABLE operations.approvals TO axiom_app;

GRANT SELECT ON TABLE operations.audit_chain TO axiom_app;

GRANT INSERT(seq) ON TABLE operations.audit_chain TO axiom_app;

GRANT INSERT(entry_id) ON TABLE operations.audit_chain TO axiom_app;

GRANT INSERT(at) ON TABLE operations.audit_chain TO axiom_app;

GRANT INSERT(prev_hash) ON TABLE operations.audit_chain TO axiom_app;

GRANT INSERT(hash) ON TABLE operations.audit_chain TO axiom_app;

GRANT SELECT ON TABLE operations.audit_chain_checks TO axiom_app;

GRANT INSERT(target_seq),UPDATE(target_seq) ON TABLE operations.audit_chain_checks TO axiom_app;

GRANT INSERT(next_seq),UPDATE(next_seq) ON TABLE operations.audit_chain_checks TO axiom_app;

GRANT UPDATE(started_at) ON TABLE operations.audit_chain_checks TO axiom_app;

GRANT UPDATE(finished_at) ON TABLE operations.audit_chain_checks TO axiom_app;

GRANT UPDATE(last_finished_at) ON TABLE operations.audit_chain_checks TO axiom_app;

GRANT SELECT ON TABLE operations.audit_chain_faults TO axiom_app;

GRANT INSERT(kind) ON TABLE operations.audit_chain_faults TO axiom_app;

GRANT INSERT(seq) ON TABLE operations.audit_chain_faults TO axiom_app;

GRANT INSERT(entry_id) ON TABLE operations.audit_chain_faults TO axiom_app;

GRANT SELECT ON TABLE operations.audit_digests TO axiom_app;

GRANT INSERT(day) ON TABLE operations.audit_digests TO axiom_app;

GRANT INSERT(body) ON TABLE operations.audit_digests TO axiom_app;

GRANT SELECT ON TABLE operations.audit_replica TO axiom_app;

GRANT INSERT(target) ON TABLE operations.audit_replica TO axiom_app;

GRANT INSERT(replicated_seq),UPDATE(replicated_seq) ON TABLE operations.audit_replica TO axiom_app;

GRANT UPDATE(object_key) ON TABLE operations.audit_replica TO axiom_app;

GRANT INSERT(pending_last),UPDATE(pending_last) ON TABLE operations.audit_replica TO axiom_app;

GRANT UPDATE(replicated_at) ON TABLE operations.audit_replica TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.cap_counters TO axiom_app;

GRANT UPDATE(reserved) ON TABLE operations.cap_counters TO axiom_app;

GRANT UPDATE(committed) ON TABLE operations.cap_counters TO axiom_app;

GRANT UPDATE(count) ON TABLE operations.cap_counters TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(single_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(daily_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(weekly_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(object_daily_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(daily_count_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(hard_single_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(hard_daily_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(hard_weekly_max) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(rev) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE operations.cap_policies TO axiom_app;

GRANT UPDATE(paused_at) ON TABLE operations.cap_policies TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE operations.commercial_targets TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.money_actions TO axiom_app;

GRANT UPDATE(state) ON TABLE operations.money_actions TO axiom_app;

GRANT UPDATE(proof) ON TABLE operations.money_actions TO axiom_app;

GRANT UPDATE(confirmed_by) ON TABLE operations.money_actions TO axiom_app;

GRANT UPDATE(settled_at) ON TABLE operations.money_actions TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.operation_log TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.operations TO axiom_app;

GRANT UPDATE(state) ON TABLE operations.operations TO axiom_app;

GRANT UPDATE(result) ON TABLE operations.operations TO axiom_app;

GRANT UPDATE(error_code) ON TABLE operations.operations TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE operations.operations TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.price_counters TO axiom_app;

GRANT UPDATE(change_bps) ON TABLE operations.price_counters TO axiom_app;

GRANT UPDATE(count) ON TABLE operations.price_counters TO axiom_app;

GRANT UPDATE(models) ON TABLE operations.price_counters TO axiom_app;

GRANT SELECT,INSERT ON TABLE operations.price_envelopes TO axiom_app;

GRANT UPDATE(min_margin_bps) ON TABLE operations.price_envelopes TO axiom_app;

GRANT UPDATE(model_daily_change_bps) ON TABLE operations.price_envelopes TO axiom_app;

GRANT UPDATE(daily_changes) ON TABLE operations.price_envelopes TO axiom_app;

GRANT UPDATE(daily_models) ON TABLE operations.price_envelopes TO axiom_app;

GRANT UPDATE(rev) ON TABLE operations.price_envelopes TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE operations.price_envelopes TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE operations.target_revisions TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE operations.targets TO axiom_app;



ALTER TABLE ONLY operations.agent_limits FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.approvals FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.audit_chain FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.audit_chain_checks FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.audit_chain_faults FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.audit_digests FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.audit_replica FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.cap_counters FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.cap_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.commercial_targets FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.money_actions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.operation_log FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.operations FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.price_counters FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.price_envelopes FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.target_revisions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY operations.targets FORCE ROW LEVEL SECURITY;
ALTER TABLE operations.agent_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.audit_chain ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.audit_chain_checks ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.audit_chain_faults ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.audit_digests ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.audit_replica ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.cap_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.cap_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.commercial_targets ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.money_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.operation_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.price_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.price_envelopes ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.target_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE operations.targets ENABLE ROW LEVEL SECURITY;
