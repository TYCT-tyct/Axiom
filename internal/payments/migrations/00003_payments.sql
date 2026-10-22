-- +goose Up

ALTER TABLE payments.settings
    ADD COLUMN grace_seconds bigint CHECK (grace_seconds BETWEEN 0 AND 86400),
    ADD COLUMN auto_credit_seconds bigint CHECK (auto_credit_seconds BETWEEN 0 AND 2592000),
    ADD COLUMN post_expiry_query_seconds bigint CHECK (post_expiry_query_seconds BETWEEN 0 AND 2592000);

ALTER TABLE payments.orders
    ADD COLUMN next_query_at timestamptz NOT NULL DEFAULT (transaction_timestamp() + interval '30 seconds'),
    ADD COLUMN query_delay_seconds integer NOT NULL DEFAULT 30 CHECK (query_delay_seconds BETWEEN 30 AND 3600),
    ADD COLUMN next_verify_at timestamptz NOT NULL DEFAULT transaction_timestamp();
CREATE INDEX orders_query_due ON payments.orders (next_query_at, id)
    WHERE state IN ('pending', 'expired', 'cancelled', 'failed');
-- The largest configurable query window is 30 days; historical terminal orders need not be scanned each minute.
CREATE INDEX orders_late_window ON payments.orders (expires_at, next_query_at)
    WHERE state IN ('expired', 'cancelled', 'failed');

-- Identity protection and every query entry point use the same eligibility and defaults. Never release keys while
-- a receipt can still use the grace period or automatic credit policy, even if the configured query window is shorter.
CREATE VIEW payments.order_windows WITH (security_invoker = true) AS
WITH defaults AS (SELECT 300::bigint AS grace, 604800::bigint AS duration),
terms AS (
    SELECT o.id, o.merchant_id, o.state, o.trade_no, o.expires_at, o.next_query_at,
        coalesce(s.grace_seconds, d.grace) AS grace_seconds,
        coalesce(s.auto_credit_seconds, d.duration) AS auto_credit_seconds,
        coalesce(s.post_expiry_query_seconds, d.duration) AS post_expiry_query_seconds
    FROM payments.orders o LEFT JOIN payments.settings s ON s.site_id = o.site_id CROSS JOIN defaults d
)
SELECT terms.*,
    (state = 'pending' OR (state IN ('expired', 'cancelled') OR (state = 'failed' AND trade_no <> ''))
        AND expires_at + make_interval(secs => greatest(grace_seconds, auto_credit_seconds, post_expiry_query_seconds))
            >= clock_timestamp()) AS queryable
FROM terms;
GRANT SELECT ON payments.order_windows TO axiom_app;

CREATE TABLE payments.order_payments (
    id uuid PRIMARY KEY,
    order_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    trade_no text NOT NULL CHECK (octet_length(trade_no) BETWEEN 1 AND 1024),
    amount bigint NOT NULL CHECK (amount > 0),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    source text NOT NULL CHECK (source IN ('notify', 'query', 'admin')),
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (merchant_id, trade_no),
    FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders (id, site_id, account_id),
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants (id, site_id)
);
CREATE INDEX order_payments_by_order ON payments.order_payments (order_id);

CREATE TABLE payments.discrepancies (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid,
    order_id uuid,
    merchant_id uuid NOT NULL,
    kind text NOT NULL,
    dedupe text NOT NULL,
    detail jsonb NOT NULL,
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (merchant_id, kind, dedupe),
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants (id, site_id),
    FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders (id, site_id, account_id),
    CHECK ((order_id IS NULL) = (account_id IS NULL))
);
CREATE INDEX discrepancies_by_site ON payments.discrepancies (site_id, state, created_at);

ALTER TABLE payments.order_payments ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.discrepancies ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY account ON payments.order_payments USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY account ON payments.discrepancies USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));
GRANT SELECT, INSERT ON payments.order_payments, payments.discrepancies TO axiom_app;
