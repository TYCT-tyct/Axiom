-- name: ReadTargets :many
-- The site's targets for the months in [from_month, to_month]; the caller includes the month a window ends in even
-- when the window ends exactly at its first instant, and the arithmetic then gives that month a zero share.
SELECT month, metric, amount::text AS amount, currency
FROM operations.targets
WHERE site_id = @site_id
  AND month >= @from_month
  AND month <= @to_month
ORDER BY month, metric;

-- name: UpsertTarget :exec
INSERT INTO operations.targets (site_id, metric, month, amount, currency)
VALUES (@site_id, @metric, @month, @amount, @currency)
ON CONFLICT (site_id, metric, month) DO UPDATE
SET amount = EXCLUDED.amount, currency = EXCLUDED.currency, updated_at = now();

-- name: DeleteTarget :exec
DELETE FROM operations.targets
WHERE site_id = @site_id AND metric = @metric AND month = @month;

-- name: LockTargets :exec
-- Serializes the writers of one site's month: the read of the old values, the writes and the audit entry then see one
-- order, instead of two administrators overwriting each other's values and auditing a "before" that never was.
SELECT pg_advisory_xact_lock(hashtextextended(@key, 0));

-- name: ReadTargetRevision :one
-- The month's revision, 0 before the first write. The editor sends this value back and a set compares it under the
-- lock, so a form that read before another change cannot restore the old set.
SELECT coalesce((SELECT revision FROM operations.target_revisions WHERE site_id = @site_id AND month = @month), 0)::bigint AS revision;

-- name: UpsertTargetRevision :exec
INSERT INTO operations.target_revisions (site_id, month, revision)
VALUES (@site_id, @month, @revision)
ON CONFLICT (site_id, month) DO UPDATE
SET revision = EXCLUDED.revision, updated_at = now();

-- name: ReadCommercialTargets :many
-- The site's commercial targets for the months in [from_month, to_month]. The table is separate so a node from before
-- these metrics keeps reading operations.targets without meeting one it does not know.
SELECT month, metric, amount::text AS amount, currency
FROM operations.commercial_targets
WHERE site_id = @site_id
  AND month >= @from_month
  AND month <= @to_month
ORDER BY month, metric;

-- name: UpsertCommercialTarget :exec
INSERT INTO operations.commercial_targets (site_id, metric, month, amount, currency)
VALUES (@site_id, @metric, @month, @amount, @currency)
ON CONFLICT (site_id, metric, month) DO UPDATE
SET amount = EXCLUDED.amount, currency = EXCLUDED.currency, updated_at = now();

-- name: DeleteCommercialTarget :exec
DELETE FROM operations.commercial_targets
WHERE site_id = @site_id AND metric = @metric AND month = @month;
