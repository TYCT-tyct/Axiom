-- +goose Up

-- Removing a supplier erases its secrets at once (internal/suppliers/README.md「移除」): the login row is deleted and the
-- sealed value of each of its credentials is cleared, with the credential kept as 'erased', because attempts that were
-- made with it point at the row (attempt_costs). This is the one exception to "the application deletes nothing" in
-- 00001: a sealed login is a secret nothing may keep once the supplier is gone, and no row refers to it.
--
-- A change a binary that does not know it survives: the older binary only reads the new status, and routes by
-- status = 'active'.
ALTER TABLE suppliers.credentials DROP CONSTRAINT credentials_status_check;
ALTER TABLE suppliers.credentials ADD CONSTRAINT credentials_status_check CHECK (status IN
    ('binding', 'active', 'upstream_disabled', 'quota_exhausted', 'expired', 'disabled', 'erased'));
ALTER TABLE suppliers.credentials DROP CONSTRAINT sealed_unless_binding;
ALTER TABLE suppliers.credentials ADD CONSTRAINT sealed_unless_binding
    CHECK (status IN ('binding', 'erased') OR ciphertext IS NOT NULL);
-- An erased credential holds nothing, and nothing but an erased one is allowed to hold nothing after it was sealed.
ALTER TABLE suppliers.credentials ADD CONSTRAINT erased_is_empty CHECK (status <> 'erased' OR ciphertext IS NULL);

GRANT DELETE ON suppliers.supplier_logins TO axiom_app;
