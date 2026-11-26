-- A cloud platform's model service reached with its credential (docs/domains/suppliers.md「云平台」).

-- name: InsertCloudSupplier :one
-- As a direct API: no balance to read, costs in the settlement currency itself (native unit usd, unit value 1), in
-- service at once. What is not secret about the credential is cloud_account.
INSERT INTO suppliers.suppliers (owner_scope, name, kind, origin, upstream_account, native_unit, settlement_currency,
    mgmt_egress_id, status, mode, cloud_account)
VALUES ('platform', @name, @kind, @origin, @upstream_account, 'usd', @settlement_currency, 'direct', 'active', 'cloud',
    @cloud_account)
RETURNING id;

-- name: InsertCloudCredential :exec
-- A region that passed its check, at its first check's answer.
INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce, wrapped_dek,
    key_version, status, max_concurrency, requests_per_minute, allowed_models, call_names, hint, source, region,
    checked_at, check_outcome, check_status)
VALUES (@id, @supplier_id, @supplier_group_id, @upstream_key_ref, @ciphertext, @nonce, @wrapped_dek, @key_version,
    'active', @max_concurrency, sqlc.narg(requests_per_minute), sqlc.narg(allowed_models), sqlc.narg(call_names), @hint,
    'pasted', sqlc.narg(region), @checked_at, 'passed', @check_status);

-- name: LockCloudCredentials :many
-- A cloud supplier's credentials, erased ones too, held until the replacement commits.
SELECT id, upstream_key_ref, region, status, max_concurrency, requests_per_minute FROM suppliers.credentials
WHERE supplier_id = @supplier_id ORDER BY id FOR UPDATE;

-- name: ResealCloudCredential :exec
-- A region of a replacement that passed with the credential this row was made for: sealed again, as the check found it.
UPDATE suppliers.credentials
SET ciphertext = @ciphertext, nonce = @nonce, wrapped_dek = @wrapped_dek, key_version = @key_version, status = 'active',
    allowed_models = sqlc.narg(allowed_models), call_names = sqlc.narg(call_names), checked_at = @checked_at,
    check_outcome = 'passed', check_cause = NULL, check_status = @check_status
WHERE supplier_id = @supplier_id AND id = @id;

-- name: SetCloudAccount :exec
UPDATE suppliers.suppliers SET cloud_account = @cloud_account WHERE id = @id AND mode = 'cloud';

-- name: StoredRefsOutside :many
-- The credentials of other suppliers than this one that hold one of refs now: a region of a credential any of them
-- holds is a duplicate. The zero ID is no supplier, so every supplier's count.
SELECT DISTINCT upstream_key_ref FROM suppliers.credentials
WHERE upstream_key_ref = ANY(@refs::text[]) AND status <> 'erased' AND supplier_id <> @supplier_id
ORDER BY upstream_key_ref;

-- name: LineEndpoints :many
-- The models the line has an endpoint for already, and the name each endpoint is stored by.
SELECT model_id, upstream_name FROM suppliers.endpoints WHERE supplier_group_id = @supplier_group_id ORDER BY model_id;

-- name: ServedNames :many
-- The names the supplier's credential serving now was stored with and how its calls name them: what a replacement
-- keeps for a platform whose models a person named. A platform without regions has one such row.
SELECT allowed_models, call_names FROM suppliers.credentials
WHERE supplier_id = @supplier_id AND status <> 'erased' ORDER BY id DESC LIMIT 1;
