-- +goose Up

-- The owner's limits on the agent's requests and money writes, beside its alerts' (docs/domains/operations.md「运营凭证」).
-- Nullable: a row an old node made has none (its update leaves them be), and a limit never set is its ceiling in the code.
ALTER TABLE operations.agent_limits
    ADD COLUMN requests_per_minute integer CHECK (requests_per_minute BETWEEN 1 AND 600),
    ADD COLUMN money_writes_per_minute integer CHECK (money_writes_per_minute BETWEEN 1 AND 30),
    ADD COLUMN money_writes_per_hour integer CHECK (money_writes_per_hour BETWEEN 1 AND 300);

GRANT UPDATE (requests_per_minute, money_writes_per_minute, money_writes_per_hour) ON operations.agent_limits TO axiom_app;

-- +goose Down
ALTER TABLE operations.agent_limits
    DROP COLUMN requests_per_minute, DROP COLUMN money_writes_per_minute, DROP COLUMN money_writes_per_hour;
