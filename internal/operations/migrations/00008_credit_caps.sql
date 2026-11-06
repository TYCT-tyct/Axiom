-- +goose Up

-- A credit grant is the second money operation (docs/domains/operations.md「转账的累计金额上限」): the agent proposes
-- giving an account credit, the owner approves it, and its face value counts against the caps of `money.credit` in
-- the account's site. Only the CHECKs widen; a node that predates them never writes the new values.
ALTER TABLE operations.cap_policies DROP CONSTRAINT cap_policies_category_check,
    ADD CONSTRAINT cap_policies_category_check CHECK (category IN ('money.top_up', 'money.credit'));
ALTER TABLE operations.money_actions DROP CONSTRAINT money_actions_category_check,
    ADD CONSTRAINT money_actions_category_check CHECK (category IN ('money.top_up', 'money.credit'));
ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting', 'top_up', 'price', 'gift'));

-- +goose Down
ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting', 'top_up', 'price'));
ALTER TABLE operations.money_actions DROP CONSTRAINT money_actions_category_check,
    ADD CONSTRAINT money_actions_category_check CHECK (category IN ('money.top_up'));
ALTER TABLE operations.cap_policies DROP CONSTRAINT cap_policies_category_check,
    ADD CONSTRAINT cap_policies_category_check CHECK (category IN ('money.top_up'));
