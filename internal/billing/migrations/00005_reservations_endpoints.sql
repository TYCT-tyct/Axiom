-- +goose Up

-- The endpoints of the route a reservation keeps its prices for (README.md「写入的数据」, docs/domains/billing.md
-- 「预留与结算」): the endpoint of each kept entry, by route index, the zero UUID for a dropped one, so the same length
-- and order as prices. Cleanup hands them to suppliers as the route of a request whose node was lost, so that a charge
-- the supplier reports later can be told which endpoint it was from; without them a lost request says nothing about
-- where to look.
ALTER TABLE billing.reservations ADD COLUMN endpoints uuid[] NOT NULL DEFAULT '{}';
