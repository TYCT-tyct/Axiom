-- +goose Up

-- Orders, their events and the site settings the order limits read (README.md「数据」). An order is written before its
-- channel is called, so a channel order always has a row here; the money columns (paid_*, arrival_*) are written when a
-- payment is confirmed, which is the callback's work (README.md「实现拆分」E1-2c).

-- Each column is NULL where the site keeps the default in code (README.md「数据」), and the code holds what a NULL
-- means: 3 pending orders per account, 5 cancellations an hour, no daily limit, 100 credits for a first top-up.
ALTER TABLE payments.settings
    ADD COLUMN pending_limit bigint CHECK (pending_limit > 0),
    ADD COLUMN cancel_limit bigint CHECK (cancel_limit > 0),
    ADD COLUMN cancel_window_seconds bigint CHECK (cancel_window_seconds BETWEEN 1 AND 86400),
    ADD COLUMN daily_credits bigint CHECK (daily_credits > 0),
    ADD COLUMN first_credits bigint CHECK (first_credits > 0);

-- Amounts are minor units of the paying currency; credits are billionths of the site's; the rates are exact fractions
-- stored as integers of any size, since a reference term goes to 10¹⁸ and the buffer multiplies it.
CREATE TABLE payments.orders (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    method text NOT NULL CHECK (method ~ '^[a-z][a-z0-9_.]{0,31}$'),
    state text NOT NULL CHECK (state IN
        ('pending', 'paid', 'expired', 'cancelled', 'failed', 'review', 'partially_refunded', 'refunded', 'charged_back')),
    idem_key text NOT NULL CHECK (octet_length(idem_key) BETWEEN 1 AND 128),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    credits bigint NOT NULL CHECK (credits > 0),
    site_currency text NOT NULL CHECK (site_currency ~ '^[A-Z]{3}$'),
    pay_currency text NOT NULL CHECK (pay_currency ~ '^[A-Z]{3}$'),
    total bigint NOT NULL CHECK (total > 0),
    fee bigint NOT NULL CHECK (fee BETWEEN 0 AND total),
    fee_ppm integer NOT NULL CHECK (fee_ppm BETWEEN 0 AND 500000),
    fee_fixed bigint NOT NULL CHECK (fee_fixed >= 0),
    rate_id uuid,
    ref_num numeric NOT NULL CHECK (ref_num > 0 AND scale(ref_num) = 0),
    ref_den numeric NOT NULL CHECK (ref_den > 0 AND scale(ref_den) = 0),
    applied_num numeric NOT NULL CHECK (applied_num > 0 AND scale(applied_num) = 0),
    applied_den numeric NOT NULL CHECK (applied_den > 0 AND scale(applied_den) = 0),
    trade_no text NOT NULL DEFAULT '',
    checkout jsonb NOT NULL DEFAULT '{}' CHECK (octet_length(checkout::text) <= 8192),
    expires_at timestamptz NOT NULL,
    paid_total bigint CHECK (paid_total > 0),
    paid_fee bigint CHECK (paid_fee >= 0),
    credited bigint CHECK (credited > 0),
    paid_at timestamptz,
    arrival_rate_id uuid,
    arrival_ref_num numeric CHECK (arrival_ref_num > 0 AND scale(arrival_ref_num) = 0),
    arrival_ref_den numeric CHECK (arrival_ref_den > 0 AND scale(arrival_ref_den) = 0),
    arrival_num numeric CHECK (arrival_num > 0 AND scale(arrival_num) = 0),
    arrival_den numeric CHECK (arrival_den > 0 AND scale(arrival_den) = 0),
    client_ip inet NOT NULL,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (account_id, idem_key),
    UNIQUE (id, site_id, account_id),
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants (id, site_id),
    -- A rate is locked exactly when the paying currency is not the site's.
    CHECK ((rate_id IS NULL) = (pay_currency = site_currency)),
    -- The arrival rate is a set of five columns: all of them, or none.
    CHECK (num_nonnulls(arrival_rate_id, arrival_ref_num, arrival_ref_den, arrival_num, arrival_den) IN (0, 5)),
    -- Paid is never a state without what it paid and credited, so a replay can always tell what was banked.
    CHECK (state NOT IN ('paid', 'partially_refunded', 'refunded', 'charged_back') OR
        (paid_at IS NOT NULL AND paid_total IS NOT NULL AND paid_fee IS NOT NULL AND credited IS NOT NULL
         AND paid_fee BETWEEN 0 AND paid_total))
);
CREATE INDEX orders_by_trade ON payments.orders (merchant_id, trade_no);
CREATE INDEX orders_expiring ON payments.orders (expires_at, id) WHERE state = 'pending';
CREATE INDEX orders_by_account ON payments.orders (account_id, created_at DESC);
CREATE INDEX orders_by_merchant ON payments.orders (merchant_id, created_at);

-- What happened to an order, appended by whoever changed it; never updated or deleted.
CREATE TABLE payments.order_events (
    id uuid PRIMARY KEY,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    source text NOT NULL CHECK (source IN ('notify', 'query', 'user', 'admin', 'job')),
    kind text NOT NULL CHECK (char_length(kind) BETWEEN 1 AND 64),
    from_state text NOT NULL,
    to_state text NOT NULL,
    trade_no text NOT NULL DEFAULT '',
    amount bigint CHECK (amount > 0),
    raw text NOT NULL DEFAULT '' CHECK (octet_length(raw) <= 8192),
    FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders (id, site_id, account_id)
);
CREATE INDEX order_events_by_order ON payments.order_events (order_id, at);

-- Account data: the account's own scope, the site's, or the platform's (README.md「数据」).
ALTER TABLE payments.orders ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.order_events ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY account ON payments.orders USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY account ON payments.order_events USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));
GRANT SELECT, INSERT, UPDATE ON payments.orders TO axiom_app;
GRANT SELECT, INSERT ON payments.order_events TO axiom_app;
