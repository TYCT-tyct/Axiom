-- +goose NO TRANSACTION
-- +goose Up

-- The admin lists one site's orders and refunds newest first, with (created_at, id) as the page cursor. Without these,
-- a site-wide page would read every site's orders through the existing account and merchant indexes. They are built
-- concurrently so order and refund writes are not blocked while they build.
DROP INDEX CONCURRENTLY IF EXISTS payments.orders_by_site_created;
CREATE INDEX CONCURRENTLY orders_by_site_created ON payments.orders (site_id, created_at DESC, id DESC);

-- The refund queue filters by state first and then pages: the site and state are the prefix, the cursor follows.
DROP INDEX CONCURRENTLY IF EXISTS payments.refunds_by_site_state_created;
CREATE INDEX CONCURRENTLY refunds_by_site_state_created ON payments.refunds (site_id, state, created_at DESC, id DESC);
