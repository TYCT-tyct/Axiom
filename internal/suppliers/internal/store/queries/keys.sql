-- The credentials as the admin's upstream pages manage them (internal/suppliers/README.md「管理接口」): listed across
-- suppliers, pasted, checked, switched and erased. The sealed key is read only where a check sends it.

-- name: CredentialsPage :many
-- Newest first: an ID is a UUIDv7. Only the platform's own suppliers: an account's key is its own (BYOK). The mode
-- filter is the supplier's own mode (relay, direct, cloud).
SELECT c.id, c.supplier_id, c.supplier_group_id, c.upstream_key_ref, c.status, c.cooldown_until, c.expires_at,
    c.max_concurrency, c.requests_per_minute, c.hint, c.source, c.checked_at, c.check_outcome, c.check_cause, c.check_status, c.region
FROM suppliers.credentials c
JOIN suppliers.suppliers s ON s.id = c.supplier_id
WHERE s.owner_scope = 'platform'
    AND (sqlc.narg(supplier_id)::uuid IS NULL OR c.supplier_id = sqlc.narg(supplier_id))
    AND (sqlc.narg(status)::text IS NULL OR c.status = sqlc.narg(status))
    AND (sqlc.narg(mode)::text IS NULL OR s.mode = sqlc.narg(mode))
    AND (sqlc.narg(after)::uuid IS NULL OR c.id < sqlc.narg(after))
ORDER BY c.id DESC
LIMIT @page_size::int;

-- name: LockCredentialOf :many
-- One credential of one supplier, held for a change of its status until the transaction ends: a check's answer and a
-- person's switch must not cross.
SELECT c.id, c.supplier_id, c.supplier_group_id, c.upstream_key_ref, c.status, c.cooldown_until, c.expires_at,
    c.max_concurrency, c.requests_per_minute, c.hint, c.source, c.checked_at, c.check_outcome, c.check_cause, c.check_status, c.region
FROM suppliers.credentials c WHERE c.supplier_id = @supplier_id AND c.id = @id FOR UPDATE;

-- name: SetCredentialStatusOf :exec
UPDATE suppliers.credentials SET status = @status WHERE supplier_id = @supplier_id AND id = @id;

-- name: RecordCredentialCheck :exec
-- What a check found, and the status it leaves: the caller decided it under the row's lock.
UPDATE suppliers.credentials
SET status = @status, checked_at = @checked_at, check_outcome = @outcome, check_cause = sqlc.narg(cause), check_status = @upstream_status
WHERE supplier_id = @supplier_id AND id = @id;

-- name: EraseCredentialOf :exec
-- The sealed key goes and the row stays, because attempts made with it point at it.
UPDATE suppliers.credentials SET ciphertext = NULL, nonce = NULL, wrapped_dek = NULL, key_version = NULL, status = 'erased'
WHERE supplier_id = @supplier_id AND id = @id;

-- name: RejectedCredentials :many
-- The named credentials of platform suppliers that are refused now. Read without a lock: the caller takes each
-- supplier's row and then the credential's, the order every change of a supplier takes them in, and reads it again.
SELECT c.id, c.supplier_id FROM suppliers.credentials c
JOIN suppliers.suppliers s ON s.id = c.supplier_id
WHERE c.id = ANY(@ids::uuid[]) AND c.status = 'rejected' AND s.owner_scope = 'platform'
ORDER BY c.id;

-- name: CredentialsToTest :many
-- The credentials a check of every key reaches: one that holds a key, of a platform supplier Axiom is logged in to (in use
-- or paused), of the supplier given or of all, in ID order, one more than the limit so the caller knows when there are
-- too many. A disabled one is counted apart.
SELECT c.id, c.supplier_id FROM suppliers.credentials c
JOIN suppliers.suppliers s ON s.id = c.supplier_id
WHERE s.owner_scope = 'platform' AND s.status IN ('active', 'low_balance', 'paused')
    AND c.status NOT IN ('binding', 'disabled', 'erased')
    AND (sqlc.narg(supplier_id)::uuid IS NULL OR c.supplier_id = sqlc.narg(supplier_id))
ORDER BY c.id
LIMIT @max_count::int;

-- name: CountDisabledCredentials :one
SELECT count(*)::bigint FROM suppliers.credentials c
JOIN suppliers.suppliers s ON s.id = c.supplier_id
WHERE s.owner_scope = 'platform' AND s.status IN ('active', 'low_balance', 'paused') AND c.status = 'disabled'
    AND (sqlc.narg(supplier_id)::uuid IS NULL OR c.supplier_id = sqlc.narg(supplier_id));

-- name: CredentialByRef :many
SELECT id FROM suppliers.credentials WHERE supplier_id = @supplier_id AND upstream_key_ref = @upstream_key_ref;

-- name: InsertSealedCredential :exec
-- A key a person gave or Axiom made at their asking, sealed to this row: active, with what its check found and what the
-- supplier's listing lets it call (allowed_models NULL is every model).
INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce, wrapped_dek,
    key_version, status, max_concurrency, requests_per_minute, hint, source, checked_at, check_outcome, check_status,
    allowed_models, expires_at)
VALUES (@id, @supplier_id, @supplier_group_id, @upstream_key_ref, @ciphertext, @nonce, @wrapped_dek, @key_version,
    'active', @max_concurrency, sqlc.narg(requests_per_minute), sqlc.narg(hint), @source, sqlc.narg(checked_at),
    sqlc.narg(check_outcome), sqlc.narg(check_status), sqlc.narg(allowed_models), sqlc.narg(expires_at));

-- name: GroupHasCredential :one
-- Whether the group has a credential that holds a key or waits for one.
SELECT EXISTS (SELECT FROM suppliers.credentials WHERE supplier_group_id = @group_id AND status <> 'erased')::boolean;

-- name: SetCredentialRejected :exec
-- The call path learned the supplier refused this key (401). Addressed by credential: the caller holds an id from the
-- snapshot. Only an active key: a person's switch and an erasure stand.
UPDATE suppliers.credentials SET status = 'rejected' WHERE id = @credential_id AND status = 'active';
