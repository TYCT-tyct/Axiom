-- +goose Up

-- A cost row of a test generation (docs/domains/quality.md「试探」): the platform's own probe, whose estimate is a
-- deliberate upper bound and whose charge, when the supplier bills it, may never be matched to it. Reconciliation skips
-- these rows, so a probe never moves an endpoint's cost state (H-I9). The column is defaulted, so the insert privilege
-- already granted covers it and no existing row changes meaning.
ALTER TABLE suppliers.attempt_costs ADD COLUMN test boolean NOT NULL DEFAULT false;
