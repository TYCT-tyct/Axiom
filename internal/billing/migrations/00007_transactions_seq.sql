-- +goose Up

-- The order of an account's ledger transactions, for the allocation of credit batches (internal/billing/README.md
-- 「额度批次的分配与到期」, docs/domains/billing.md「额度批次」). created_at and the IDs cannot give it: a clock can step
-- back, and two transactions of one settlement can share a microsecond. A sequence value is taken by the INSERT of a
-- transaction that moves the user's balance, which post runs under the account's balance row lock, so one account's
-- balance movements are numbered in the order they commit.
--
-- Rolling deploy and locks: the migration is split into files that each touch one table and run a short transaction
-- (internal/platform/README.md「迁移」), so it cannot hold this table's lock while waiting on balances, which a
-- settlement holds while waiting on this table. The column is added without a default, which changes only the catalog,
-- and the default is set apart, which does not rewrite the rows already there: they keep seq NULL, and a node of the
-- previous release, which does not name the column, gets the default like any other.
--
-- Rows from before this release have no seq. The replay puts them first, ordered by (created_at, id), a key that never
-- changes, so every replay of them is the same. Their order cannot change a result that matters: until this release
-- nothing allocated charges to batches or expired a batch, so there is no stored allocation for it to contradict.
CREATE SEQUENCE billing.transactions_seq;
ALTER TABLE billing.transactions ADD COLUMN seq bigint;
ALTER TABLE billing.transactions ALTER COLUMN seq SET DEFAULT nextval('billing.transactions_seq');
ALTER SEQUENCE billing.transactions_seq OWNED BY billing.transactions.seq;
GRANT USAGE ON SEQUENCE billing.transactions_seq TO axiom_app;

-- A batch expiring is a ledger event of its own: the user's balance against the income of credit that lapsed. The
-- wider constraint is added NOT VALID, which checks new rows without scanning the table; 00016 validates it.
ALTER TABLE billing.transactions
    DROP CONSTRAINT transactions_kind_check,
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost', 'cost_adjust',
        'expire')) NOT VALID;
