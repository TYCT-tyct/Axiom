-- +goose Up

-- A call whose settlement was not written before its response ended is recorded before the ending anyway, with no
-- amounts. That record reaches the call log only when no settlement will replace it, with this column set: the
-- cleanup took the reservation, or the node died first (README「写入」). Old nodes do not name the column, so what they
-- write is settled, as it always was. The table stays insert-only.
ALTER TABLE observability.call_log ADD COLUMN billing_pending boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE observability.call_log DROP COLUMN billing_pending;
