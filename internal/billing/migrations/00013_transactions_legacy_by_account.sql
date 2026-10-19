-- +goose NO TRANSACTION
-- +goose Up

-- The transactions from before the sequence (seq NULL), which the replay reads first, in the order of their time and ID.
-- No transaction is added to this set after this release, so the index is built once and not written to again. Built
-- CONCURRENTLY (see 00012).
CREATE INDEX CONCURRENTLY transactions_legacy_by_account ON billing.transactions (account_id, created_at, id) WHERE seq IS NULL;
