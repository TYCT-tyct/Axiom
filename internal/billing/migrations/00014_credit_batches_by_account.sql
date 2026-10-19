-- +goose NO TRANSACTION
-- +goose Up

-- An account's batches, for the allocation to load the ones it works on. Built CONCURRENTLY (see 00012).
CREATE INDEX CONCURRENTLY credit_batches_by_account ON billing.credit_batches (account_id);
