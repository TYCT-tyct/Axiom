-- +goose NO TRANSACTION
-- +goose Up

-- Daily payment checks must read one accounting day without scanning the merchant's entire history.
-- Restart failed builds rather than accepting an invalid index left by a partial migration.
DROP INDEX CONCURRENTLY IF EXISTS billing.credit_batches_by_merchant_day;
CREATE INDEX CONCURRENTLY credit_batches_by_merchant_day ON billing.credit_batches (merchant_id, created_at, id)
    WHERE merchant_id IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS billing.transactions_refunds_by_day;
CREATE INDEX CONCURRENTLY transactions_refunds_by_day ON billing.transactions (created_at, request_id) WHERE kind = 'refund';
