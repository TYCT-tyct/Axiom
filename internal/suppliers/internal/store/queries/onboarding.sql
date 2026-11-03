-- name: GroupsToKey :many
-- The groups the sync makes a key for: present, eligible for a key of ours, still one Axiom may use (selected, or never
-- chosen at all), with no credential that holds a secret or waits for one, and not in the cooldown after a failure.
SELECT id, upstream_ref
FROM suppliers.supplier_groups g
WHERE g.supplier_id = @supplier_id
  AND g.present
  AND g.key_eligible
  AND (g.selected OR g.codename IS NULL)
  AND (g.key_retry_at IS NULL OR g.key_retry_at <= @now)
  AND NOT EXISTS (
      SELECT 1 FROM suppliers.credentials c
      WHERE c.supplier_group_id = g.id AND c.status <> 'erased'
  )
ORDER BY g.upstream_ref;

-- name: GroupIDsOf :many
SELECT id, upstream_ref FROM suppliers.supplier_groups WHERE supplier_id = @supplier_id;

-- name: GroupsToBind :many
-- A credential a person bound by reference only: the sync reads the key's text and activates it.
SELECT c.id, c.upstream_key_ref, g.upstream_ref
FROM suppliers.credentials c
JOIN suppliers.supplier_groups g ON g.id = c.supplier_group_id
WHERE c.supplier_id = @supplier_id AND c.status = 'binding'
  AND (g.key_retry_at IS NULL OR g.key_retry_at <= @now)
ORDER BY c.id;

-- name: ErasedCredentialID :one
-- A key made again after its credential was erased (the listing had lost it, then found it): revive the row instead of
-- inserting a second one for the same upstream key.
SELECT id FROM suppliers.credentials
WHERE supplier_id = @supplier_id AND upstream_key_ref = @upstream_key_ref AND status = 'erased';

-- name: ReviveCredential :execrows
UPDATE suppliers.credentials
SET ciphertext = @ciphertext, nonce = @nonce, wrapped_dek = @wrapped_dek, key_version = @key_version,
    status = @status, cooldown_until = NULL, expires_at = NULL, last_seen_at = now()
WHERE id = @id AND supplier_id = @supplier_id AND status = 'erased';

-- name: ActivateCredential :exec
-- Turns a binding into a credential with a secret. The status guard leaves one a person removed alone.
UPDATE suppliers.credentials
SET status = 'active', ciphertext = @ciphertext, nonce = @nonce, wrapped_dek = @wrapped_dek, key_version = @key_version,
    last_seen_at = now()
WHERE id = @id AND supplier_id = @supplier_id AND status = 'binding';

-- name: SetGroupKeyError :exec
UPDATE suppliers.supplier_groups
SET key_error = @key_error, key_retry_at = @retry_at
WHERE supplier_id = @supplier_id AND upstream_ref = @upstream_ref;

-- name: ClearGroupKeyError :exec
UPDATE suppliers.supplier_groups
SET key_error = NULL, key_retry_at = NULL
WHERE supplier_id = @supplier_id AND upstream_ref = @upstream_ref;

-- name: ClearKeyErrors :exec
UPDATE suppliers.supplier_groups
SET key_error = NULL, key_retry_at = NULL
WHERE supplier_id = @supplier_id AND key_error IS NOT NULL;

-- name: EraseCredentialsByRef :exec
-- A key that has not been in a complete listing for a while: its text cannot open anything, so it goes. The wait keeps
-- a listing that is briefly behind from deleting a key just made. A binding is left for the sync to finish or fail, and
-- an erased row is left as it is.
UPDATE suppliers.credentials
SET status = 'erased', ciphertext = NULL, nonce = NULL, wrapped_dek = NULL, key_version = NULL
WHERE supplier_id = @supplier_id AND upstream_key_ref = ANY(@refs::text[])
  AND status NOT IN ('binding', 'erased') AND last_seen_at < @stale_before;
