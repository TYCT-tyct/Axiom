-- +goose Up

-- Why a proposal waits for the owner, as the server decided it when the agent asked (docs/domains/operations.md
-- 「风险分级与审批」): the autonomous cap limits it goes over, what the price envelope does not cover, a paused scope, or
-- a class that always asks. The same function that decides it waits writes it, so a page never re-derives it from the
-- summary. A nullable new column: a node that predates it inserts no reasons, and the API leaves them out.
ALTER TABLE operations.approvals ADD COLUMN reasons text[] CHECK (cardinality(reasons) BETWEEN 1 AND 16);

-- +goose Down
ALTER TABLE operations.approvals DROP COLUMN reasons;
