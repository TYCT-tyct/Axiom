-- +goose Up

-- What allocating and expiring credit batches need (internal/billing/README.md「额度批次的分配与到期」,
-- docs/domains/billing.md「额度批次」). Everything here only adds: a node of the previous release keeps inserting
-- transactions and never meets a column it does not know, since each new column has a default.

-- The order of an account's ledger transactions. created_at and the IDs cannot give it: a clock can step back, and
-- two transactions of one settlement can share a microsecond. A sequence value is taken by the INSERT, which runs under
-- the account's balance row lock (README.md「写入的数据」), so the values of one account's user-balance movements rise
-- in the order they commit. Adding the column rewrites the table, which numbers the existing rows once, in the order
-- they are stored: before this release no batch expires, so that order decides nothing.
CREATE SEQUENCE billing.transactions_seq;
ALTER TABLE billing.transactions ADD COLUMN seq bigint NOT NULL DEFAULT nextval('billing.transactions_seq');
ALTER SEQUENCE billing.transactions_seq OWNED BY billing.transactions.seq;
CREATE UNIQUE INDEX transactions_by_account_seq ON billing.transactions (account_id, seq);
GRANT USAGE ON SEQUENCE billing.transactions_seq TO axiom_app;

-- A batch expiring is a ledger event of its own: the user's balance against the income of credit that lapsed.
ALTER TABLE billing.transactions
    DROP CONSTRAINT transactions_kind_check,
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost', 'cost_adjust',
        'expire'));
ALTER TABLE billing.entries
    DROP CONSTRAINT entries_book_check,
    ADD CONSTRAINT entries_book_check CHECK (book IN ('user_balance', 'revenue', 'platform_borne', 'supplier_cost',
        'supplier_prepaid', 'gift_expense', 'credit_expired'));

-- What the allocation has put on a batch: consumed by charges, expired by a write-off. Both start at zero, which is
-- what a batch is before the allocation has replayed the account, so no row needs filling in. What is left of a batch
-- is amount - consumed - expired.
ALTER TABLE billing.credit_batches
    ADD COLUMN consumed bigint NOT NULL DEFAULT 0 CHECK (consumed >= 0),
    ADD COLUMN expired bigint NOT NULL DEFAULT 0 CHECK (expired >= 0),
    ADD CONSTRAINT credit_batches_within_amount CHECK (consumed + expired <= amount);
-- The expiry job's query: batches whose time has come and which still hold something.
CREATE INDEX credit_batches_due ON billing.credit_batches (expires_at, account_id)
    WHERE expires_at IS NOT NULL AND consumed + expired < amount;

-- Where the allocation of an account stands. allocated_seq is the last transaction replayed. unfunded is what charges
-- took beyond every batch (the balance below zero); unbatched is credit that belongs to no batch, such as a charge
-- given back after its batch had expired. Both are 0 for an account never replayed.
ALTER TABLE billing.balances
    ADD COLUMN allocated_seq bigint NOT NULL DEFAULT 0,
    ADD COLUMN unfunded bigint NOT NULL DEFAULT 0 CHECK (unfunded >= 0),
    ADD COLUMN unbatched bigint NOT NULL DEFAULT 0 CHECK (unbatched >= 0);

CREATE POLICY updates ON billing.credit_batches FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));
GRANT UPDATE (consumed, expired) ON billing.credit_batches TO axiom_app;
GRANT UPDATE (allocated_seq, unfunded, unbatched) ON billing.balances TO axiom_app;
