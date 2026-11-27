-- The platform's egress proxies (docs/domains/suppliers.md「出口代理」). The table is small and read whole: a node's
-- Reload and a check both pick a credential's exit from every row, as one rule.

-- name: ListEgressProxies :many
SELECT id, name, scheme, host, port, username, ciphertext, nonce, wrapped_dek, key_version, fallback_id, status
FROM suppliers.egress_proxies ORDER BY id;

-- name: LockEgressProxies :exec
-- The admin's changes to the proxies and to which credential uses one take turns, so that a check of the fallback
-- chain or of a proxy's users holds until the change commits.
SELECT pg_advisory_xact_lock(hashtext('suppliers.egress_proxies'));

-- name: InsertEgressProxy :exec
INSERT INTO suppliers.egress_proxies (id, name, scheme, host, port, username, ciphertext, nonce, wrapped_dek, key_version,
    fallback_id, status)
VALUES (@id, @name, @scheme, @host, @port::bigint, sqlc.narg(username), sqlc.narg(ciphertext), sqlc.narg(nonce),
    sqlc.narg(wrapped_dek), sqlc.narg(key_version), sqlc.narg(fallback_id), @status);

-- name: UpdateEgressProxy :exec
UPDATE suppliers.egress_proxies SET name = @name, username = sqlc.narg(username), ciphertext = sqlc.narg(ciphertext),
    nonce = sqlc.narg(nonce), wrapped_dek = sqlc.narg(wrapped_dek), key_version = sqlc.narg(key_version),
    fallback_id = sqlc.narg(fallback_id), status = @status
WHERE id = @id;

-- name: EgressProxyUsers :one
-- What keeps a proxy from being removed: the credentials that are not erased and use it, and the proxies it is the
-- fallback of.
SELECT (SELECT count(*) FROM suppliers.credentials c WHERE c.egress_id = @id::text AND c.status <> 'erased')::int AS credentials,
    (SELECT count(*) FROM suppliers.egress_proxies p WHERE p.fallback_id = @id::text)::int AS fallbacks;

-- name: ReleaseErasedEgress :exec
-- An erased credential never calls again, so it lets go of the proxy rather than keeping it from being removed.
UPDATE suppliers.credentials SET egress_id = NULL WHERE egress_id = @id::text AND status = 'erased';

-- name: DeleteEgressProxy :exec
DELETE FROM suppliers.egress_proxies WHERE id = @id;

-- name: SetCredentialEgress :exec
UPDATE suppliers.credentials SET egress_id = sqlc.narg(egress_id) WHERE supplier_id = @supplier_id AND id = @id;
