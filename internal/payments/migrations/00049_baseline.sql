-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

-- +goose StatementBegin
CREATE FUNCTION payments.refund_second_required() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE
    limit_credits bigint;
BEGIN
    IF NEW.second_required IS NULL THEN
        -- A site that never saved settings has no row, and its limit is the code's default 0. The scalar subquery keeps
        -- COALESCE seeing that case; SELECT INTO would leave the variable NULL.
        limit_credits := COALESCE((
            SELECT s.refund_second_approval_credits
            FROM payments.settings AS s
            WHERE s.site_id = NEW.site_id
        ), 0);
        NEW.second_required := limit_credits > 0 AND NEW.credits > limit_credits;
    END IF;
    RETURN NEW;
END;
$$;


ALTER FUNCTION payments.refund_second_required() OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE payments.discrepancies (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid,
    order_id uuid,
    merchant_id uuid NOT NULL,
    kind text NOT NULL,
    dedupe text NOT NULL,
    detail jsonb NOT NULL,
    state text DEFAULT 'open'::text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    investigated_by text,
    investigated_at timestamptz,
    note text,
    CONSTRAINT discrepancies_check CHECK (((order_id IS NULL) = (account_id IS NULL))),
    CONSTRAINT discrepancies_note_check CHECK (((char_length(note) >= 1) AND (char_length(note) <= 1000))),
    CONSTRAINT discrepancies_state_check CHECK ((state = ANY (ARRAY['open'::text, 'resolved'::text]))),
    CONSTRAINT investigation_complete CHECK ((((investigated_by IS NULL) AND (investigated_at IS NULL) AND (note IS NULL)) OR ((state = 'resolved'::text) AND (investigated_by IS NOT NULL) AND (investigated_at IS NOT NULL) AND (note IS NOT NULL))))
);



ALTER TABLE payments.discrepancies OWNER TO axiom_owner;

CREATE TABLE payments.ip_usage (
    site_id uuid NOT NULL,
    ip inet NOT NULL,
    window_start timestamptz NOT NULL,
    orders bigint NOT NULL,
    failures bigint NOT NULL,
    CONSTRAINT ip_usage_failures_check CHECK ((failures >= 0)),
    CONSTRAINT ip_usage_orders_check CHECK ((orders >= 0))
);



ALTER TABLE payments.ip_usage OWNER TO axiom_owner;

CREATE TABLE payments.merchant_configs (
    merchant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    settings text NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    rev bigint NOT NULL,
    CONSTRAINT merchant_configs_ciphertext_check CHECK ((octet_length(ciphertext) > 0)),
    CONSTRAINT merchant_configs_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT merchant_configs_nonce_check CHECK ((octet_length(nonce) > 0)),
    CONSTRAINT merchant_configs_rev_check CHECK ((rev > 0)),
    CONSTRAINT merchant_configs_settings_check CHECK (((octet_length(settings) >= 2) AND (octet_length(settings) <= 65536))),
    CONSTRAINT merchant_configs_wrapped_dek_check CHECK ((octet_length(wrapped_dek) > 0))
);



ALTER TABLE payments.merchant_configs OWNER TO axiom_owner;

CREATE TABLE payments.merchant_methods (
    merchant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    method text NOT NULL,
    upstream text NOT NULL,
    fee_ppm integer NOT NULL,
    fee_fixed bigint NOT NULL,
    single_min bigint,
    single_max bigint,
    daily_limit bigint,
    enabled boolean NOT NULL,
    CONSTRAINT merchant_methods_check CHECK ((single_min <= single_max)),
    CONSTRAINT merchant_methods_daily_limit_check CHECK ((daily_limit > 0)),
    CONSTRAINT merchant_methods_fee_fixed_check CHECK ((fee_fixed >= 0)),
    CONSTRAINT merchant_methods_fee_ppm_check CHECK (((fee_ppm >= 0) AND (fee_ppm <= 500000))),
    CONSTRAINT merchant_methods_method_check CHECK ((method ~ '^[a-z][a-z0-9_.]{0,31}$'::text)),
    CONSTRAINT merchant_methods_single_max_check CHECK ((single_max > 0)),
    CONSTRAINT merchant_methods_single_min_check CHECK ((single_min > 0)),
    CONSTRAINT merchant_methods_upstream_check CHECK (((char_length(upstream) >= 1) AND (char_length(upstream) <= 64)))
);



ALTER TABLE payments.merchant_methods OWNER TO axiom_owner;

CREATE TABLE payments.merchant_usage (
    merchant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    day date NOT NULL,
    used bigint NOT NULL,
    CONSTRAINT merchant_usage_used_check CHECK ((used >= 0))
);



ALTER TABLE payments.merchant_usage OWNER TO axiom_owner;

CREATE TABLE payments.merchants (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    enabled boolean NOT NULL,
    currency text NOT NULL,
    ttl_seconds integer NOT NULL,
    refund_enabled boolean NOT NULL,
    user_refund boolean NOT NULL,
    sort integer NOT NULL,
    rev bigint NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT merchants_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT merchants_kind_check CHECK ((kind ~ '^[a-z][a-z0-9_]{0,31}$'::text)),
    CONSTRAINT merchants_name_check CHECK (((char_length(btrim(name)) >= 1) AND (char_length(btrim(name)) <= 100))),
    CONSTRAINT merchants_rev_check CHECK ((rev > 0)),
    CONSTRAINT merchants_ttl_seconds_check CHECK (((ttl_seconds >= 60) AND (ttl_seconds <= 86400)))
);



ALTER TABLE payments.merchants OWNER TO axiom_owner;

CREATE TABLE payments.order_disputes (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    order_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider text NOT NULL,
    provider_id text NOT NULL,
    payment_intent text DEFAULT ''::text NOT NULL,
    amount bigint NOT NULL,
    currency text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    status text NOT NULL,
    state text DEFAULT 'recorded'::text NOT NULL,
    credits bigint DEFAULT 0 NOT NULL,
    cash bigint DEFAULT 0 NOT NULL,
    fee bigint DEFAULT 0 NOT NULL,
    chargeback_revision bigint DEFAULT 0 NOT NULL,
    opened_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    applied_at timestamptz,
    reversed_at timestamptz,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    suspended_at timestamptz,
    CONSTRAINT order_disputes_amount_check CHECK ((amount > 0)),
    CONSTRAINT order_disputes_cash_check CHECK ((cash >= 0)),
    CONSTRAINT order_disputes_chargeback_revision_check CHECK ((chargeback_revision >= 0)),
    CONSTRAINT order_disputes_check CHECK (((fee >= 0) AND (fee <= cash))),
    CONSTRAINT order_disputes_check1 CHECK (((state = 'recorded'::text) = (applied_at IS NULL))),
    CONSTRAINT order_disputes_check2 CHECK (((state <> 'applied'::text) OR (reversed_at IS NULL))),
    CONSTRAINT order_disputes_check3 CHECK (((state <> 'reversed'::text) OR (reversed_at IS NOT NULL))),
    CONSTRAINT order_disputes_check4 CHECK (((state = 'recorded'::text) = (chargeback_revision = 0))),
    CONSTRAINT order_disputes_credits_check CHECK ((credits >= 0)),
    CONSTRAINT order_disputes_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT order_disputes_payment_intent_check CHECK ((octet_length(payment_intent) <= 1024)),
    CONSTRAINT order_disputes_provider_check CHECK ((provider ~ '^[a-z][a-z0-9_]{0,31}$'::text)),
    CONSTRAINT order_disputes_provider_id_check CHECK (((octet_length(provider_id) >= 1) AND (octet_length(provider_id) <= 255))),
    CONSTRAINT order_disputes_reason_check CHECK ((char_length(reason) <= 64)),
    CONSTRAINT order_disputes_state_check CHECK ((state = ANY (ARRAY['recorded'::text, 'applied'::text, 'reversed'::text]))),
    CONSTRAINT order_disputes_status_check CHECK (((char_length(status) >= 1) AND (char_length(status) <= 64)))
);



ALTER TABLE payments.order_disputes OWNER TO axiom_owner;

CREATE TABLE payments.order_events (
    id uuid NOT NULL,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    at timestamptz DEFAULT clock_timestamp() NOT NULL,
    source text NOT NULL,
    kind text NOT NULL,
    from_state text NOT NULL,
    to_state text NOT NULL,
    trade_no text DEFAULT ''::text NOT NULL,
    amount bigint,
    raw text DEFAULT ''::text NOT NULL,
    CONSTRAINT order_events_amount_check CHECK ((amount > 0)),
    CONSTRAINT order_events_kind_check CHECK (((char_length(kind) >= 1) AND (char_length(kind) <= 64))),
    CONSTRAINT order_events_raw_check CHECK ((octet_length(raw) <= 8192)),
    CONSTRAINT order_events_source_check CHECK ((source = ANY (ARRAY['notify'::text, 'query'::text, 'user'::text, 'admin'::text, 'job'::text])))
);



ALTER TABLE payments.order_events OWNER TO axiom_owner;

CREATE TABLE payments.order_payments (
    id uuid NOT NULL,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    trade_no text NOT NULL,
    amount bigint NOT NULL,
    currency text NOT NULL,
    source text NOT NULL,
    at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT order_payments_amount_check CHECK ((amount > 0)),
    CONSTRAINT order_payments_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT order_payments_source_check CHECK ((source = ANY (ARRAY['notify'::text, 'query'::text, 'admin'::text]))),
    CONSTRAINT order_payments_trade_no_check CHECK (((octet_length(trade_no) >= 1) AND (octet_length(trade_no) <= 1024)))
);



ALTER TABLE payments.order_payments OWNER TO axiom_owner;

CREATE TABLE payments.orders (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    method text NOT NULL,
    state text NOT NULL,
    idem_key text NOT NULL,
    fingerprint bytea NOT NULL,
    credits bigint NOT NULL,
    site_currency text NOT NULL,
    pay_currency text NOT NULL,
    total bigint NOT NULL,
    fee bigint NOT NULL,
    fee_ppm integer NOT NULL,
    fee_fixed bigint NOT NULL,
    rate_id uuid,
    ref_num numeric NOT NULL,
    ref_den numeric NOT NULL,
    applied_num numeric NOT NULL,
    applied_den numeric NOT NULL,
    trade_no text DEFAULT ''::text NOT NULL,
    checkout jsonb DEFAULT '{}'::jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    paid_total bigint,
    paid_fee bigint,
    credited bigint,
    paid_at timestamptz,
    arrival_rate_id uuid,
    arrival_ref_num numeric,
    arrival_ref_den numeric,
    arrival_num numeric,
    arrival_den numeric,
    client_ip inet NOT NULL,
    created_at timestamptz DEFAULT transaction_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT transaction_timestamp() NOT NULL,
    next_query_at timestamptz DEFAULT (transaction_timestamp() + '00:00:30'::interval) NOT NULL,
    query_delay_seconds integer DEFAULT 30 NOT NULL,
    next_verify_at timestamptz DEFAULT transaction_timestamp() NOT NULL,
    refund_with_fee boolean,
    CONSTRAINT orders_applied_den_check CHECK (((applied_den > (0)::numeric) AND (scale(applied_den) = 0))),
    CONSTRAINT orders_applied_num_check CHECK (((applied_num > (0)::numeric) AND (scale(applied_num) = 0))),
    CONSTRAINT orders_arrival_den_check CHECK (((arrival_den > (0)::numeric) AND (scale(arrival_den) = 0))),
    CONSTRAINT orders_arrival_num_check CHECK (((arrival_num > (0)::numeric) AND (scale(arrival_num) = 0))),
    CONSTRAINT orders_arrival_ref_den_check CHECK (((arrival_ref_den > (0)::numeric) AND (scale(arrival_ref_den) = 0))),
    CONSTRAINT orders_arrival_ref_num_check CHECK (((arrival_ref_num > (0)::numeric) AND (scale(arrival_ref_num) = 0))),
    CONSTRAINT orders_check CHECK (((fee >= 0) AND (fee <= total))),
    CONSTRAINT orders_check1 CHECK (((rate_id IS NULL) = (pay_currency = site_currency))),
    CONSTRAINT orders_check2 CHECK ((num_nonnulls(arrival_rate_id, arrival_ref_num, arrival_ref_den, arrival_num, arrival_den) = ANY (ARRAY[0, 5]))),
    CONSTRAINT orders_check3 CHECK (((state <> ALL (ARRAY['paid'::text, 'partially_refunded'::text, 'refunded'::text, 'charged_back'::text])) OR ((paid_at IS NOT NULL) AND (paid_total IS NOT NULL) AND (paid_fee IS NOT NULL) AND (credited IS NOT NULL) AND ((paid_fee >= 0) AND (paid_fee <= paid_total))))),
    CONSTRAINT orders_checkout_check CHECK ((octet_length((checkout)::text) <= 8192)),
    CONSTRAINT orders_credited_check CHECK ((credited > 0)),
    CONSTRAINT orders_credits_check CHECK ((credits > 0)),
    CONSTRAINT orders_fee_fixed_check CHECK ((fee_fixed >= 0)),
    CONSTRAINT orders_fee_ppm_check CHECK (((fee_ppm >= 0) AND (fee_ppm <= 500000))),
    CONSTRAINT orders_fingerprint_check CHECK ((octet_length(fingerprint) = 32)),
    CONSTRAINT orders_idem_key_check CHECK (((octet_length(idem_key) >= 1) AND (octet_length(idem_key) <= 128))),
    CONSTRAINT orders_method_check CHECK ((method ~ '^[a-z][a-z0-9_.]{0,31}$'::text)),
    CONSTRAINT orders_paid_fee_check CHECK ((paid_fee >= 0)),
    CONSTRAINT orders_paid_total_check CHECK ((paid_total > 0)),
    CONSTRAINT orders_pay_currency_check CHECK ((pay_currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT orders_query_delay_seconds_check CHECK (((query_delay_seconds >= 30) AND (query_delay_seconds <= 3600))),
    CONSTRAINT orders_ref_den_check CHECK (((ref_den > (0)::numeric) AND (scale(ref_den) = 0))),
    CONSTRAINT orders_ref_num_check CHECK (((ref_num > (0)::numeric) AND (scale(ref_num) = 0))),
    CONSTRAINT orders_site_currency_check CHECK ((site_currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT orders_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'paid'::text, 'expired'::text, 'cancelled'::text, 'failed'::text, 'review'::text, 'partially_refunded'::text, 'refunded'::text, 'charged_back'::text]))),
    CONSTRAINT orders_total_check CHECK ((total > 0))
);



ALTER TABLE payments.orders OWNER TO axiom_owner;

CREATE TABLE payments.settings (
    site_id uuid NOT NULL,
    open boolean,
    credits_min bigint,
    credits_max bigint,
    strategy text,
    pending_limit bigint,
    cancel_limit bigint,
    cancel_window_seconds bigint,
    daily_credits bigint,
    first_credits bigint,
    grace_seconds bigint,
    auto_credit_seconds bigint,
    post_expiry_query_seconds bigint,
    ip_order_limit bigint,
    ip_failure_limit bigint,
    refund_with_fee boolean,
    amount_mismatch_policy text,
    refund_second_approval_credits bigint DEFAULT 0,
    CONSTRAINT settings_amount_mismatch_policy_check CHECK ((amount_mismatch_policy = ANY (ARRAY['manual'::text, 'actual'::text]))),
    CONSTRAINT settings_auto_credit_seconds_check CHECK (((auto_credit_seconds >= 0) AND (auto_credit_seconds <= 2592000))),
    CONSTRAINT settings_cancel_limit_check CHECK ((cancel_limit > 0)),
    CONSTRAINT settings_cancel_window_seconds_check CHECK (((cancel_window_seconds >= 1) AND (cancel_window_seconds <= 86400))),
    CONSTRAINT settings_check CHECK ((credits_min <= credits_max)),
    CONSTRAINT settings_credits_max_check CHECK ((credits_max > 0)),
    CONSTRAINT settings_credits_min_check CHECK ((credits_min > 0)),
    CONSTRAINT settings_daily_credits_check CHECK ((daily_credits > 0)),
    CONSTRAINT settings_first_credits_check CHECK ((first_credits > 0)),
    CONSTRAINT settings_grace_seconds_check CHECK (((grace_seconds >= 0) AND (grace_seconds <= 86400))),
    CONSTRAINT settings_ip_failure_limit_check CHECK ((ip_failure_limit > 0)),
    CONSTRAINT settings_ip_order_limit_check CHECK ((ip_order_limit > 0)),
    CONSTRAINT settings_pending_limit_check CHECK ((pending_limit > 0)),
    CONSTRAINT settings_post_expiry_query_seconds_check CHECK (((post_expiry_query_seconds >= 0) AND (post_expiry_query_seconds <= 2592000))),
    CONSTRAINT settings_refund_second_approval_credits_check CHECK ((refund_second_approval_credits >= 0)),
    CONSTRAINT settings_strategy_check CHECK ((strategy = ANY (ARRAY['round_robin'::text, 'least_used'::text])))
);



ALTER TABLE payments.settings OWNER TO axiom_owner;

CREATE VIEW payments.order_windows WITH (security_invoker='true') AS
 WITH defaults AS (
         SELECT (300)::bigint AS grace,
            (604800)::bigint AS duration
        ), terms AS (
         SELECT o.id,
            o.merchant_id,
            o.state,
            o.trade_no,
            o.expires_at,
            o.next_query_at,
            COALESCE(s.grace_seconds, d.grace) AS grace_seconds,
            COALESCE(s.auto_credit_seconds, d.duration) AS auto_credit_seconds,
            COALESCE(s.post_expiry_query_seconds, d.duration) AS post_expiry_query_seconds
           FROM ((payments.orders o
             LEFT JOIN payments.settings s ON ((s.site_id = o.site_id)))
             CROSS JOIN defaults d)
        )
 SELECT id,
    merchant_id,
    state,
    trade_no,
    expires_at,
    next_query_at,
    grace_seconds,
    auto_credit_seconds,
    post_expiry_query_seconds,
    ((state = 'pending'::text) OR (((state = ANY (ARRAY['expired'::text, 'cancelled'::text])) OR ((state = 'failed'::text) AND (trade_no <> ''::text))) AND ((expires_at + make_interval(secs => (GREATEST(grace_seconds, auto_credit_seconds, post_expiry_query_seconds))::double precision)) >= clock_timestamp()))) AS queryable
   FROM terms;


ALTER VIEW payments.order_windows OWNER TO axiom_owner;

CREATE TABLE payments.query_health (
    merchant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    failures integer DEFAULT 0 NOT NULL,
    paused_until timestamptz DEFAULT '1970-01-01 00:00:00+00'::timestamptz NOT NULL,
    first_token uuid,
    first_until timestamptz DEFAULT '1970-01-01 00:00:00+00'::timestamptz NOT NULL,
    second_token uuid,
    second_until timestamptz DEFAULT '1970-01-01 00:00:00+00'::timestamptz NOT NULL,
    CONSTRAINT query_health_failures_check CHECK (((failures >= 0) AND (failures <= 4)))
);



ALTER TABLE payments.query_health OWNER TO axiom_owner;

CREATE TABLE payments.redeem_attempts (
    site_id uuid NOT NULL,
    kind text NOT NULL,
    subject text NOT NULL,
    window_start timestamptz NOT NULL,
    attempts integer NOT NULL,
    CONSTRAINT redeem_attempts_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT redeem_attempts_kind_check CHECK ((kind = ANY (ARRAY['account'::text, 'ip'::text]))),
    CONSTRAINT redeem_attempts_subject_check CHECK (((octet_length(subject) >= 1) AND (octet_length(subject) <= 64)))
);



ALTER TABLE payments.redeem_attempts OWNER TO axiom_owner;

CREATE TABLE payments.redeem_codes (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    code_hint text NOT NULL,
    amount bigint NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    state text DEFAULT 'enabled'::text NOT NULL,
    expires_at timestamptz,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    created_by uuid NOT NULL,
    redeemed_at timestamptz,
    disabled_at timestamptz,
    disabled_by uuid,
    CONSTRAINT redeem_codes_amount_check CHECK ((amount > 0)),
    CONSTRAINT redeem_codes_check CHECK (((state = 'used'::text) = (redeemed_at IS NOT NULL))),
    CONSTRAINT redeem_codes_check1 CHECK (((state = 'disabled'::text) = (disabled_at IS NOT NULL))),
    CONSTRAINT redeem_codes_code_hash_check CHECK ((octet_length(code_hash) = 32)),
    CONSTRAINT redeem_codes_code_hint_check CHECK ((code_hint ~ '^[0-9A-HJKMNP-TV-Z]{4}$'::text)),
    CONSTRAINT redeem_codes_note_check CHECK ((char_length(note) <= 200)),
    CONSTRAINT redeem_codes_state_check CHECK ((state = ANY (ARRAY['enabled'::text, 'used'::text, 'disabled'::text])))
);



ALTER TABLE payments.redeem_codes OWNER TO axiom_owner;

CREATE TABLE payments.redeem_redemptions (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    code_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    amount bigint NOT NULL,
    code_hint text NOT NULL,
    expires_at timestamptz,
    idem_key text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT redeem_redemptions_amount_check CHECK ((amount > 0)),
    CONSTRAINT redeem_redemptions_code_hint_check CHECK ((code_hint ~ '^[0-9A-HJKMNP-TV-Z]{4}$'::text)),
    CONSTRAINT redeem_redemptions_idem_key_check CHECK (((octet_length(idem_key) >= 1) AND (octet_length(idem_key) <= 128)))
);



ALTER TABLE payments.redeem_redemptions OWNER TO axiom_owner;

CREATE TABLE payments.refunds (
    id uuid NOT NULL,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    state text NOT NULL,
    via text NOT NULL,
    credits bigint NOT NULL,
    requested_credits bigint NOT NULL,
    amount bigint NOT NULL,
    with_fee boolean NOT NULL,
    refund_key boolean NOT NULL,
    manual_only boolean DEFAULT false NOT NULL,
    provider_ref text DEFAULT ''::text NOT NULL,
    reason text NOT NULL,
    requested_by uuid NOT NULL,
    approved_by uuid,
    idem_key text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    sent_at timestamptz,
    next_query_at timestamptz DEFAULT (clock_timestamp() + '00:01:00'::interval) NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    finished_at timestamptz,
    second_required boolean NOT NULL,
    first_approved_by uuid,
    requested_by_agent boolean DEFAULT false NOT NULL,
    CONSTRAINT refunds_amount_check CHECK ((amount > 0)),
    CONSTRAINT refunds_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT refunds_check CHECK ((requested_credits >= credits)),
    CONSTRAINT refunds_check1 CHECK (((approved_by IS NULL) OR (approved_by <> requested_by))),
    CONSTRAINT refunds_check2 CHECK (((state <> ALL (ARRAY['sending'::text, 'pending'::text])) OR (sent_at IS NOT NULL))),
    CONSTRAINT refunds_check3 CHECK (((state = ANY (ARRAY['succeeded'::text, 'failed'::text])) = (finished_at IS NOT NULL))),
    CONSTRAINT refunds_credits_check CHECK ((credits > 0)),
    CONSTRAINT refunds_first_approver CHECK (((first_approved_by IS NULL) OR (first_approved_by <> requested_by))),
    CONSTRAINT refunds_second_approval CHECK ((((NOT second_required) AND (first_approved_by IS NULL)) OR (second_required AND ((approved_by IS NULL) OR ((first_approved_by IS NOT NULL) AND (first_approved_by <> approved_by)))))),
    CONSTRAINT refunds_state_check CHECK ((state = ANY (ARRAY['frozen'::text, 'sending'::text, 'pending'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT refunds_via_check CHECK ((via = ANY (ARRAY['channel'::text, 'offline'::text])))
);



ALTER TABLE payments.refunds OWNER TO axiom_owner;

CREATE TABLE payments.resolutions (
    id uuid NOT NULL,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    refund_id uuid,
    outcome text NOT NULL,
    trade_no text NOT NULL,
    amount bigint NOT NULL,
    evidence text NOT NULL,
    reason text NOT NULL,
    requested_by uuid NOT NULL,
    approved_by uuid,
    idem_key text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT resolutions_check CHECK ((approved_by <> requested_by)),
    CONSTRAINT resolutions_check1 CHECK ((((outcome = 'credit'::text) AND (refund_id IS NULL) AND (amount > 0) AND (trade_no <> ''::text)) OR ((outcome <> 'credit'::text) AND (refund_id IS NOT NULL) AND (amount = 0) AND (trade_no = ''::text)))),
    CONSTRAINT resolutions_evidence_check CHECK (((char_length(evidence) >= 1) AND (char_length(evidence) <= 8192))),
    CONSTRAINT resolutions_outcome_check CHECK ((outcome = ANY (ARRAY['succeeded'::text, 'failed'::text, 'credit'::text]))),
    CONSTRAINT resolutions_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 1000)))
);



ALTER TABLE payments.resolutions OWNER TO axiom_owner;

ALTER TABLE ONLY payments.discrepancies
    ADD CONSTRAINT discrepancies_merchant_id_kind_dedupe_key UNIQUE (merchant_id, kind, dedupe);

ALTER TABLE ONLY payments.discrepancies
    ADD CONSTRAINT discrepancies_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.ip_usage
    ADD CONSTRAINT ip_usage_pkey PRIMARY KEY (site_id, ip);

ALTER TABLE ONLY payments.merchant_configs
    ADD CONSTRAINT merchant_configs_pkey PRIMARY KEY (merchant_id);

ALTER TABLE ONLY payments.merchant_methods
    ADD CONSTRAINT merchant_methods_pkey PRIMARY KEY (merchant_id, method);

ALTER TABLE ONLY payments.merchant_usage
    ADD CONSTRAINT merchant_usage_pkey PRIMARY KEY (merchant_id, day);

ALTER TABLE ONLY payments.merchants
    ADD CONSTRAINT merchants_id_site_id_key UNIQUE (id, site_id);

ALTER TABLE ONLY payments.merchants
    ADD CONSTRAINT merchants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.order_disputes
    ADD CONSTRAINT order_disputes_merchant_id_provider_provider_id_key UNIQUE (merchant_id, provider, provider_id);

ALTER TABLE ONLY payments.order_disputes
    ADD CONSTRAINT order_disputes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.order_events
    ADD CONSTRAINT order_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.order_payments
    ADD CONSTRAINT order_payments_merchant_id_trade_no_key UNIQUE (merchant_id, trade_no);

ALTER TABLE ONLY payments.order_payments
    ADD CONSTRAINT order_payments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.orders
    ADD CONSTRAINT orders_account_id_idem_key_key UNIQUE (account_id, idem_key);

ALTER TABLE ONLY payments.orders
    ADD CONSTRAINT orders_id_site_id_account_id_key UNIQUE (id, site_id, account_id);

ALTER TABLE ONLY payments.orders
    ADD CONSTRAINT orders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.query_health
    ADD CONSTRAINT query_health_pkey PRIMARY KEY (merchant_id);

ALTER TABLE ONLY payments.redeem_attempts
    ADD CONSTRAINT redeem_attempts_pkey PRIMARY KEY (site_id, kind, subject);

ALTER TABLE ONLY payments.redeem_codes
    ADD CONSTRAINT redeem_codes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.redeem_codes
    ADD CONSTRAINT redeem_codes_site_id_code_hash_key UNIQUE (site_id, code_hash);

ALTER TABLE ONLY payments.redeem_redemptions
    ADD CONSTRAINT redeem_redemptions_account_id_idem_key_key UNIQUE (account_id, idem_key);

ALTER TABLE ONLY payments.redeem_redemptions
    ADD CONSTRAINT redeem_redemptions_code_id_key UNIQUE (code_id);

ALTER TABLE ONLY payments.redeem_redemptions
    ADD CONSTRAINT redeem_redemptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.refunds
    ADD CONSTRAINT refunds_id_order_id_key UNIQUE (id, order_id);

ALTER TABLE ONLY payments.refunds
    ADD CONSTRAINT refunds_order_id_idem_key_key UNIQUE (order_id, idem_key);

ALTER TABLE ONLY payments.refunds
    ADD CONSTRAINT refunds_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.resolutions
    ADD CONSTRAINT resolutions_order_id_idem_key_key UNIQUE (order_id, idem_key);

ALTER TABLE ONLY payments.resolutions
    ADD CONSTRAINT resolutions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments.settings
    ADD CONSTRAINT settings_pkey PRIMARY KEY (site_id);

CREATE INDEX discrepancies_by_id ON payments.discrepancies USING btree (state, id DESC);

CREATE INDEX discrepancies_by_site ON payments.discrepancies USING btree (site_id, state, created_at);

CREATE INDEX merchant_methods_by_site ON payments.merchant_methods USING btree (site_id, method);

CREATE INDEX merchants_by_site ON payments.merchants USING btree (site_id, sort);

CREATE INDEX order_disputes_by_merchant_applied ON payments.order_disputes USING btree (merchant_id, applied_at) WHERE (applied_at IS NOT NULL);

CREATE INDEX order_disputes_by_merchant_reversed ON payments.order_disputes USING btree (merchant_id, reversed_at) WHERE (reversed_at IS NOT NULL);

CREATE INDEX order_disputes_by_order ON payments.order_disputes USING btree (order_id, opened_at DESC, id DESC);

CREATE INDEX order_disputes_without_suspension ON payments.order_disputes USING btree (applied_at) WHERE ((state = ANY (ARRAY['applied'::text, 'reversed'::text])) AND (suspended_at IS NULL));

CREATE INDEX order_events_by_order ON payments.order_events USING btree (order_id, at);

CREATE INDEX order_payments_by_order ON payments.order_payments USING btree (order_id);

CREATE INDEX orders_by_account ON payments.orders USING btree (account_id, created_at DESC);

CREATE INDEX orders_by_merchant ON payments.orders USING btree (merchant_id, created_at);

CREATE INDEX orders_by_site_created ON payments.orders USING btree (site_id, created_at DESC, id DESC);

CREATE INDEX orders_by_trade ON payments.orders USING btree (merchant_id, trade_no);

CREATE INDEX orders_changed_by_merchant ON payments.orders USING btree (merchant_id, updated_at, id);

CREATE INDEX orders_expiring ON payments.orders USING btree (expires_at, id) WHERE (state = 'pending'::text);

CREATE INDEX orders_late_window ON payments.orders USING btree (expires_at, next_query_at) WHERE (state = ANY (ARRAY['expired'::text, 'cancelled'::text, 'failed'::text]));

CREATE INDEX orders_merchant_cursor ON payments.orders USING btree (merchant_id, id);

CREATE INDEX orders_query_due ON payments.orders USING btree (next_query_at, id) WHERE (state = ANY (ARRAY['pending'::text, 'expired'::text, 'cancelled'::text, 'failed'::text]));

CREATE INDEX redeem_codes_by_site_created ON payments.redeem_codes USING btree (site_id, created_at DESC, id DESC);

CREATE INDEX redeem_codes_by_site_state ON payments.redeem_codes USING btree (site_id, state, created_at DESC, id DESC);

CREATE INDEX redeem_redemptions_by_account ON payments.redeem_redemptions USING btree (account_id, created_at DESC, id DESC);

CREATE INDEX refunds_by_site_state_created ON payments.refunds USING btree (site_id, state, created_at DESC, id DESC);

CREATE INDEX refunds_cash ON payments.refunds USING btree (finished_at, order_id) WHERE (state = 'succeeded'::text);

CREATE INDEX refunds_due ON payments.refunds USING btree (next_query_at, id) WHERE ((via = 'channel'::text) AND (approved_by IS NOT NULL) AND (NOT manual_only) AND (state = ANY (ARRAY['frozen'::text, 'sending'::text, 'pending'::text])));

CREATE UNIQUE INDEX refunds_one_inflight ON payments.refunds USING btree (order_id) WHERE (state = ANY (ARRAY['frozen'::text, 'sending'::text, 'pending'::text]));

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON payments.merchant_configs FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER refunds_second_required BEFORE INSERT ON payments.refunds FOR EACH ROW EXECUTE FUNCTION payments.refund_second_required();

ALTER TABLE ONLY payments.discrepancies
    ADD CONSTRAINT discrepancies_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.discrepancies
    ADD CONSTRAINT discrepancies_order_id_site_id_account_id_fkey FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id);

ALTER TABLE ONLY payments.merchant_configs
    ADD CONSTRAINT merchant_configs_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.merchant_methods
    ADD CONSTRAINT merchant_methods_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.merchant_usage
    ADD CONSTRAINT merchant_usage_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.order_disputes
    ADD CONSTRAINT order_disputes_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.order_disputes
    ADD CONSTRAINT order_disputes_order_id_site_id_account_id_fkey FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id);

ALTER TABLE ONLY payments.order_events
    ADD CONSTRAINT order_events_order_id_site_id_account_id_fkey FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id);

ALTER TABLE ONLY payments.order_payments
    ADD CONSTRAINT order_payments_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.order_payments
    ADD CONSTRAINT order_payments_order_id_site_id_account_id_fkey FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id);

ALTER TABLE ONLY payments.orders
    ADD CONSTRAINT orders_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.query_health
    ADD CONSTRAINT query_health_merchant_id_site_id_fkey FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id);

ALTER TABLE ONLY payments.redeem_redemptions
    ADD CONSTRAINT redeem_redemptions_code_id_fkey FOREIGN KEY (code_id) REFERENCES payments.redeem_codes(id);

ALTER TABLE ONLY payments.refunds
    ADD CONSTRAINT refunds_order_id_site_id_account_id_fkey FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id);

ALTER TABLE ONLY payments.resolutions
    ADD CONSTRAINT resolutions_order_id_site_id_account_id_fkey FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id);

ALTER TABLE ONLY payments.resolutions
    ADD CONSTRAINT resolutions_refund_id_order_id_fkey FOREIGN KEY (refund_id, order_id) REFERENCES payments.refunds(id, order_id);

CREATE POLICY account ON payments.discrepancies USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY account ON payments.order_disputes USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY account ON payments.order_events USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY account ON payments.order_payments USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY account ON payments.orders USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY account ON payments.redeem_redemptions USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY account ON payments.refunds USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY deletes ON payments.merchant_methods FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));


CREATE POLICY inserts ON payments.ip_usage FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY inserts ON payments.merchant_methods FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON payments.merchant_usage FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY inserts ON payments.merchants FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON payments.redeem_attempts FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY inserts ON payments.settings FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));











CREATE POLICY reads ON payments.ip_usage FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY reads ON payments.merchant_methods FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY reads ON payments.merchant_usage FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY reads ON payments.merchants FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY reads ON payments.redeem_attempts FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY reads ON payments.settings FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));







CREATE POLICY site ON payments.merchant_configs USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY site ON payments.query_health USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY site ON payments.redeem_codes USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY site ON payments.resolutions USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON payments.ip_usage FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site)))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY updates ON payments.merchant_methods FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON payments.merchant_usage FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site)))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY updates ON payments.merchants FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON payments.redeem_attempts FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY updates ON payments.settings FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

GRANT USAGE ON SCHEMA payments TO axiom_app;

GRANT SELECT,INSERT ON TABLE payments.discrepancies TO axiom_app;

GRANT UPDATE(state) ON TABLE payments.discrepancies TO axiom_app;

GRANT UPDATE(investigated_by) ON TABLE payments.discrepancies TO axiom_app;

GRANT UPDATE(investigated_at) ON TABLE payments.discrepancies TO axiom_app;

GRANT UPDATE(note) ON TABLE payments.discrepancies TO axiom_app;

GRANT SELECT ON TABLE payments.ip_usage TO axiom_app;

GRANT INSERT(site_id) ON TABLE payments.ip_usage TO axiom_app;

GRANT INSERT(ip) ON TABLE payments.ip_usage TO axiom_app;

GRANT INSERT(window_start),UPDATE(window_start) ON TABLE payments.ip_usage TO axiom_app;

GRANT INSERT(orders),UPDATE(orders) ON TABLE payments.ip_usage TO axiom_app;

GRANT INSERT(failures),UPDATE(failures) ON TABLE payments.ip_usage TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE payments.merchant_configs TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE payments.merchant_methods TO axiom_app;

GRANT SELECT ON TABLE payments.merchant_usage TO axiom_app;

GRANT INSERT(merchant_id) ON TABLE payments.merchant_usage TO axiom_app;

GRANT INSERT(site_id) ON TABLE payments.merchant_usage TO axiom_app;

GRANT INSERT(day) ON TABLE payments.merchant_usage TO axiom_app;

GRANT INSERT(used),UPDATE(used) ON TABLE payments.merchant_usage TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE payments.merchants TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE payments.order_disputes TO axiom_app;

GRANT SELECT,INSERT ON TABLE payments.order_events TO axiom_app;

GRANT SELECT,INSERT ON TABLE payments.order_payments TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE payments.orders TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE payments.settings TO axiom_app;

GRANT SELECT ON TABLE payments.order_windows TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE payments.query_health TO axiom_app;

GRANT SELECT ON TABLE payments.redeem_attempts TO axiom_app;

GRANT INSERT(site_id) ON TABLE payments.redeem_attempts TO axiom_app;

GRANT INSERT(kind) ON TABLE payments.redeem_attempts TO axiom_app;

GRANT INSERT(subject) ON TABLE payments.redeem_attempts TO axiom_app;

GRANT INSERT(window_start),UPDATE(window_start) ON TABLE payments.redeem_attempts TO axiom_app;

GRANT INSERT(attempts),UPDATE(attempts) ON TABLE payments.redeem_attempts TO axiom_app;

GRANT SELECT,INSERT ON TABLE payments.redeem_codes TO axiom_app;

GRANT UPDATE(state) ON TABLE payments.redeem_codes TO axiom_app;

GRANT UPDATE(redeemed_at) ON TABLE payments.redeem_codes TO axiom_app;

GRANT UPDATE(disabled_at) ON TABLE payments.redeem_codes TO axiom_app;

GRANT UPDATE(disabled_by) ON TABLE payments.redeem_codes TO axiom_app;

GRANT SELECT,INSERT ON TABLE payments.redeem_redemptions TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE payments.refunds TO axiom_app;

GRANT SELECT,INSERT ON TABLE payments.resolutions TO axiom_app;

GRANT UPDATE(approved_by) ON TABLE payments.resolutions TO axiom_app;



ALTER TABLE ONLY payments.discrepancies FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.ip_usage FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.merchant_configs FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.merchant_methods FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.merchant_usage FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.merchants FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.order_disputes FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.order_events FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.order_payments FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.orders FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.settings FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.query_health FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.redeem_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.redeem_codes FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.redeem_redemptions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.refunds FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY payments.resolutions FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.discrepancies ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.ip_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.merchant_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.merchant_methods ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.merchant_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.merchants ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.order_disputes ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.order_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.order_payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.query_health ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.redeem_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.redeem_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.redeem_redemptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.refunds ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.resolutions ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments.settings ENABLE ROW LEVEL SECURITY;
