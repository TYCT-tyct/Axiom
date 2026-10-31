-- +goose NO TRANSACTION
-- +goose Up

-- The operations dashboard sums one site's calls, revenue and cost over a time window from the reservations, and the
-- cash that moved from the ledger (README.md「运营看板」). Without these indexes each page load would read every
-- reservation and every transaction ever written.
-- Built CONCURRENTLY so settlements and top-ups keep inserting; a build that fails leaves an invalid index of this
-- name, which is dropped before the file is run again.
DROP INDEX CONCURRENTLY IF EXISTS billing.reservations_by_site_created;
CREATE INDEX CONCURRENTLY reservations_by_site_created ON billing.reservations (site_id, created_at);

-- Cash transactions are the few credits and refunds, not the settlements: the kind comes before the time so the scan
-- reads those two ranges and never the call volume.
DROP INDEX CONCURRENTLY IF EXISTS billing.transactions_by_site_kind_created;
CREATE INDEX CONCURRENTLY transactions_by_site_kind_created ON billing.transactions (site_id, kind, created_at, id);
