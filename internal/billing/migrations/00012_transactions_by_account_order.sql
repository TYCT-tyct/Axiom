-- +goose NO TRANSACTION
-- +goose Up

-- The replay reads an account's transactions in the order (seq, created_at, id) a page at a time from a key
-- (internal/billing/queries/allocation.sql). A transaction from before the sequence has no seq and reads as 0. Built
-- CONCURRENTLY so that settlements keep inserting; a build that fails leaves an invalid index of this name, which is
-- dropped before the file is run again.
CREATE INDEX CONCURRENTLY transactions_by_account_order ON billing.transactions (account_id, (coalesce(seq, 0)), created_at, id);
