-- +goose Up

-- A refund is the third money operation (docs/domains/operations.md「转账的累计金额上限」): the agent proposes returning
-- part of a paid order, the owner approves it, the payment channel sends it, and the cash it returns counts against
-- the caps of `money.refund` in the order's site until the channel's outcome commits or releases it. Only the CHECKs
-- widen; a node that predates them never writes the new values.
ALTER TABLE operations.cap_policies DROP CONSTRAINT cap_policies_category_check,
    ADD CONSTRAINT cap_policies_category_check CHECK (category IN ('money.top_up', 'money.credit', 'money.refund'));
ALTER TABLE operations.money_actions DROP CONSTRAINT money_actions_category_check,
    ADD CONSTRAINT money_actions_category_check CHECK (category IN ('money.top_up', 'money.credit', 'money.refund'));
ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting', 'top_up', 'price', 'gift', 'refund'));
-- The outcome job reads the operations waiting for their target's outcome every minute; there are few.
CREATE INDEX operations_executing ON operations.operations (tool, id) WHERE state = 'executing';

-- +goose Down
DROP INDEX operations.operations_executing;
ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting', 'top_up', 'price', 'gift'));
ALTER TABLE operations.money_actions DROP CONSTRAINT money_actions_category_check,
    ADD CONSTRAINT money_actions_category_check CHECK (category IN ('money.top_up', 'money.credit'));
ALTER TABLE operations.cap_policies DROP CONSTRAINT cap_policies_category_check,
    ADD CONSTRAINT cap_policies_category_check CHECK (category IN ('money.top_up', 'money.credit'));
