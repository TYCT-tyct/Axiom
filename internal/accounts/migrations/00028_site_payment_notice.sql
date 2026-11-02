-- +goose Up
-- A secondary site's payment configuration change is a mandatory notice to its owner ([docs/domains/payments.md]).
ALTER TABLE accounts.user_notices DROP CONSTRAINT IF EXISTS user_notices_kind_check;
ALTER TABLE accounts.user_notices ADD CONSTRAINT user_notices_kind_check
    CHECK (kind IN ('low_balance', 'top_up', 'factor_changed', 'spend_anomaly', 'site_payment_changed'));
