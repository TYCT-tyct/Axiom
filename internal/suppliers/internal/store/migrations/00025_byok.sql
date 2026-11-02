-- +goose Up

-- Account-owned provider keys (BYOK, docs/domains/accounts.md「自带的供应商 key」). They are suppliers so that the
-- envelope encryption, row-level security, capacity, failure classification and routing snapshot are the existing ones,
-- and they carry their own status so that a binary that does not know them neither routes to them nor tries to sync
-- them: every reader of suppliers filters by status, and no old query names 'byok'.
ALTER TABLE suppliers.suppliers DROP CONSTRAINT suppliers_status_check;
ALTER TABLE suppliers.suppliers ADD CONSTRAINT suppliers_status_check CHECK (status IN
    ('connecting', 'active', 'low_balance', 'needs_attention', 'paused', 'removing', 'removed', 'byok'));

-- When the account made the key, for the console list. Existing rows get the migration's time, which is only ever read
-- for BYOK rows, all of which are newer than this migration.
ALTER TABLE suppliers.suppliers ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
-- Whether the key may fall back to Axiom's endpoints for a model it covers. OpenRouter's shared-capacity switch.
ALTER TABLE suppliers.suppliers ADD COLUMN byok_fallback boolean NOT NULL DEFAULT true;

-- The last four characters, for the console to tell two keys of the same provider apart. Not a secret and never the
-- whole key; NULL for credentials a sync bound, whose upstream id is shown instead.
ALTER TABLE suppliers.credentials ADD COLUMN hint text CHECK (char_length(hint) BETWEEN 4 AND 8);

-- A key the provider refused authentication for. Persistent so that a restart or another node does not offer it again;
-- only a successful test moves it back to 'active'. An old binary filters credentials by status = 'active' and so
-- already skips it.
ALTER TABLE suppliers.credentials DROP CONSTRAINT credentials_status_check;
ALTER TABLE suppliers.credentials ADD CONSTRAINT credentials_status_check CHECK (status IN
    ('binding', 'active', 'upstream_disabled', 'quota_exhausted', 'expired', 'disabled', 'erased', 'rejected'));

-- +goose Down

-- A rollback must not meet rows it cannot describe: delete the BYOK suppliers (and with them their credentials and
-- endpoints) first, or leave the schema as it is.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM suppliers.suppliers WHERE status = 'byok') THEN
        RAISE EXCEPTION 'cannot roll back BYOK: suppliers with status byok exist; delete them first';
    END IF;
    IF EXISTS (SELECT FROM suppliers.credentials WHERE status = 'rejected') THEN
        RAISE EXCEPTION 'cannot roll back BYOK: credentials with status rejected exist; remove them first';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE suppliers.credentials DROP CONSTRAINT credentials_status_check;
ALTER TABLE suppliers.credentials ADD CONSTRAINT credentials_status_check CHECK (status IN
    ('binding', 'active', 'upstream_disabled', 'quota_exhausted', 'expired', 'disabled', 'erased'));
ALTER TABLE suppliers.credentials DROP COLUMN hint;
ALTER TABLE suppliers.suppliers DROP COLUMN byok_fallback;
ALTER TABLE suppliers.suppliers DROP COLUMN created_at;
ALTER TABLE suppliers.suppliers DROP CONSTRAINT suppliers_status_check;
ALTER TABLE suppliers.suppliers ADD CONSTRAINT suppliers_status_check CHECK (status IN
    ('connecting', 'active', 'low_balance', 'needs_attention', 'paused', 'removing', 'removed'));
