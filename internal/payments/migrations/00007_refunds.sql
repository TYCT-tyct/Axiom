-- +goose Up

ALTER TABLE payments.orders ADD COLUMN refund_with_fee boolean;
ALTER TABLE payments.settings ADD COLUMN refund_with_fee boolean,
    ADD COLUMN amount_mismatch_policy text CHECK (amount_mismatch_policy IN ('manual', 'actual'));

CREATE TABLE payments.refunds (
    id uuid PRIMARY KEY,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('frozen', 'sending', 'pending', 'succeeded', 'failed')),
    via text NOT NULL CHECK (via IN ('channel', 'offline')),
    credits bigint NOT NULL CHECK (credits > 0),
    requested_credits bigint NOT NULL CHECK (requested_credits >= credits),
    amount bigint NOT NULL CHECK (amount > 0),
    with_fee boolean NOT NULL,
    refund_key boolean NOT NULL,
    provider_ref text NOT NULL DEFAULT '',
    reason text NOT NULL,
    requested_by uuid NOT NULL,
    approved_by uuid,
    idem_key text NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    sent_at timestamptz,
    next_query_at timestamptz NOT NULL DEFAULT clock_timestamp() + interval '1 minute',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    UNIQUE (order_id, idem_key),
    FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id),
    CHECK (approved_by IS NULL OR approved_by <> requested_by),
    CHECK (state NOT IN ('sending', 'pending') OR sent_at IS NOT NULL),
    CHECK ((state IN ('succeeded', 'failed')) = (finished_at IS NOT NULL))
);
CREATE UNIQUE INDEX refunds_one_inflight ON payments.refunds(order_id)
    WHERE state IN ('frozen', 'sending', 'pending');
CREATE INDEX refunds_due ON payments.refunds(next_query_at, id)
    WHERE via = 'channel' AND approved_by IS NOT NULL AND state IN ('frozen', 'sending', 'pending');
CREATE INDEX refunds_cash ON payments.refunds(finished_at, order_id) WHERE state = 'succeeded';
ALTER TABLE payments.refunds ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY account ON payments.refunds USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]));
GRANT SELECT, INSERT, UPDATE ON payments.refunds TO axiom_app;

CREATE TABLE payments.resolutions (
    id uuid PRIMARY KEY,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    refund_id uuid REFERENCES payments.refunds(id),
    outcome text NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'credit')),
    trade_no text NOT NULL,
    amount bigint NOT NULL,
    evidence text NOT NULL CHECK (char_length(evidence) BETWEEN 1 AND 8192),
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1000),
    requested_by uuid NOT NULL,
    approved_by uuid CHECK (approved_by <> requested_by),
    idem_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (order_id, idem_key),
    FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders(id, site_id, account_id),
    CHECK ((outcome = 'credit' AND refund_id IS NULL AND amount > 0 AND trade_no <> '')
        OR (outcome <> 'credit' AND refund_id IS NOT NULL AND amount = 0 AND trade_no = ''))
);
ALTER TABLE payments.resolutions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY site ON payments.resolutions USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]));
GRANT SELECT, INSERT, UPDATE (approved_by) ON payments.resolutions TO axiom_app;
