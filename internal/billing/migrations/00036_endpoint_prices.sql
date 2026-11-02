-- +goose Up

-- A published cost-plus version's sale price for each endpoint, by endpoint UUID
-- (docs/domains/billing.md「定价」「价格版本」): the price the serving endpoint is charged at, frozen with the version.
-- NULL is a version whose one schedule is every endpoint's price: every row written before this release, and every
-- fixed or reference-multiplier rule after it. The schedule column keeps the model-level envelope in both cases, so a
-- node that predates this column still charges one price that covers every endpoint's floor, and a new node reading an
-- old version serves it as a shared price.
ALTER TABLE billing.price_versions ADD COLUMN IF NOT EXISTS endpoints jsonb;
