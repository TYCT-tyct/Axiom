-- +goose Up
-- Where the correction job has read the suppliers' charge events: (created_at, id), so an event is consumed once and a
-- late one is still read. One row, owned by the queue's leader.
CREATE TABLE billing.charge_correction_cursor (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    after_at timestamptz NOT NULL DEFAULT 'epoch',
    after_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000'
);
INSERT INTO billing.charge_correction_cursor DEFAULT VALUES;

-- Charge events whose settlement was not ready when they were read: still pending, or not written yet. The job retries
-- them until the settlement is final, so one slow request cannot hold every other account's correction back. A row
-- disappears once the event is corrected or found permanently ineligible.
CREATE TABLE billing.charge_corrections (
    event_id uuid PRIMARY KEY,
    revision integer NOT NULL CHECK (revision >= 1),
    request_id uuid NOT NULL,
    next_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 1),
    last_error text NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 500)
);
CREATE INDEX charge_corrections_due ON billing.charge_corrections (next_at);

-- A correction changes a settlement that has ended: the platform scope alone may write one again, at the revision
-- the supplier's event names. Tenant requests keep the pending-only policy.
CREATE POLICY corrections ON billing.reservations FOR UPDATE
    USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());

-- The correction job reads suppliers' tables in the platform scope; these are platform rows, not tenant rows.
ALTER TABLE billing.charge_correction_cursor ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON billing.charge_correction_cursor USING (platform.in_platform_scope());
ALTER TABLE billing.charge_corrections ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON billing.charge_corrections USING (platform.in_platform_scope());

GRANT SELECT, INSERT, UPDATE ON billing.charge_correction_cursor TO axiom_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON billing.charge_corrections TO axiom_app;
