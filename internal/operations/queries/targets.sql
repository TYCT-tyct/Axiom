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
