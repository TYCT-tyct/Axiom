-- +goose Up

-- Settlement, cleanup, correction, credits and the ledger (docs/domains/billing.md「预留与结算」「持有者与截止时间」
-- 「用量来源与暂估」「账本」「额度批次」「免单规则」). Money is in billionths of a credit, in the site's own credits.

-- The reservation now ends in one of the states README.md「预留的状态」 lists. prices are the kept entries' locked
-- price schedules in canonical JSON, by route index ('' for a dropped entry), so cleanup and corrections can price
-- usage without the process that admitted it. charged_seq and entry are the attempt the user is charged for and the
-- route index it bound; charged what the user paid so far, price what the rules asked before the floor capped it;
-- waived whether only per-use items were charged, which a correction keeps; estimated whether the charge rests on
-- usage the upstream did not report; unpriced whether it has usage no price covers yet (docs/domains/billing.md
-- 「价目表缺项」), which keeps it held, out of cleanup's reach, until a supplier record prices it; final_usage the
-- charged usage as Final gives it, unknown usage included (README.md「结算与账本」); correct_by ends the hour in which
-- an estimated or cleaned-up charge may still be corrected, holding amount - charged reserved until then.
ALTER TABLE billing.reservations
    DROP CONSTRAINT reservations_state_check,
    ADD CONSTRAINT reservations_state_check CHECK (state IN ('pending', 'released', 'settled', 'estimated',
        'cleanup_pending', 'cleanup_settled', 'cleanup_released')),
    ADD COLUMN prices text[] NOT NULL DEFAULT '{}',
    ADD COLUMN charged_seq integer CHECK (charged_seq >= 1),
    ADD COLUMN entry integer CHECK (entry >= 0),
    ADD COLUMN charged bigint NOT NULL DEFAULT 0 CHECK (charged >= 0),
    ADD COLUMN price bigint NOT NULL DEFAULT 0 CHECK (price >= 0),
    ADD COLUMN waived boolean NOT NULL DEFAULT false,
    ADD COLUMN estimated boolean NOT NULL DEFAULT false,
    ADD COLUMN unpriced boolean NOT NULL DEFAULT false,
    ADD COLUMN final_usage jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN correct_by timestamptz;

-- The newest supplier record applied to each attempt: a record's revisions belong to its attempt, and every attempt's
-- cost follows its own record whatever state the reservation is in.
CREATE TABLE billing.corrections (
    request_id uuid NOT NULL,
    attempt_seq integer NOT NULL CHECK (attempt_seq >= 1),
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision >= 1),
    PRIMARY KEY (request_id, attempt_seq)
);

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
        'supplier_prepaid', 'gift_expense')),
    amount bigint NOT NULL CHECK (amount <> 0)
);
CREATE INDEX entries_by_transaction ON billing.entries (transaction_id);
CREATE INDEX entries_user_balance ON billing.entries (account_id) INCLUDE (amount) WHERE book = 'user_balance';

-- A credit's batch: where it came from. Only gifts for now: an offline payment needs its cash booked at what was
-- paid, in its currency at a locked rate, which comes with payments. Allocation and expiry replay later.
CREATE TABLE billing.credit_batches (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    source text NOT NULL CHECK (source IN ('gift')),
    amount bigint NOT NULL CHECK (amount > 0),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
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

ALTER TABLE billing.corrections ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.corrections FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.corrections FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON billing.corrections FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE billing.waivers ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.waivers FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.waivers FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON billing.waivers FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));

-- The ledger only grows: no UPDATE at all. balance and spent follow it, in the same transaction.
GRANT SELECT, INSERT ON billing.transactions, billing.entries, billing.credit_batches, billing.waivers,
    billing.corrections TO axiom_app;
GRANT UPDATE (cost) ON billing.waivers TO axiom_app;
GRANT UPDATE (revision) ON billing.corrections TO axiom_app;
GRANT UPDATE (balance) ON billing.balances TO axiom_app;
GRANT UPDATE (spent) ON billing.spend TO axiom_app;
GRANT UPDATE (charged_seq, entry, charged, price, waived, estimated, unpriced, final_usage, correct_by)
    ON billing.reservations TO axiom_app;
