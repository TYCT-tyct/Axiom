-- +goose NO TRANSACTION
-- +goose Up

-- The expiry job's scan: the batches that still hold something and expire, in the order of their time, so that the
-- query bounds expires_at by the time the run gives it. Built CONCURRENTLY (see 00012).
CREATE INDEX CONCURRENTLY credit_batches_due ON billing.credit_batches (expires_at, account_id)
    WHERE expires_at IS NOT NULL AND consumed + expired < amount;
