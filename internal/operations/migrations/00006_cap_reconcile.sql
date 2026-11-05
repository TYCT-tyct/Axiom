-- +goose Up

-- The reconciliation of the cap counters (docs/domains/operations.md「失败与对账」). Every minute the counters of the
-- current UTC day and ISO week are added up again from money_actions; a scope and category whose counters differ is
-- paused: every proposal there waits for the owner until the owner has the counters recomputed. Nullable, so a node
-- that predates it reads and writes the policies as before.
ALTER TABLE operations.cap_policies ADD COLUMN paused_at timestamptz;
GRANT UPDATE (paused_at) ON operations.cap_policies TO axiom_app;

-- The reconciliation reads the money actions of the current week only.
CREATE INDEX money_actions_week ON operations.money_actions (week_start);

-- +goose Down
DROP INDEX operations.money_actions_week;
REVOKE UPDATE (paused_at) ON operations.cap_policies FROM axiom_app;
ALTER TABLE operations.cap_policies DROP COLUMN paused_at;
