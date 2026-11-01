-- +goose Up

-- A chargeback reported by a channel (docs/domains/payments.md「拒付」). One row per channel dispute, keyed by the
-- merchant account and the channel's own ID, so repeated and out-of-order events change nothing. state says whether
-- the money was taken (applied) or given back after a win (reversed); a warning or a dispute that did not match the
-- payment stays recorded and is explained by a discrepancy. credits, cash and fee are what was actually booked, in
-- billionths and payment minor units, so a reversal can reuse them without recomputing history.
CREATE TABLE payments.order_disputes (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    order_id uuid NOT NULL,
    merchant_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_]{0,31}$'),
    provider_id text NOT NULL CHECK (octet_length(provider_id) BETWEEN 1 AND 255),
    payment_intent text NOT NULL DEFAULT '' CHECK (octet_length(payment_intent) <= 1024),
    amount bigint NOT NULL CHECK (amount > 0),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 64),
    status text NOT NULL CHECK (char_length(status) BETWEEN 1 AND 64),
    state text NOT NULL DEFAULT 'recorded' CHECK (state IN ('recorded', 'applied', 'reversed')),
    credits bigint NOT NULL DEFAULT 0 CHECK (credits >= 0),
    cash bigint NOT NULL DEFAULT 0 CHECK (cash >= 0),
    fee bigint NOT NULL DEFAULT 0 CHECK (fee >= 0 AND fee <= cash),
    -- The batch's chargeback revision the ledger event used; 0 only while nothing was booked.
    chargeback_revision bigint NOT NULL DEFAULT 0 CHECK (chargeback_revision >= 0),
    opened_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    applied_at timestamptz,
    reversed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (merchant_id, provider, provider_id),
    FOREIGN KEY (order_id, site_id, account_id) REFERENCES payments.orders (id, site_id, account_id),
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants (id, site_id),
    CHECK ((state = 'recorded') = (applied_at IS NULL)),
    CHECK (state <> 'applied' OR reversed_at IS NULL),
    CHECK (state <> 'reversed' OR reversed_at IS NOT NULL),
    CHECK ((state = 'recorded') = (chargeback_revision = 0))
);
CREATE INDEX order_disputes_by_order ON payments.order_disputes (order_id, opened_at DESC, id DESC);
CREATE INDEX order_disputes_by_merchant_applied ON payments.order_disputes (merchant_id, applied_at) WHERE applied_at IS NOT NULL;
CREATE INDEX order_disputes_by_merchant_reversed ON payments.order_disputes (merchant_id, reversed_at) WHERE reversed_at IS NOT NULL;

ALTER TABLE payments.order_disputes ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY account ON payments.order_disputes USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]));
GRANT SELECT, INSERT, UPDATE ON payments.order_disputes TO axiom_app;

-- +goose Down
DROP TABLE payments.order_disputes;
