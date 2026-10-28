-- +goose Up

-- Query calls never hold a database transaction. Two expiring tokens fence completions from a crashed worker.
CREATE TABLE payments.query_health (
    merchant_id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    failures integer NOT NULL DEFAULT 0 CHECK (failures BETWEEN 0 AND 4),
    paused_until timestamptz NOT NULL DEFAULT '1970-01-01 UTC',
    first_token uuid,
    first_until timestamptz NOT NULL DEFAULT '1970-01-01 UTC',
    second_token uuid,
    second_until timestamptz NOT NULL DEFAULT '1970-01-01 UTC',
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants(id, site_id)
);
ALTER TABLE payments.query_health ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY site ON payments.query_health USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));
GRANT SELECT, INSERT, UPDATE ON payments.query_health TO axiom_app;

ALTER TABLE payments.discrepancies
    ADD COLUMN investigated_by text,
    ADD COLUMN investigated_at timestamptz,
    ADD COLUMN note text CHECK (char_length(note) BETWEEN 1 AND 1000),
    ADD CONSTRAINT investigation_complete CHECK (
        (investigated_by IS NULL AND investigated_at IS NULL AND note IS NULL)
        OR (state = 'resolved' AND investigated_by IS NOT NULL AND investigated_at IS NOT NULL AND note IS NOT NULL));
GRANT UPDATE (state, investigated_by, investigated_at, note) ON payments.discrepancies TO axiom_app;
CREATE INDEX discrepancies_by_id ON payments.discrepancies (state, id DESC);
