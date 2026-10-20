-- +goose Up

-- The constraint 00007 added NOT VALID, now checked against the rows already there. VALIDATE takes a lock that lets
-- reads and writes go on.
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_conversion_state_check;
