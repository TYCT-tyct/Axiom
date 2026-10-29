-- name: ReadRateLimit :many
SELECT t.site_id, '00000000-0000-0000-0000-000000000000'::uuid AS account_id, t.rate_requests AS requests, coalesce(t.rate_requests, -1)::integer AS effective
FROM accounts.tiers t WHERE @target::text = 'tier' AND t.id = @id
UNION ALL
SELECT a.site_id, a.id, a.rate_requests, coalesce(a.rate_requests, t.rate_requests, -1)::integer
FROM accounts.accounts a JOIN accounts.tiers t ON t.site_id = a.site_id AND t.id = a.tier_id
WHERE @target::text = 'account' AND a.id = @id
UNION ALL
SELECT k.site_id, k.account_id, k.rate_requests, coalesce(k.rate_requests, a.rate_requests, t.rate_requests, -1)::integer
FROM accounts.api_keys k JOIN accounts.accounts a ON a.site_id = k.site_id AND a.id = k.account_id
JOIN accounts.tiers t ON t.site_id = a.site_id AND t.id = a.tier_id
WHERE @target::text = 'key' AND k.id = @id AND k.kind = 'call' AND k.deleted_at IS NULL;

-- name: LockRateTier :exec
SELECT id FROM accounts.tiers WHERE id = @id FOR UPDATE;

-- name: LockRateAccount :exec
SELECT id FROM accounts.accounts WHERE id = @id FOR UPDATE;

-- name: LockRateKey :exec
SELECT id FROM accounts.api_keys WHERE id = @id FOR UPDATE;

-- name: WriteRateLimit :exec
WITH t AS (
    UPDATE accounts.tiers rt SET rate_requests = sqlc.narg(requests)::integer WHERE @target::text = 'tier' AND rt.id = @id
), a AS (
    UPDATE accounts.accounts ra SET rate_requests = sqlc.narg(requests)::integer WHERE @target::text = 'account' AND ra.id = @id
)
UPDATE accounts.api_keys rk SET rate_requests = sqlc.narg(requests)::integer WHERE @target::text = 'key' AND rk.id = @id;
