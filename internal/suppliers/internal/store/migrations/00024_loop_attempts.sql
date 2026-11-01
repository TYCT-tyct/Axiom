-- +goose Up

-- Every supplier request of a call is an attempt with its own number, cost row and charge key: the fallbacks routing
-- makes (platform.MaxAttempts, 3) and each further sampling of a model-side loop (platform.MaxServerToolSteps, 30), so
-- the number runs to platform.MaxAttemptSeq, 33. The range only widens: every row an old node writes still fits, and
-- the key stays (request_id, attempt_seq), so an old node's settlement keeps working mid-release. NOT VALID because the
-- rows already there fit the narrower check; nothing needs to scan them under the ALTER's lock.
ALTER TABLE suppliers.attempt_costs DROP CONSTRAINT attempt_costs_attempt_seq_check,
    ADD CONSTRAINT attempt_costs_attempt_seq_check CHECK (attempt_seq BETWEEN 1 AND 33) NOT VALID;

-- +goose Down

ALTER TABLE suppliers.attempt_costs DROP CONSTRAINT attempt_costs_attempt_seq_check,
    ADD CONSTRAINT attempt_costs_attempt_seq_check CHECK (attempt_seq BETWEEN 1 AND 3) NOT VALID;
