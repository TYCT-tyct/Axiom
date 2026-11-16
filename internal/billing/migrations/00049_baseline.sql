-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

-- +goose StatementBegin
CREATE FUNCTION billing.balance_notice() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    INSERT INTO billing.balance_notices(site_id,account_id,previous,balance)
    VALUES(NEW.site_id,NEW.account_id,OLD.balance,NEW.balance);
    RETURN NEW;
END $$;


ALTER FUNCTION billing.balance_notice() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION billing.top_up_notice() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    INSERT INTO billing.balance_notices(id,site_id,account_id,previous,balance,top_up)
    VALUES(NEW.id,NEW.site_id,NEW.account_id,0,0,NEW.amount);
    RETURN NEW;
END $$;


ALTER FUNCTION billing.top_up_notice() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION billing.window_from(reset text, at timestamptz) RETURNS timestamptz
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE reset WHEN 'daily'::text THEN (date_trunc('day'::text, (at AT TIME ZONE 'UTC'::text)) AT TIME ZONE 'UTC'::text) WHEN 'weekly'::text THEN (date_trunc('week'::text, (at AT TIME ZONE 'UTC'::text)) AT TIME ZONE 'UTC'::text) WHEN 'monthly'::text THEN (date_trunc('month'::text, (at AT TIME ZONE 'UTC'::text)) AT TIME ZONE 'UTC'::text) ELSE '-infinity'::timestamptz END;


ALTER FUNCTION billing.window_from(reset text, at timestamptz) OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION billing.window_to(reset text, at timestamptz) RETURNS timestamptz
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE reset WHEN 'daily'::text THEN ((date_trunc('day'::text, (at AT TIME ZONE 'UTC'::text)) + '1 day'::interval) AT TIME ZONE 'UTC'::text) WHEN 'weekly'::text THEN ((date_trunc('week'::text, (at AT TIME ZONE 'UTC'::text)) + '7 days'::interval) AT TIME ZONE 'UTC'::text) WHEN 'monthly'::text THEN ((date_trunc('month'::text, (at AT TIME ZONE 'UTC'::text)) + '1 mon'::interval) AT TIME ZONE 'UTC'::text) ELSE 'infinity'::timestamptz END;


ALTER FUNCTION billing.window_to(reset text, at timestamptz) OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE billing.audience_publications (
    site_id uuid NOT NULL,
    model_id uuid NOT NULL,
    audience_kind text NOT NULL,
    audience_id text NOT NULL,
    candidate_id uuid NOT NULL,
    candidate jsonb NOT NULL,
    current_id uuid,
    status text NOT NULL,
    CONSTRAINT audience_publications_audience_id_check CHECK ((audience_id <> ''::text)),
    CONSTRAINT audience_publications_audience_kind_check CHECK ((audience_kind = ANY (ARRAY['account'::text, 'tier'::text]))),
    CONSTRAINT audience_publications_status_check CHECK ((status = ANY (ARRAY['published'::text, 'pending'::text, 'blocked'::text, 'unpriced'::text])))
);



ALTER TABLE billing.audience_publications OWNER TO axiom_owner;

CREATE TABLE billing.balance_notices (
    id uuid DEFAULT uuidv7() NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    previous bigint NOT NULL,
    balance bigint NOT NULL,
    top_up bigint DEFAULT 0 NOT NULL
);



ALTER TABLE billing.balance_notices OWNER TO axiom_owner;

CREATE TABLE billing.balances (
    account_id uuid NOT NULL,
    site_id uuid NOT NULL,
    balance bigint DEFAULT 0 NOT NULL,
    floor bigint DEFAULT 0 NOT NULL,
    reserved bigint DEFAULT 0 NOT NULL,
    allocated_seq bigint DEFAULT 0 NOT NULL,
    allocated_at timestamptz DEFAULT '1970-01-01 00:00:00+00'::timestamptz NOT NULL,
    allocated_id uuid DEFAULT '00000000-0000-0000-0000-000000000000'::uuid NOT NULL,
    unfunded bigint DEFAULT 0 NOT NULL,
    frozen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT balances_floor_check CHECK ((floor <= 0)),
    CONSTRAINT balances_frozen_check CHECK ((frozen >= 0)),
    CONSTRAINT balances_reserved_check CHECK ((reserved >= 0)),
    CONSTRAINT balances_unfunded_check CHECK ((unfunded >= 0))
);



ALTER TABLE billing.balances OWNER TO axiom_owner;

CREATE TABLE billing.charge_correction_cursor (
    singleton boolean DEFAULT true NOT NULL,
    after_at timestamptz DEFAULT '1970-01-01 00:00:00+00'::timestamptz NOT NULL,
    after_id uuid DEFAULT '00000000-0000-0000-0000-000000000000'::uuid NOT NULL,
    CONSTRAINT charge_correction_cursor_singleton_check CHECK (singleton)
);



ALTER TABLE billing.charge_correction_cursor OWNER TO axiom_owner;

CREATE TABLE billing.charge_corrections (
    event_id uuid NOT NULL,
    revision integer NOT NULL,
    request_id uuid NOT NULL,
    next_at timestamptz NOT NULL,
    attempts integer DEFAULT 1 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    CONSTRAINT charge_corrections_attempts_check CHECK ((attempts >= 1)),
    CONSTRAINT charge_corrections_last_error_check CHECK ((char_length(last_error) <= 500)),
    CONSTRAINT charge_corrections_revision_check CHECK ((revision >= 1))
);



ALTER TABLE billing.charge_corrections OWNER TO axiom_owner;

CREATE TABLE billing.credit_batches (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    source text NOT NULL,
    amount bigint NOT NULL,
    expires_at timestamptz,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    consumed bigint DEFAULT 0 NOT NULL,
    expired bigint DEFAULT 0 NOT NULL,
    currency text,
    paid bigint,
    fee bigint,
    merchant_id uuid,
    offline_channel text,
    rate_id uuid,
    freeze_total bigint DEFAULT 0 NOT NULL,
    unfreeze_total bigint DEFAULT 0 NOT NULL,
    refunded bigint DEFAULT 0 NOT NULL,
    refund_with_fee boolean,
    charged_back bigint DEFAULT 0 NOT NULL,
    CONSTRAINT credit_batches_amount_check CHECK ((amount > 0)),
    CONSTRAINT credit_batches_charged_back_check CHECK (((charged_back >= 0) AND ((refunded + charged_back) <= amount))),
    CONSTRAINT credit_batches_consumed_check CHECK ((consumed >= 0)),
    CONSTRAINT credit_batches_expired_check CHECK ((expired >= 0)),
    CONSTRAINT credit_batches_freezes_check CHECK (((freeze_total >= 0) AND (unfreeze_total >= 0) AND (refunded >= 0) AND (unfreeze_total <= freeze_total) AND (refunded <= (freeze_total - unfreeze_total)) AND (consumed <= (amount - expired)) AND ((freeze_total - unfreeze_total) <= ((amount - expired) - consumed)) AND ((source = 'paid'::text) OR ((freeze_total = 0) AND (refund_with_fee IS NULL))))),
    CONSTRAINT credit_batches_source_check CHECK ((((source = ANY (ARRAY['gift'::text, 'redeem'::text])) AND (currency IS NULL) AND (paid IS NULL) AND (fee IS NULL) AND (merchant_id IS NULL) AND (offline_channel IS NULL) AND (rate_id IS NULL)) OR ((source = 'paid'::text) AND (currency IS NOT NULL) AND (paid IS NOT NULL) AND (fee IS NOT NULL) AND (currency ~ '^[A-Z]{3}$'::text) AND (paid > 0) AND (fee >= 0) AND (fee < paid) AND ((merchant_id IS NULL) <> (offline_channel IS NULL)) AND ((char_length(btrim(COALESCE(offline_channel, 'x'::text))) >= 1) AND (char_length(btrim(COALESCE(offline_channel, 'x'::text))) <= 200)))))
);



ALTER TABLE billing.credit_batches OWNER TO axiom_owner;

CREATE TABLE billing.entries (
    transaction_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    book text NOT NULL,
    amount bigint NOT NULL,
    currency text,
    CONSTRAINT entries_amount_check CHECK ((amount <> 0)),
    CONSTRAINT entries_book_check CHECK ((book = ANY (ARRAY['user_balance'::text, 'revenue'::text, 'platform_borne'::text, 'supplier_cost'::text, 'supplier_prepaid'::text, 'gift_expense'::text, 'credit_expired'::text, 'cash'::text, 'fee_income'::text, 'exchange_gain_loss'::text, 'plan_liability'::text, 'plan_discount'::text])))
);



ALTER TABLE billing.entries OWNER TO axiom_owner;

CREATE TABLE billing.exchange_rates (
    id uuid NOT NULL,
    base text NOT NULL,
    quote text NOT NULL,
    version bigint NOT NULL,
    numerator bigint NOT NULL,
    denominator bigint NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    kind text NOT NULL,
    reason text NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    source_at timestamptz,
    CONSTRAINT exchange_rates_base_check CHECK ((base = ANY (ARRAY['USD'::text, 'CNY'::text, 'EUR'::text, 'GBP'::text, 'JPY'::text, 'HKD'::text, 'SGD'::text, 'AUD'::text, 'CAD'::text, 'CHF'::text]))),
    CONSTRAINT exchange_rates_check CHECK ((base < quote)),
    CONSTRAINT exchange_rates_check1 CHECK (((((numerator)::numeric * (1000000)::numeric) >= (denominator)::numeric) AND ((numerator)::numeric <= ((denominator)::numeric * (1000000)::numeric)))),
    CONSTRAINT exchange_rates_created_by_check CHECK ((created_by <> ''::text)),
    CONSTRAINT exchange_rates_denominator_check CHECK (((denominator >= 1) AND (denominator <= '1000000000000000000'::bigint))),
    CONSTRAINT exchange_rates_kind_check CHECK ((kind = ANY (ARRAY['initial'::text, 'adjustment'::text, 'correction'::text]))),
    CONSTRAINT exchange_rates_numerator_check CHECK (((numerator >= 1) AND (numerator <= '1000000000000000000'::bigint))),
    CONSTRAINT exchange_rates_quote_check CHECK ((quote = ANY (ARRAY['USD'::text, 'CNY'::text, 'EUR'::text, 'GBP'::text, 'JPY'::text, 'HKD'::text, 'SGD'::text, 'AUD'::text, 'CAD'::text, 'CHF'::text]))),
    CONSTRAINT exchange_rates_reason_check CHECK (((char_length(btrim(reason)) >= 1) AND (char_length(btrim(reason)) <= 1000))),
    CONSTRAINT exchange_rates_source_check CHECK ((((source = 'manual'::text) AND (source_at IS NULL)) OR ((source = 'ecb'::text) AND (source_at IS NOT NULL)))),
    CONSTRAINT exchange_rates_version_check CHECK ((version > 0))
);



ALTER TABLE billing.exchange_rates OWNER TO axiom_owner;

CREATE TABLE billing.exchange_sync (
    singleton boolean DEFAULT true NOT NULL,
    started_at timestamptz,
    last_attempt timestamptz,
    last_success timestamptz,
    source_at timestamptz,
    alerted boolean DEFAULT false NOT NULL,
    CONSTRAINT exchange_sync_singleton_check CHECK (singleton)
);



ALTER TABLE billing.exchange_sync OWNER TO axiom_owner;

CREATE TABLE billing.expiry_cursor (
    one boolean DEFAULT true NOT NULL,
    after uuid NOT NULL,
    CONSTRAINT expiry_cursor_one_check CHECK (one)
);



ALTER TABLE billing.expiry_cursor OWNER TO axiom_owner;

CREATE TABLE billing.fallbacks (
    currency text NOT NULL,
    body text NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT fallbacks_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text))
);



ALTER TABLE billing.fallbacks OWNER TO axiom_owner;

CREATE TABLE billing.holders (
    id uuid NOT NULL,
    started_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    expires_at timestamptz NOT NULL,
    reaped_at timestamptz
);



ALTER TABLE billing.holders OWNER TO axiom_owner;

CREATE TABLE billing.price_policies (
    site_id uuid NOT NULL,
    rev bigint DEFAULT 0 NOT NULL,
    confirmed_at timestamptz,
    floor_multiplier text NOT NULL,
    change_ratio text NOT NULL,
    max_increase_ratio text NOT NULL,
    byok_fee_bps integer DEFAULT 500 NOT NULL,
    CONSTRAINT price_policies_byok_fee_bps_check CHECK (((byok_fee_bps >= 0) AND (byok_fee_bps <= 10000))),
    CONSTRAINT price_policies_rev_check CHECK ((rev >= 0))
);



ALTER TABLE billing.price_policies OWNER TO axiom_owner;

CREATE TABLE billing.price_publications (
    site_id uuid NOT NULL,
    model_id uuid NOT NULL,
    candidate_id uuid NOT NULL,
    candidate jsonb NOT NULL,
    current_id uuid,
    status text NOT NULL,
    CONSTRAINT price_publications_status_check CHECK ((status = ANY (ARRAY['published'::text, 'pending'::text, 'blocked'::text, 'unpriced'::text])))
);



ALTER TABLE billing.price_publications OWNER TO axiom_owner;

CREATE TABLE billing.price_versions (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    model_id uuid NOT NULL,
    candidate_id uuid NOT NULL,
    schedule jsonb NOT NULL,
    basis jsonb NOT NULL,
    overridden boolean NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    at timestamptz DEFAULT clock_timestamp() NOT NULL,
    audience_kind text DEFAULT 'all'::text NOT NULL,
    audience_id text DEFAULT ''::text NOT NULL,
    endpoints jsonb,
    CONSTRAINT price_versions_audience_kind_check CHECK ((audience_kind = ANY (ARRAY['all'::text, 'account'::text, 'tier'::text]))),
    CONSTRAINT price_versions_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 1000)))
);



ALTER TABLE billing.price_versions OWNER TO axiom_owner;

CREATE TABLE billing.pricing_rule_versions (
    rule_id uuid NOT NULL,
    rev bigint NOT NULL,
    body jsonb NOT NULL,
    at timestamptz DEFAULT now() NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    CONSTRAINT pricing_rule_versions_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT pricing_rule_versions_body_check CHECK ((jsonb_typeof(body) = 'object'::text)),
    CONSTRAINT pricing_rule_versions_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 1000))),
    CONSTRAINT pricing_rule_versions_rev_check CHECK ((rev >= 0))
);



ALTER TABLE billing.pricing_rule_versions OWNER TO axiom_owner;

CREATE TABLE billing.pricing_rules (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    rev bigint DEFAULT 0 NOT NULL,
    enabled boolean NOT NULL,
    target_kind text NOT NULL,
    target_id text NOT NULL,
    audience_kind text NOT NULL,
    audience_id text NOT NULL,
    method jsonb NOT NULL,
    constraints jsonb NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT pricing_rules_audience_kind_check CHECK ((audience_kind = ANY (ARRAY['account'::text, 'tier'::text, 'all'::text]))),
    CONSTRAINT pricing_rules_check CHECK ((((target_kind = 'all'::text) = (target_id = ''::text)) AND (char_length(target_id) <= 200))),
    CONSTRAINT pricing_rules_check1 CHECK ((((audience_kind = 'all'::text) = (audience_id = ''::text)) AND (char_length(audience_id) <= 64))),
    CONSTRAINT pricing_rules_constraints_check CHECK ((jsonb_typeof(constraints) = 'object'::text)),
    CONSTRAINT pricing_rules_method_check CHECK ((jsonb_typeof(method) = 'object'::text)),
    CONSTRAINT pricing_rules_rev_check CHECK ((rev >= 0)),
    CONSTRAINT pricing_rules_target_kind_check CHECK ((target_kind = ANY (ARRAY['endpoint'::text, 'group'::text, 'supplier'::text, 'model'::text, 'author'::text, 'source_type'::text, 'all'::text])))
);



ALTER TABLE billing.pricing_rules OWNER TO axiom_owner;

CREATE TABLE billing.reservations (
    request_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    holder_id uuid NOT NULL,
    workspace_window timestamptz NOT NULL,
    key_window timestamptz NOT NULL,
    segment integer NOT NULL,
    amount bigint NOT NULL,
    state text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    deadline timestamptz NOT NULL,
    prices text[] DEFAULT '{}'::text[] NOT NULL,
    charged_seq integer,
    entry integer,
    charged bigint DEFAULT 0 NOT NULL,
    price bigint DEFAULT 0 NOT NULL,
    waived boolean DEFAULT false NOT NULL,
    estimated boolean DEFAULT false NOT NULL,
    final_usage jsonb DEFAULT '[]'::jsonb NOT NULL,
    fallbacks text[] DEFAULT '{}'::text[] NOT NULL,
    models uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    attempt_entries integer[] DEFAULT '{}'::integer[] NOT NULL,
    endpoints uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    exchange_rates jsonb,
    profit bigint,
    price_versions uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    pricing_tiers smallint,
    funding text DEFAULT 'wallet'::text NOT NULL,
    subscription_id uuid,
    window_seq integer,
    CONSTRAINT reservations_amount_check CHECK ((amount >= 0)),
    CONSTRAINT reservations_charged_check CHECK ((charged >= 0)),
    CONSTRAINT reservations_charged_seq_check CHECK ((charged_seq >= 1)),
    CONSTRAINT reservations_check CHECK (((segment > 0) OR ((state = 'released'::text) AND (amount = 0)))),
    CONSTRAINT reservations_entry_check CHECK ((entry >= 0)),
    CONSTRAINT reservations_funding_check CHECK ((((funding = 'wallet'::text) AND (subscription_id IS NULL) AND (window_seq IS NULL)) OR ((funding = 'plan'::text) AND (subscription_id IS NOT NULL) AND (window_seq IS NOT NULL)))),
    CONSTRAINT reservations_price_check CHECK ((price >= 0)),
    CONSTRAINT reservations_segment_check CHECK ((segment >= 0)),
    CONSTRAINT reservations_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'released'::text, 'settled'::text, 'cleanup_settled'::text, 'cleanup_released'::text])))
);



ALTER TABLE billing.reservations OWNER TO axiom_owner;

CREATE TABLE billing.segment_settings (
    singleton boolean DEFAULT true NOT NULL,
    output_tokens bigint,
    overshoot_seconds integer,
    CONSTRAINT segment_settings_output_tokens_check CHECK (((output_tokens >= 1) AND (output_tokens <= 1000000))),
    CONSTRAINT segment_settings_overshoot_seconds_check CHECK (((overshoot_seconds >= 1) AND (overshoot_seconds <= 600))),
    CONSTRAINT segment_settings_singleton_check CHECK (singleton)
);



ALTER TABLE billing.segment_settings OWNER TO axiom_owner;

CREATE TABLE billing.spend (
    subject uuid NOT NULL,
    window_start timestamptz NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    spent bigint DEFAULT 0 NOT NULL,
    reserved bigint DEFAULT 0 NOT NULL,
    CONSTRAINT spend_reserved_check CHECK ((reserved >= 0)),
    CONSTRAINT spend_spent_check CHECK ((spent >= 0))
);



ALTER TABLE billing.spend OWNER TO axiom_owner;

CREATE TABLE billing.subscription_grants (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_name text NOT NULL,
    source text NOT NULL,
    quota bigint NOT NULL,
    window_seconds bigint NOT NULL,
    term_seconds bigint NOT NULL,
    windows integer NOT NULL,
    face bigint NOT NULL,
    currency text,
    paid bigint,
    fee bigint,
    merchant_id uuid,
    offline_channel text,
    rate_id uuid,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    first_window integer,
    refunded_value bigint DEFAULT 0 NOT NULL,
    refunded_paid bigint DEFAULT 0 NOT NULL,
    refunded_fee bigint DEFAULT 0 NOT NULL,
    refunded_windows integer DEFAULT 0 NOT NULL,
    CONSTRAINT subscription_grants_check CHECK (((term_seconds >= window_seconds) AND (term_seconds <= '9223372036'::bigint))),
    CONSTRAINT subscription_grants_check1 CHECK ((((source = 'gift'::text) AND (currency IS NULL) AND (paid IS NULL) AND (fee IS NULL) AND (merchant_id IS NULL) AND (offline_channel IS NULL) AND (rate_id IS NULL)) OR ((source = 'paid'::text) AND (currency ~ '^[A-Z]{3}$'::text) AND (paid > 0) AND (fee >= 0) AND (fee < paid) AND ((merchant_id IS NOT NULL) <> (offline_channel IS NOT NULL))))),
    CONSTRAINT subscription_grants_check2 CHECK ((face = (quota * windows))),
    CONSTRAINT subscription_grants_face_check CHECK ((face > 0)),
    CONSTRAINT subscription_grants_first_window_check CHECK (((first_window IS NULL) OR (first_window >= 1))),
    CONSTRAINT subscription_grants_plan_name_check CHECK ((plan_name <> ''::text)),
    CONSTRAINT subscription_grants_quota_check CHECK ((quota > 0)),
    CONSTRAINT subscription_grants_refunded_check CHECK (((refunded_paid <= COALESCE(paid, (0)::bigint)) AND (refunded_fee <= COALESCE(fee, (0)::bigint)))),
    CONSTRAINT subscription_grants_refunded_fee_check CHECK ((refunded_fee >= 0)),
    CONSTRAINT subscription_grants_refunded_paid_check CHECK ((refunded_paid >= 0)),
    CONSTRAINT subscription_grants_refunded_value_check CHECK ((refunded_value >= 0)),
    CONSTRAINT subscription_grants_refunded_windows_check CHECK ((refunded_windows >= 0)),
    CONSTRAINT subscription_grants_refunded_windows_limit CHECK ((refunded_windows <= windows)),
    CONSTRAINT subscription_grants_source_check CHECK ((source = ANY (ARRAY['gift'::text, 'paid'::text]))),
    CONSTRAINT subscription_grants_window_seconds_check CHECK (((window_seconds >= 3600) AND (window_seconds <= '9223372036'::bigint))),
    CONSTRAINT subscription_grants_windows_check CHECK (((windows >= 1) AND (windows <= 400)))
);



ALTER TABLE billing.subscription_grants OWNER TO axiom_owner;

CREATE TABLE billing.subscription_plans (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    name text NOT NULL,
    currency text NOT NULL,
    price bigint NOT NULL,
    quota bigint NOT NULL,
    window_seconds bigint NOT NULL,
    term_seconds bigint NOT NULL,
    active boolean DEFAULT true NOT NULL,
    rev integer DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT subscription_plans_check CHECK (((term_seconds >= window_seconds) AND (term_seconds <= '9223372036'::bigint))),
    CONSTRAINT subscription_plans_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT subscription_plans_name_check CHECK (((name <> ''::text) AND (length(name) <= 200))),
    CONSTRAINT subscription_plans_price_check CHECK ((price >= 0)),
    CONSTRAINT subscription_plans_quota_check CHECK ((quota > 0)),
    CONSTRAINT subscription_plans_rev_check CHECK ((rev > 0)),
    CONSTRAINT subscription_plans_window_seconds_check CHECK (((window_seconds >= 3600) AND (window_seconds <= '9223372036'::bigint)))
);



ALTER TABLE billing.subscription_plans OWNER TO axiom_owner;

CREATE TABLE billing.subscription_refund_requests (
    id uuid NOT NULL,
    grant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    credits bigint NOT NULL,
    site_currency text NOT NULL,
    requested_by uuid NOT NULL,
    approved_by uuid,
    state text DEFAULT 'pending'::text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    finished_at timestamptz,
    CONSTRAINT subscription_refund_requests_check CHECK (((approved_by IS NULL) OR (approved_by <> requested_by))),
    CONSTRAINT subscription_refund_requests_check1 CHECK (((state = 'pending'::text) OR (approved_by IS NOT NULL))),
    CONSTRAINT subscription_refund_requests_credits_check CHECK ((credits > 0)),
    CONSTRAINT subscription_refund_requests_reason_check CHECK ((reason <> ''::text)),
    CONSTRAINT subscription_refund_requests_site_currency_check CHECK ((site_currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT subscription_refund_requests_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text])))
);



ALTER TABLE billing.subscription_refund_requests OWNER TO axiom_owner;

CREATE TABLE billing.subscription_refunds (
    id uuid NOT NULL,
    grant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    credits bigint NOT NULL,
    paid bigint NOT NULL,
    fee bigint NOT NULL,
    currency text NOT NULL,
    windows integer NOT NULL,
    unused bigint NOT NULL,
    revoked boolean NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT subscription_refunds_credits_check CHECK ((credits > 0)),
    CONSTRAINT subscription_refunds_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT subscription_refunds_fee_check CHECK ((fee >= 0)),
    CONSTRAINT subscription_refunds_paid_check CHECK ((paid > 0)),
    CONSTRAINT subscription_refunds_reason_check CHECK ((reason <> ''::text)),
    CONSTRAINT subscription_refunds_unused_check CHECK ((unused >= 0)),
    CONSTRAINT subscription_refunds_windows_check CHECK ((windows >= 0))
);



ALTER TABLE billing.subscription_refunds OWNER TO axiom_owner;

CREATE TABLE billing.subscription_windows (
    subscription_id uuid NOT NULL,
    seq integer NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    quota bigint NOT NULL,
    used bigint DEFAULT 0 NOT NULL,
    reserved bigint DEFAULT 0 NOT NULL,
    voided_at timestamptz,
    CONSTRAINT subscription_windows_check CHECK ((ends_at > starts_at)),
    CONSTRAINT subscription_windows_check1 CHECK (((used + reserved) <= quota)),
    CONSTRAINT subscription_windows_quota_check CHECK ((quota > 0)),
    CONSTRAINT subscription_windows_reserved_check CHECK ((reserved >= 0)),
    CONSTRAINT subscription_windows_seq_check CHECK ((seq >= 1)),
    CONSTRAINT subscription_windows_used_check CHECK ((used >= 0))
);



ALTER TABLE billing.subscription_windows OWNER TO axiom_owner;

CREATE TABLE billing.subscriptions (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_name text NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    current_seq integer DEFAULT 1 NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    closed_at timestamptz,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT subscriptions_check CHECK ((ends_at > starts_at)),
    CONSTRAINT subscriptions_check1 CHECK (((closed_at IS NULL) OR (state = 'expired'::text))),
    CONSTRAINT subscriptions_current_seq_check CHECK ((current_seq >= 1)),
    CONSTRAINT subscriptions_plan_name_check CHECK ((plan_name <> ''::text)),
    CONSTRAINT subscriptions_state_check CHECK ((state = ANY (ARRAY['active'::text, 'expired'::text])))
);



ALTER TABLE billing.subscriptions OWNER TO axiom_owner;

CREATE TABLE billing.top_up_buffers (
    site_id uuid NOT NULL,
    ppm integer NOT NULL,
    CONSTRAINT top_up_buffers_ppm_check CHECK (((ppm >= 0) AND (ppm <= 100000)))
);



ALTER TABLE billing.top_up_buffers OWNER TO axiom_owner;

CREATE TABLE billing.transactions (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    kind text NOT NULL,
    request_id uuid NOT NULL,
    attempt_seq integer DEFAULT 0 NOT NULL,
    revision bigint DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    seq bigint,
    CONSTRAINT transactions_kind_check CHECK ((kind = ANY (ARRAY['credit'::text, 'settle'::text, 'reverse'::text, 'attempt_cost'::text, 'cost_adjust'::text, 'expire'::text, 'freeze'::text, 'unfreeze'::text, 'refund'::text, 'chargeback'::text, 'chargeback_reverse'::text, 'plan_credit'::text, 'plan_expire'::text, 'plan_refund'::text])))
);



ALTER TABLE billing.transactions OWNER TO axiom_owner;

CREATE SEQUENCE billing.transactions_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE billing.transactions_seq OWNER TO axiom_owner;

ALTER SEQUENCE billing.transactions_seq OWNED BY billing.transactions.seq;

CREATE TABLE billing.unknown_items (
    model_id uuid NOT NULL,
    field text NOT NULL,
    request_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    seen_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    cleared_at timestamptz
);



ALTER TABLE billing.unknown_items OWNER TO axiom_owner;

ALTER TABLE ONLY billing.transactions ALTER COLUMN seq SET DEFAULT nextval('billing.transactions_seq'::regclass);

INSERT INTO billing.charge_correction_cursor (singleton, after_at, after_id) VALUES (true, '1970-01-01 00:00:00+00', '00000000-0000-0000-0000-000000000000');
INSERT INTO billing.exchange_sync (singleton, started_at, last_attempt, last_success, source_at, alerted) VALUES (true, NULL, NULL, NULL, NULL, false);
INSERT INTO billing.fallbacks (currency, body, updated_at) VALUES ('USD', '{"currency":"USD","per_use":{"code_execution":"0.05","web_search":"0.025"}}', now());

ALTER TABLE ONLY billing.audience_publications
    ADD CONSTRAINT audience_publications_pkey PRIMARY KEY (site_id, model_id, audience_kind, audience_id);

ALTER TABLE ONLY billing.balance_notices
    ADD CONSTRAINT balance_notices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.balances
    ADD CONSTRAINT balances_pkey PRIMARY KEY (account_id);

ALTER TABLE ONLY billing.charge_correction_cursor
    ADD CONSTRAINT charge_correction_cursor_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY billing.charge_corrections
    ADD CONSTRAINT charge_corrections_pkey PRIMARY KEY (event_id);

ALTER TABLE ONLY billing.credit_batches
    ADD CONSTRAINT credit_batches_pkey PRIMARY KEY (id);

ALTER TABLE billing.entries
    ADD CONSTRAINT entries_currency_check CHECK (((currency IS NULL) OR ((currency ~ '^[A-Z]{3}$'::text) AND (book <> 'user_balance'::text)))) NOT VALID;

ALTER TABLE ONLY billing.exchange_rates
    ADD CONSTRAINT exchange_rates_base_quote_version_key UNIQUE (base, quote, version);

ALTER TABLE ONLY billing.exchange_rates
    ADD CONSTRAINT exchange_rates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.exchange_sync
    ADD CONSTRAINT exchange_sync_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY billing.expiry_cursor
    ADD CONSTRAINT expiry_cursor_pkey PRIMARY KEY (one);

ALTER TABLE ONLY billing.fallbacks
    ADD CONSTRAINT fallbacks_pkey PRIMARY KEY (currency);

ALTER TABLE ONLY billing.holders
    ADD CONSTRAINT holders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.price_policies
    ADD CONSTRAINT price_policies_pkey PRIMARY KEY (site_id);

ALTER TABLE ONLY billing.price_publications
    ADD CONSTRAINT price_publications_pkey PRIMARY KEY (site_id, model_id);

ALTER TABLE ONLY billing.price_versions
    ADD CONSTRAINT price_versions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.pricing_rule_versions
    ADD CONSTRAINT pricing_rule_versions_pkey PRIMARY KEY (rule_id, rev);

ALTER TABLE ONLY billing.pricing_rules
    ADD CONSTRAINT pricing_rules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.pricing_rules
    ADD CONSTRAINT pricing_rules_site_id_target_kind_target_id_audience_kind_a_key UNIQUE (site_id, target_kind, target_id, audience_kind, audience_id);

ALTER TABLE ONLY billing.reservations
    ADD CONSTRAINT reservations_pkey PRIMARY KEY (request_id);

ALTER TABLE billing.reservations
    ADD CONSTRAINT reservations_pricing_tiers_check CHECK (((pricing_tiers >= '-1'::integer) AND (pricing_tiers <= 15))) NOT VALID;

ALTER TABLE ONLY billing.segment_settings
    ADD CONSTRAINT segment_settings_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY billing.spend
    ADD CONSTRAINT spend_pkey PRIMARY KEY (subject, window_start);

ALTER TABLE ONLY billing.subscription_grants
    ADD CONSTRAINT subscription_grants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.subscription_plans
    ADD CONSTRAINT subscription_plans_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.subscription_plans
    ADD CONSTRAINT subscription_plans_site_id_name_key UNIQUE (site_id, name);

ALTER TABLE ONLY billing.subscription_refund_requests
    ADD CONSTRAINT subscription_refund_requests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.subscription_refunds
    ADD CONSTRAINT subscription_refunds_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.subscription_windows
    ADD CONSTRAINT subscription_windows_pkey PRIMARY KEY (subscription_id, seq);

ALTER TABLE ONLY billing.subscriptions
    ADD CONSTRAINT subscriptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.top_up_buffers
    ADD CONSTRAINT top_up_buffers_pkey PRIMARY KEY (site_id);

ALTER TABLE ONLY billing.transactions
    ADD CONSTRAINT transactions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY billing.transactions
    ADD CONSTRAINT transactions_request_id_attempt_seq_kind_revision_key UNIQUE (request_id, attempt_seq, kind, revision);

ALTER TABLE ONLY billing.unknown_items
    ADD CONSTRAINT unknown_items_pkey PRIMARY KEY (model_id, field);

CREATE INDEX charge_corrections_due ON billing.charge_corrections USING btree (next_at);

CREATE INDEX credit_batches_by_account ON billing.credit_batches USING btree (account_id);

CREATE INDEX credit_batches_by_merchant_day ON billing.credit_batches USING btree (merchant_id, created_at, id) WHERE (merchant_id IS NOT NULL);

CREATE INDEX credit_batches_by_site_paid_created ON billing.credit_batches USING btree (site_id, created_at) INCLUDE (account_id, amount) WHERE (source = 'paid'::text);

CREATE INDEX credit_batches_due ON billing.credit_batches USING btree (expires_at, account_id) WHERE ((expires_at IS NOT NULL) AND ((consumed + expired) < amount));

CREATE INDEX entries_by_transaction ON billing.entries USING btree (transaction_id);

CREATE INDEX entries_user_balance ON billing.entries USING btree (account_id) INCLUDE (amount) WHERE (book = 'user_balance'::text);

CREATE UNIQUE INDEX one_active_subscription ON billing.subscriptions USING btree (account_id) WHERE (state = 'active'::text);

CREATE INDEX pending_by_deadline ON billing.reservations USING btree (deadline) WHERE (state = 'pending'::text);

CREATE INDEX pending_by_holder ON billing.reservations USING btree (holder_id) WHERE (state = 'pending'::text);

CREATE INDEX reservations_by_site_created ON billing.reservations USING btree (site_id, created_at);

CREATE INDEX transactions_by_account_seq ON billing.transactions USING btree (account_id, seq);

CREATE INDEX transactions_by_site_kind_created ON billing.transactions USING btree (site_id, kind, created_at, id);

CREATE INDEX transactions_legacy_by_account ON billing.transactions USING btree (account_id, created_at, id) WHERE (seq IS NULL);

CREATE INDEX transactions_refunds_by_day ON billing.transactions USING btree (created_at, request_id) WHERE (kind = 'refund'::text);

CREATE TRIGGER balance_notice AFTER UPDATE OF balance ON billing.balances FOR EACH ROW WHEN ((new.balance <> old.balance)) EXECUTE FUNCTION billing.balance_notice();

CREATE TRIGGER top_up_notice AFTER INSERT ON billing.credit_batches FOR EACH ROW WHEN ((new.source = 'paid'::text)) EXECUTE FUNCTION billing.top_up_notice();

ALTER TABLE ONLY billing.audience_publications
    ADD CONSTRAINT audience_publications_current_id_fkey FOREIGN KEY (current_id) REFERENCES billing.price_versions(id);

ALTER TABLE ONLY billing.entries
    ADD CONSTRAINT entries_transaction_id_fkey FOREIGN KEY (transaction_id) REFERENCES billing.transactions(id);

ALTER TABLE ONLY billing.price_publications
    ADD CONSTRAINT price_publications_current_id_fkey FOREIGN KEY (current_id) REFERENCES billing.price_versions(id);

ALTER TABLE ONLY billing.pricing_rule_versions
    ADD CONSTRAINT pricing_rule_versions_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES billing.pricing_rules(id);

ALTER TABLE ONLY billing.reservations
    ADD CONSTRAINT reservations_holder_id_fkey FOREIGN KEY (holder_id) REFERENCES billing.holders(id);






CREATE POLICY claims ON billing.balance_notices FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY corrections ON billing.reservations FOR UPDATE USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());


CREATE POLICY drains ON billing.balance_notices FOR DELETE USING (( SELECT platform.in_platform_scope() AS in_platform_scope));







CREATE POLICY inserts ON billing.audience_publications FOR INSERT WITH CHECK (platform.in_platform_scope());

CREATE POLICY inserts ON billing.balance_notices FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.balances FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.credit_batches FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.entries FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.exchange_rates FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON billing.expiry_cursor FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON billing.fallbacks FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON billing.holders FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON billing.price_policies FOR INSERT WITH CHECK (platform.in_platform_scope());

CREATE POLICY inserts ON billing.price_publications FOR INSERT WITH CHECK (platform.in_platform_scope());

CREATE POLICY inserts ON billing.price_versions FOR INSERT WITH CHECK (platform.in_platform_scope());

CREATE POLICY inserts ON billing.pricing_rule_versions FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON billing.pricing_rules FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON billing.reservations FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.spend FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.subscription_grants FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.subscription_plans FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON billing.subscription_refund_requests FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.subscription_refunds FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.subscription_windows FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.subscriptions FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.transactions FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON billing.unknown_items FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY platform ON billing.exchange_sync USING (platform.in_platform_scope());

CREATE POLICY platform ON billing.segment_settings USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON billing.charge_correction_cursor USING (platform.in_platform_scope());

CREATE POLICY platform_only ON billing.charge_corrections USING (platform.in_platform_scope());






CREATE POLICY reads ON billing.audience_publications FOR SELECT USING (platform.in_platform_scope());

CREATE POLICY reads ON billing.balance_notices FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.balances FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.credit_batches FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.entries FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.exchange_rates FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON billing.expiry_cursor FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON billing.fallbacks FOR SELECT USING (( SELECT platform.in_any_scope() AS in_any_scope));

CREATE POLICY reads ON billing.holders FOR SELECT USING (( SELECT platform.in_any_scope() AS in_any_scope));

CREATE POLICY reads ON billing.price_policies FOR SELECT USING (platform.in_platform_scope());

CREATE POLICY reads ON billing.price_publications FOR SELECT USING (platform.in_platform_scope());

CREATE POLICY reads ON billing.price_versions FOR SELECT USING (platform.in_platform_scope());

CREATE POLICY reads ON billing.pricing_rule_versions FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON billing.pricing_rules FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON billing.reservations FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.spend FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.subscription_grants FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.subscription_plans FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON billing.subscription_refund_requests FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.subscription_refunds FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.subscription_windows FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.subscriptions FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.transactions FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON billing.unknown_items FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));



CREATE POLICY site ON billing.top_up_buffers USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));











CREATE POLICY updates ON billing.audience_publications FOR UPDATE USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());

CREATE POLICY updates ON billing.balances FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.credit_batches FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.expiry_cursor FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON billing.fallbacks FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON billing.holders FOR UPDATE USING (( SELECT platform.in_any_scope() AS in_any_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON billing.price_policies FOR UPDATE USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());

CREATE POLICY updates ON billing.price_publications FOR UPDATE USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());

CREATE POLICY updates ON billing.pricing_rules FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON billing.reservations FOR UPDATE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))) AND (state <> ALL (ARRAY['released'::text, 'settled'::text])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.spend FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.subscription_grants FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.subscription_plans FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON billing.subscription_refund_requests FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.subscription_refunds FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.subscription_windows FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.subscriptions FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON billing.unknown_items FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

GRANT USAGE ON SCHEMA billing TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE billing.audience_publications TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE billing.balance_notices TO axiom_app;

GRANT UPDATE(id) ON TABLE billing.balance_notices TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.balances TO axiom_app;

GRANT UPDATE(balance) ON TABLE billing.balances TO axiom_app;

GRANT UPDATE(reserved) ON TABLE billing.balances TO axiom_app;

GRANT UPDATE(allocated_seq) ON TABLE billing.balances TO axiom_app;

GRANT UPDATE(allocated_at) ON TABLE billing.balances TO axiom_app;

GRANT UPDATE(allocated_id) ON TABLE billing.balances TO axiom_app;

GRANT UPDATE(unfunded) ON TABLE billing.balances TO axiom_app;

GRANT UPDATE(frozen) ON TABLE billing.balances TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE billing.charge_correction_cursor TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE billing.charge_corrections TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.credit_batches TO axiom_app;

GRANT UPDATE(consumed) ON TABLE billing.credit_batches TO axiom_app;

GRANT UPDATE(expired) ON TABLE billing.credit_batches TO axiom_app;

GRANT UPDATE(freeze_total) ON TABLE billing.credit_batches TO axiom_app;

GRANT UPDATE(unfreeze_total) ON TABLE billing.credit_batches TO axiom_app;

GRANT UPDATE(refunded) ON TABLE billing.credit_batches TO axiom_app;

GRANT UPDATE(refund_with_fee) ON TABLE billing.credit_batches TO axiom_app;

GRANT UPDATE(charged_back) ON TABLE billing.credit_batches TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.entries TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.exchange_rates TO axiom_app;

GRANT SELECT,UPDATE ON TABLE billing.exchange_sync TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.expiry_cursor TO axiom_app;

GRANT UPDATE(after) ON TABLE billing.expiry_cursor TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.fallbacks TO axiom_app;

GRANT UPDATE(body) ON TABLE billing.fallbacks TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE billing.fallbacks TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.holders TO axiom_app;

GRANT UPDATE(expires_at) ON TABLE billing.holders TO axiom_app;

GRANT UPDATE(reaped_at) ON TABLE billing.holders TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE billing.price_policies TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE billing.price_publications TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.price_versions TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.pricing_rule_versions TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(rev) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(enabled) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(target_kind) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(target_id) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(audience_kind) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(audience_id) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(method) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(constraints) ON TABLE billing.pricing_rules TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE billing.pricing_rules TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(segment) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(amount) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(state) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(charged_seq) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(entry) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(charged) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(price) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(waived) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(estimated) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(final_usage) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(attempt_entries) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(profit) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(pricing_tiers) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(funding) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(subscription_id) ON TABLE billing.reservations TO axiom_app;

GRANT UPDATE(window_seq) ON TABLE billing.reservations TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE billing.segment_settings TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.spend TO axiom_app;

GRANT UPDATE(spent) ON TABLE billing.spend TO axiom_app;

GRANT UPDATE(reserved) ON TABLE billing.spend TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.subscription_grants TO axiom_app;

GRANT UPDATE(refunded_value) ON TABLE billing.subscription_grants TO axiom_app;

GRANT UPDATE(refunded_paid) ON TABLE billing.subscription_grants TO axiom_app;

GRANT UPDATE(refunded_fee) ON TABLE billing.subscription_grants TO axiom_app;

GRANT UPDATE(refunded_windows) ON TABLE billing.subscription_grants TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(name) ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(price) ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(quota) ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(window_seconds) ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(term_seconds) ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(active) ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(rev) ON TABLE billing.subscription_plans TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE billing.subscription_plans TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.subscription_refund_requests TO axiom_app;

GRANT UPDATE(approved_by) ON TABLE billing.subscription_refund_requests TO axiom_app;

GRANT UPDATE(state) ON TABLE billing.subscription_refund_requests TO axiom_app;

GRANT UPDATE(finished_at) ON TABLE billing.subscription_refund_requests TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.subscription_refunds TO axiom_app;

GRANT UPDATE(revoked) ON TABLE billing.subscription_refunds TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.subscription_windows TO axiom_app;

GRANT UPDATE(used) ON TABLE billing.subscription_windows TO axiom_app;

GRANT UPDATE(reserved) ON TABLE billing.subscription_windows TO axiom_app;

GRANT UPDATE(voided_at) ON TABLE billing.subscription_windows TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.subscriptions TO axiom_app;

GRANT UPDATE(ends_at) ON TABLE billing.subscriptions TO axiom_app;

GRANT UPDATE(current_seq) ON TABLE billing.subscriptions TO axiom_app;

GRANT UPDATE(state) ON TABLE billing.subscriptions TO axiom_app;

GRANT UPDATE(closed_at) ON TABLE billing.subscriptions TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE billing.top_up_buffers TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.transactions TO axiom_app;

GRANT USAGE ON SEQUENCE billing.transactions_seq TO axiom_app;

GRANT SELECT,INSERT ON TABLE billing.unknown_items TO axiom_app;

GRANT UPDATE(cleared_at) ON TABLE billing.unknown_items TO axiom_app;


ALTER TABLE ONLY billing.audience_publications FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.balance_notices FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.balances FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.charge_correction_cursor FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.charge_corrections FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.credit_batches FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.entries FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.exchange_rates FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.exchange_sync FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.expiry_cursor FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.fallbacks FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.holders FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.price_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.price_publications FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.price_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.pricing_rule_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.pricing_rules FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.reservations FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.segment_settings FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.spend FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.subscription_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.subscription_plans FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.subscription_refund_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.subscription_refunds FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.subscription_windows FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.subscriptions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.top_up_buffers FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.transactions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY billing.unknown_items FORCE ROW LEVEL SECURITY;
ALTER TABLE billing.audience_publications ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.balance_notices ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.balances ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.charge_correction_cursor ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.charge_corrections ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.credit_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.exchange_rates ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.exchange_sync ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.expiry_cursor ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.fallbacks ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.holders ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.price_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.price_publications ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.price_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.pricing_rule_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.pricing_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.segment_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.spend ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.subscription_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.subscription_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.subscription_refund_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.subscription_refunds ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.subscription_windows ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.top_up_buffers ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.unknown_items ENABLE ROW LEVEL SECURITY;
