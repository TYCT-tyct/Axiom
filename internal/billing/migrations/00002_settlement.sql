-- +goose Up

-- Settlement, cleanup, correction, credits and the ledger (docs/domains/billing.md「预留与结算」「持有者与截止时间」
-- 「用量来源与暂估」「账本」「额度批次」「免单规则」). Money is in billionths of a credit, in the site's own credits.

-- The reservation now ends in one of the states README.md「预留的状态」 lists. prices are the kept entries' locked
-- price schedules in canonical JSON, by route index ('' for a dropped entry), so cleanup and corrections can price
-- usage without the process that admitted it. entry is the route index the charge was priced at; charged what the
-- user paid so far, price what the rules asked before the floor capped it; correct_by ends the hour in which an
-- estimated or cleaned-up charge may still be corrected, holding amount - charged reserved until then.
ALTER TABLE billing.reservations
    DROP CONSTRAINT reservations_state_check,
    ADD CONSTRAINT reservations_state_check CHECK (state IN ('pending', 'released', 'settled', 'estimated',
        'cleanup_pending', 'cleanup_settled', 'cleanup_released')),
    ADD COLUMN prices text[] NOT NULL DEFAULT '{}',
    ADD COLUMN entry integer CHECK (entry >= 0),
    ADD COLUMN charged bigint NOT NULL DEFAULT 0 CHECK (charged >= 0),
    ADD COLUMN price bigint NOT NULL DEFAULT 0 CHECK (price >= 0),
    ADD COLUMN correct_by timestamptz,
    ADD COLUMN applied_revision bigint NOT NULL DEFAULT 0;

-- Released and settled are final; every other state still changes.
DROP POLICY updates ON billing.reservations;
CREATE POLICY updates ON billing.reservations FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND state NOT IN ('released', 'settled'))
    WITH CHECK (platform.in_scope(site_id, account_id));

-- The ledger: one transaction per event, idempotent on (request_id, attempt_seq, kind, revision), whose entries
-- balance to zero: a debit is positive, a credit negative. Every entry carries the tenant whose event it records,
-- which row-level security reads; book is the account of the books it moves (docs/domains/billing.md「账本」).
-- Monthly partitioning comes with the volume that needs it.
CREATE TABLE billing.transactions (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost', 'cost_adjust')),
    request_id uuid NOT NULL, -- the call, or the credit's own ID
    attempt_seq integer NOT NULL DEFAULT 0,
    revision bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (request_id, attempt_seq, kind, revision)
);

CREATE TABLE billing.entries (
    transaction_id uuid NOT NULL REFERENCES billing.transactions,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    book text NOT NULL CHECK (book IN ('user_balance', 'revenue', 'platform_borne', 'supplier_cost',
        'supplier_prepaid', 'gift_expense', 'cash')),
    amount bigint NOT NULL CHECK (amount <> 0)
);
CREATE INDEX entries_by_transaction ON billing.entries (transaction_id);

-- A credit's batch: where it came from, and for a payment what one credit cost. Allocation and expiry replay later.
CREATE TABLE billing.credit_batches (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    source text NOT NULL CHECK (source IN ('gift', 'offline')),
    amount bigint NOT NULL CHECK (amount > 0),
    paid_price text CHECK (paid_price ~ '^[0-9]+(\.[0-9]+)?$'), -- the payment per credit, in its currency
    paid_currency text CHECK (paid_currency ~ '^[A-Z]{3}$'),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((source = 'offline') = (paid_price IS NOT NULL AND paid_currency IS NOT NULL))
);

-- What an account's waived calls cost the platform on one UTC day, against the daily cap (「免单规则」).
CREATE TABLE billing.waivers (
    account_id uuid NOT NULL,
    day date NOT NULL,
    site_id uuid NOT NULL,
    cost bigint NOT NULL CHECK (cost >= 0),
    PRIMARY KEY (account_id, day)
);

ALTER TABLE billing.transactions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.transactions FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.transactions FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE billing.entries ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.entries FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.entries FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE billing.credit_batches ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.credit_batches FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.credit_batches FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE billing.waivers ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.waivers FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.waivers FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON billing.waivers FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));

-- The ledger only grows: no UPDATE at all. balance and spent follow it, in the same transaction.
GRANT SELECT, INSERT ON billing.transactions, billing.entries, billing.credit_batches, billing.waivers TO axiom_app;
GRANT UPDATE (cost) ON billing.waivers TO axiom_app;
GRANT UPDATE (balance) ON billing.balances TO axiom_app;
GRANT UPDATE (spent) ON billing.spend TO axiom_app;
GRANT UPDATE (entry, charged, price, correct_by, applied_revision) ON billing.reservations TO axiom_app;
