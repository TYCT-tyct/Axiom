-- +goose NO TRANSACTION
-- +goose Up

-- The replay reads an account's numbered transactions a page at a time, in the order of seq, from a place
-- (internal/billing/queries/allocation.sql). Built CONCURRENTLY so that settlements keep inserting; a build that fails
-- leaves an invalid index of this name, which is dropped before the file is run again.
CREATE INDEX CONCURRENTLY transactions_by_account_seq ON billing.transactions (account_id, seq);
