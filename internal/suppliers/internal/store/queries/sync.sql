-- name: AcquireSyncLease :one
-- Takes the lease of a supplier's sync state, making the state on the first take. It takes a lease that was released or
-- has run out, and nothing else: a lease still held returns no row. Every take raises lease_fence, which is what a
-- write must name.
INSERT INTO suppliers.sync_states AS y (supplier_id, next_due, lease_holder, lease_fence, lease_until)
VALUES (@supplier_id, @now, @holder, 1, @lease_until)
ON CONFLICT (supplier_id) DO UPDATE
SET lease_holder = excluded.lease_holder, lease_fence = y.lease_fence + 1, lease_until = excluded.lease_until
WHERE y.lease_until IS NULL OR y.lease_until <= @now
RETURNING lease_fence;

-- name: ReleaseSyncLease :exec
UPDATE suppliers.sync_states SET lease_holder = NULL, lease_until = NULL
WHERE supplier_id = @supplier_id AND lease_fence = @lease_fence;

-- name: GetSyncState :one
SELECT version, format, state FROM suppliers.sync_states WHERE supplier_id = @supplier_id;

-- name: CommitSyncState :execrows
-- Stores a step over the version it read, if the lease is still the one that read it, and keeps the lease for as long
-- as another step may take. No row changed means the step is void.
UPDATE suppliers.sync_states
SET version = @version, format = @format, state = @state, next_due = @next_due, lease_until = @lease_until
WHERE supplier_id = @supplier_id AND version = @expect AND lease_fence = @lease_fence;

-- name: GetSupplierForSync :one
-- The supplier as a round needs it, and the configuration generation a write of its credentials must still find.
SELECT id, kind, origin, mgmt_egress_id, config_gen FROM suppliers.suppliers WHERE id = @id;

-- name: LockSupplierStatus :one
-- The status the engine's state is projected onto, held until the step that projects it commits.
SELECT status, status_reason FROM suppliers.suppliers WHERE id = @id FOR UPDATE;

-- name: SetSupplierStatus :exec
UPDATE suppliers.suppliers SET status = @status, status_reason = sqlc.narg(status_reason) WHERE id = @id;

-- name: SetSupplierBalance :exec
UPDATE suppliers.suppliers SET balance_native = @balance::numeric, balance_observed_at = @observed_at WHERE id = @id;

-- name: ListSyncDue :many
-- The suppliers a round is due for and that no node holds: those never synced, and those whose next_due has come and
-- whose lease is free or has run out. A supplier in any state but connecting, active or low_balance is not synced.
SELECT s.id FROM suppliers.suppliers s
LEFT JOIN suppliers.sync_states y ON y.supplier_id = s.id
WHERE s.status IN ('connecting', 'active', 'low_balance')
    AND (y.supplier_id IS NULL OR (y.next_due <= @now AND (y.lease_until IS NULL OR y.lease_until <= @now)))
ORDER BY y.next_due NULLS FIRST, s.id
LIMIT @max;

-- name: GetSupplierLogin :one
SELECT ciphertext, nonce, wrapped_dek, key_version, version FROM suppliers.supplier_logins WHERE supplier_id = @supplier_id;

-- name: SwapSupplierLogin :execrows
-- Replaces the sealed login over the version that was read, while the lease is still the reader's and the supplier's
-- credentials are still the generation it read them in.
UPDATE suppliers.supplier_logins l
SET ciphertext = @ciphertext, nonce = @nonce, wrapped_dek = @wrapped_dek, key_version = @key_version, version = l.version + 1
FROM suppliers.sync_states y, suppliers.suppliers s
WHERE l.supplier_id = @supplier_id AND l.version = @version
    AND y.supplier_id = l.supplier_id AND y.lease_fence = @lease_fence
    AND s.id = l.supplier_id AND s.config_gen = @config_gen;

-- name: SetRateBucket :exec
UPDATE suppliers.rate_buckets SET window_start = @window_start, used = @used WHERE scope = @scope AND name = @name;

