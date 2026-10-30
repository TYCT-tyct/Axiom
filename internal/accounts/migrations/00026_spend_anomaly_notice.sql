-- +goose Up
-- Account-level spend anomalies are a closable user reminder like the other notices ([docs/domains/accounts.md]).
-- The default keeps existing rows and older nodes, whose INSERT omits the column, opted in.
ALTER TABLE accounts.notification_preferences ADD COLUMN spend_anomaly boolean NOT NULL DEFAULT true;
GRANT UPDATE (spend_anomaly) ON accounts.notification_preferences TO axiom_app;

ALTER TABLE accounts.user_notices DROP CONSTRAINT IF EXISTS user_notices_kind_check;
ALTER TABLE accounts.user_notices ADD CONSTRAINT user_notices_kind_check
    CHECK (kind IN ('low_balance', 'top_up', 'factor_changed', 'spend_anomaly'));
