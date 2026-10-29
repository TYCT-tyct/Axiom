-- +goose NO TRANSACTION
-- +goose Up

-- Query calls never hold a database transaction. Two expiring tokens fence completions from a crashed worker.
-- The guards let the schema steps resume if a later concurrent index build fails.
CREATE TABLE IF NOT EXISTS payments.query_health (
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
-- +goose StatementBegin
DO $$ BEGIN
    IF NOT EXISTS (SELECT FROM pg_policies WHERE schemaname='payments' AND tablename='query_health' AND policyname='site') THEN
        CREATE POLICY site ON payments.query_health USING (platform.in_scope(site_id, NULL))
            WITH CHECK (platform.in_scope(site_id, NULL));
    END IF;
END $$;
-- +goose StatementEnd
GRANT SELECT, INSERT, UPDATE ON payments.query_health TO axiom_app;

ALTER TABLE payments.discrepancies
    ADD COLUMN IF NOT EXISTS investigated_by text,
    ADD COLUMN IF NOT EXISTS investigated_at timestamptz,
    ADD COLUMN IF NOT EXISTS note text CHECK (char_length(note) BETWEEN 1 AND 1000);
-- +goose StatementBegin
DO $$ BEGIN
    IF NOT EXISTS (SELECT FROM pg_constraint WHERE conrelid='payments.discrepancies'::regclass AND conname='investigation_complete') THEN
        ALTER TABLE payments.discrepancies ADD CONSTRAINT investigation_complete CHECK (
            (investigated_by IS NULL AND investigated_at IS NULL AND note IS NULL)
            OR (state = 'resolved' AND investigated_by IS NOT NULL AND investigated_at IS NOT NULL AND note IS NOT NULL));
    END IF;
END $$;
-- +goose StatementEnd
GRANT UPDATE (state, investigated_by, investigated_at, note) ON payments.discrepancies TO axiom_app;
DROP INDEX CONCURRENTLY IF EXISTS payments.discrepancies_by_id;
CREATE INDEX CONCURRENTLY discrepancies_by_id ON payments.discrepancies (state, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS payments.orders_changed_by_merchant;
CREATE INDEX CONCURRENTLY orders_changed_by_merchant ON payments.orders (merchant_id, updated_at, id);
DROP INDEX CONCURRENTLY IF EXISTS payments.orders_merchant_cursor;
CREATE INDEX CONCURRENTLY orders_merchant_cursor ON payments.orders (merchant_id, id);
