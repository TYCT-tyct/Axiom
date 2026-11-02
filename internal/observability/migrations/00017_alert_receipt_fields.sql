-- +goose Up

-- A receipt freezes the fields shown in the notification. The event fields remain the current sample for rule
-- evaluation, so a later low window can reset a duration rule without rewriting an alert that is already waiting to
-- dispatch. Existing receipts have no snapshot and fall back to the event fields.
ALTER TABLE observability.alert_receipts ADD COLUMN fields jsonb;

-- +goose Down

ALTER TABLE observability.alert_receipts DROP COLUMN fields;
