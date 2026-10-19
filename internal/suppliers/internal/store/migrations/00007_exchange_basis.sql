-- +goose Up

-- No cross-module FK. NULL preserves the unknown basis of historical and old-node writes. The state's check is added
-- NOT VALID, which checks new rows without scanning the table under an exclusive lock; 00008 validates it.
ALTER TABLE suppliers.attempt_costs
    ADD COLUMN unit_value_id uuid,
    ADD COLUMN exchange_rate_id uuid,
    ADD COLUMN conversion_from text,
    ADD COLUMN conversion_to text,
    ADD COLUMN conversion_state text;
ALTER TABLE suppliers.attempt_costs
    ADD CONSTRAINT attempt_costs_conversion_state_check
    CHECK (conversion_state IN ('same_currency','converted','missing_rate','overflow','unpriced','not_executed')) NOT VALID;
