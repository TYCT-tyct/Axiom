-- +goose Up

-- Test generations are paid by the platform. Match their charges normally, but never judge the endpoint by their
-- deliberately bounded estimate (H-I9). Existing calls keep their meaning through the default.
ALTER TABLE suppliers.attempt_costs ADD COLUMN test boolean NOT NULL DEFAULT false;
