-- name: InsertSupplier :one
INSERT INTO suppliers.suppliers (owner_scope, site_id, account_id, name, kind, origin, upstream_account, native_unit,
    settlement_currency, mgmt_egress_id)
VALUES (@owner_scope, @site_id, @account_id, @name, @kind, @origin, @upstream_account, @native_unit,
    @settlement_currency, @mgmt_egress_id)
RETURNING id;

-- name: InsertSupplierLogin :exec
INSERT INTO suppliers.supplier_logins (supplier_id, kind, ciphertext, nonce, wrapped_dek, key_version, expires_at)
VALUES (@supplier_id, @kind, @ciphertext, @nonce, @wrapped_dek, @key_version, @expires_at);

-- name: InsertGroup :one
INSERT INTO suppliers.supplier_groups (supplier_id, upstream_ref, name, ratio)
VALUES (@supplier_id, @upstream_ref, @name, @ratio)
RETURNING id;

-- name: InsertCredential :exec
-- A key Axiom made or adopted by its marker name, with what the supplier lets it call (allowed_models NULL is every
-- model); the sync's complete listing projects the policy again in the same commit.
INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce,
    wrapped_dek, key_version, status, max_concurrency, source, allowed_models, expires_at)
VALUES (@id, @supplier_id, @supplier_group_id, @upstream_key_ref, @ciphertext, @nonce, @wrapped_dek, @key_version,
    @status, @max_concurrency, 'made', sqlc.narg(allowed_models), sqlc.narg(expires_at));

-- name: GetCredentialSecret :one
SELECT ciphertext, nonce, wrapped_dek, key_version FROM suppliers.credentials WHERE id = @id;

-- name: InsertEndpoint :one
INSERT INTO suppliers.endpoints (supplier_id, supplier_group_id, model_id, upstream_name, formats)
VALUES (@supplier_id, @supplier_group_id, @model_id, @upstream_name, @formats)
RETURNING id;

-- name: ListLiveSuppliers :many
SELECT id, name, kind FROM suppliers.suppliers WHERE status <> 'removed' ORDER BY name, id;
