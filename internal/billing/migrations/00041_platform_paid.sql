-- +goose NO TRANSACTION
-- +goose Up

-- The dashboard's paid-user, monthly-revenue and retention figures walk paid top-ups in one site's time range
-- (README.md「运营看板」). A partial covering index keeps that a range read of the paid batches only, not a scan of every
-- credit ever granted. Built CONCURRENTLY so top-ups keep inserting; a build that fails leaves an invalid index of this
-- name, which is dropped before the file is run again.
DROP INDEX CONCURRENTLY IF EXISTS billing.credit_batches_by_site_paid_created;
CREATE INDEX CONCURRENTLY credit_batches_by_site_paid_created
    ON billing.credit_batches (site_id, created_at) INCLUDE (account_id, amount)
    WHERE source = 'paid';

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS billing.credit_batches_by_site_paid_created;
