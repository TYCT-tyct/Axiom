-- +goose Up

-- A call whose settlement was not written before its response ended is still recorded before the ending, with no
-- amounts and this column set; the settled record that follows it through the spill completes the row (README「写入」).
-- Old nodes do not name the column, so what they write is settled, as it always was.
ALTER TABLE observability.call_log ADD COLUMN billing_pending boolean NOT NULL DEFAULT false;

-- Completing a pending row is the one change the application may make to a call: the settlement's columns, of a row
-- still pending, to a settled one. A row can therefore change once, and nothing else in the table can change at all.
GRANT UPDATE (usage, service_tier, price_version, price_nano, free_of_charge, physical_input, billing_pending)
    ON observability.call_log TO axiom_app;
CREATE POLICY settle_once ON observability.call_log AS RESTRICTIVE FOR UPDATE
    USING (billing_pending) WITH CHECK (NOT billing_pending);

-- +goose Down
DROP POLICY settle_once ON observability.call_log;
REVOKE UPDATE (usage, service_tier, price_version, price_nano, free_of_charge, physical_input, billing_pending)
    ON observability.call_log FROM axiom_app;
ALTER TABLE observability.call_log DROP COLUMN billing_pending;
