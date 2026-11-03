-- +goose Up

-- Onboarding by the sync (docs/domains/suppliers.md「接入流程」): whether a key of ours can serve a group, and why the
-- last attempt to make one did not work. The key itself is a credential, as for one a person bound. Old nodes neither
-- read nor write these columns; the defaults make a group they project not look like a target.
ALTER TABLE suppliers.credentials
    ADD COLUMN last_seen_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE suppliers.supplier_groups
    ADD COLUMN key_eligible boolean NOT NULL DEFAULT false,
    ADD COLUMN key_error text CHECK (key_error IN ('not_permitted', 'group_unavailable', 'refused')),
    ADD COLUMN key_retry_at timestamptz,
    ADD CONSTRAINT supplier_groups_key_error_complete CHECK ((key_error IS NULL) = (key_retry_at IS NULL));

-- The projection writes all three; the admin API only reads them.
GRANT UPDATE (key_eligible, key_error, key_retry_at) ON suppliers.supplier_groups TO axiom_app;
GRANT UPDATE (last_seen_at) ON suppliers.credentials TO axiom_app;

-- +goose Down

ALTER TABLE suppliers.supplier_groups DROP CONSTRAINT supplier_groups_key_error_complete;
ALTER TABLE suppliers.supplier_groups DROP COLUMN key_retry_at, DROP COLUMN key_error, DROP COLUMN key_eligible;
ALTER TABLE suppliers.credentials DROP COLUMN last_seen_at;
