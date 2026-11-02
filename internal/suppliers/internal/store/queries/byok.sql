-- Account-owned provider keys (docs/domains/accounts.md「自带的供应商 key」). They are ordinary suppliers with the
-- byok status: the code that routes, seals and admits them is the suppliers code. These statements are only the thin
-- account-facing layer: make one, list one's own, switch it off and on, and mark the key a provider refused.

-- name: InsertByokSupplier :one
-- upstream_account is the key's own public id: the table requires it, and it keeps two keys of the same provider apart.
-- The balance and unit value that make the supplier routable are written by the caller, next to this row.
INSERT INTO suppliers.suppliers (owner_scope, site_id, account_id, name, kind, origin, upstream_account, native_unit,
    settlement_currency, mgmt_egress_id, status, balance_native, balance_observed_at, byok_fallback)
VALUES ('account', @site_id, @account_id, @name, 'openai', 'https://api.openai.com', @upstream_account, 'usd', 'USD',
    'direct', 'byok', 1, now(), @byok_fallback)
RETURNING id;

-- name: InsertByokCredential :exec
INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce,
    wrapped_dek, key_version, status, max_concurrency, hint)
VALUES (@id, @supplier_id, @supplier_group_id, @upstream_key_ref, @ciphertext, @nonce, @wrapped_dek, @key_version,
    'active', 8, @hint);

-- name: ListByokKeys :many
SELECT s.id, s.name, s.kind, s.byok_fallback, s.created_at, c.status AS credential_status, c.hint, c.expires_at
FROM suppliers.suppliers s
JOIN LATERAL (
    SELECT status, hint, expires_at FROM suppliers.credentials c WHERE c.supplier_id = s.id ORDER BY c.id LIMIT 1
) c ON true
WHERE s.owner_scope = 'account' AND s.site_id = @site_id AND s.account_id = @account_id AND s.status = 'byok'
ORDER BY s.created_at DESC, s.id DESC;

-- name: CredentialOfByok :one
SELECT c.id FROM suppliers.credentials c
JOIN suppliers.suppliers s ON s.id = c.supplier_id
WHERE s.id = @supplier_id AND s.status = 'byok'
ORDER BY c.id LIMIT 1;

-- name: SetByokCredentialStatus :exec
-- Switching a key off and on, and marking the one the provider refused. Only a BYOK supplier's credential is touched:
-- a normal supplier's credential status belongs to its sync.
UPDATE suppliers.credentials c SET status = @status
FROM suppliers.suppliers s
WHERE c.supplier_id = s.id AND s.status = 'byok' AND s.id = @supplier_id AND c.id = @credential_id;

-- name: SetByokRejected :exec
-- The call path learned the provider refused this key. Addressed by credential, without a tenant: the caller holds an
-- id from the snapshot and has no account at hand.
UPDATE suppliers.credentials c SET status = 'rejected'
FROM suppliers.suppliers s
WHERE c.supplier_id = s.id AND s.status = 'byok' AND c.id = @credential_id AND c.status = 'active';
