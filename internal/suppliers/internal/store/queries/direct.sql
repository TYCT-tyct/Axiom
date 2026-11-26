-- A provider's own API reached with pasted keys (docs/domains/suppliers.md「直连接口」).

-- name: InsertDirectSupplier :one
-- A direct API has no balance to read: the column stays empty, and routing goes by its mode instead. Its costs are in
-- the settlement currency itself, so its native unit is money (usd) and its unit value 1. It is in service at once:
-- every key it has passed a check, and there is nothing left to read.
INSERT INTO suppliers.suppliers (owner_scope, name, kind, origin, upstream_account, native_unit, settlement_currency,
    mgmt_egress_id, status, mode, api_format)
VALUES ('platform', @name, @kind, @origin, @upstream_account, 'usd', @settlement_currency, 'direct', 'active', 'direct',
    sqlc.narg(api_format))
RETURNING id;

-- name: InsertDirectCredential :exec
INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce, wrapped_dek,
    key_version, status, max_concurrency, requests_per_minute, allowed_models, call_names, hint)
VALUES (@id, @supplier_id, @supplier_group_id, @upstream_key_ref, @ciphertext, @nonce, @wrapped_dek, @key_version,
    'active', @max_concurrency, sqlc.narg(requests_per_minute), @allowed_models, sqlc.narg(call_names), @hint);

-- name: StoredKeyRefs :many
-- The pasted keys already stored as a credential that is not erased, by the reference a pasted key is stored under.
SELECT DISTINCT upstream_key_ref FROM suppliers.credentials
WHERE upstream_key_ref = ANY(@refs::text[]) AND status <> 'erased'
ORDER BY upstream_key_ref;
