-- +goose Up

-- Where the expiry job's scan of accounts stopped, so that a run cut short by its time budget goes on from there and
-- the accounts after a slow one are reached. One row, platform scope only.
CREATE TABLE billing.expiry_cursor (
    one boolean PRIMARY KEY DEFAULT true CHECK (one),
    after uuid NOT NULL
);
ALTER TABLE billing.expiry_cursor ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.expiry_cursor FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.expiry_cursor FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON billing.expiry_cursor FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT ON billing.expiry_cursor TO axiom_app;
GRANT UPDATE (after) ON billing.expiry_cursor TO axiom_app;
