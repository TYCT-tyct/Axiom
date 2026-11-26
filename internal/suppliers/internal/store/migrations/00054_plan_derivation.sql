-- +goose Up

-- What each price of a cost plan is the product of (getSupplierEndpointCost's factors): a JSON array of the plan's
-- rates, each with the factors the adapter or a confirmed base multiplied into it, such as a relay's model ratio and
-- group ratio. It is written with the plan, which is append-only like the plan; a plan from before this column, and one
-- whose adapter gives no factors, is NULL.
ALTER TABLE suppliers.sync_plans
    ADD COLUMN derivation jsonb CONSTRAINT sync_plans_derivation_check CHECK (jsonb_typeof(derivation) = 'array');

ALTER TABLE suppliers.cost_schedules
    ADD COLUMN derivation jsonb CONSTRAINT cost_schedules_derivation_check CHECK (jsonb_typeof(derivation) = 'array');
