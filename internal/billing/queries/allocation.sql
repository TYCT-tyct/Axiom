-- name: AllocationState :one
-- Where the allocation of an account stands; run after the account's balance row is locked.
SELECT allocated_seq, unfunded, unbatched FROM billing.balances WHERE account_id = @account_id;

-- name: UnspentBatches :many
-- The batches that still hold something, each with the sequence number of the credit transaction that opened it.
-- consumed and expired are what the allocation has stored: they trail the ledger until the account is replayed.
SELECT b.id, b.site_id, b.source, b.expires_at, b.amount, b.consumed, b.expired, t.seq
FROM billing.credit_batches b
JOIN billing.transactions t ON t.request_id = b.id AND t.attempt_seq = 0 AND t.kind = 'credit' AND t.revision = 0
WHERE b.account_id = @account_id AND b.consumed + b.expired < b.amount;

-- name: Movements :many
-- What moved the account's balance after the sequence number given, in the order it was posted: one row per
-- transaction, with its net debit on the user's balance (positive takes from the user). A page at a time, so a long
-- history is not held in memory.
SELECT t.seq, t.kind, t.request_id, sum(e.amount)::bigint AS debit
FROM billing.transactions t
JOIN billing.entries e ON e.transaction_id = t.id AND e.book = 'user_balance'
WHERE t.account_id = @account_id AND t.seq > @after
GROUP BY t.id
HAVING sum(e.amount) <> 0
ORDER BY t.seq
LIMIT @max;

-- name: SaveBatches :exec
UPDATE billing.credit_batches b SET consumed = v.consumed, expired = v.expired
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@consumed::bigint[]) AS consumed, unnest(@expired::bigint[]) AS expired) v
WHERE b.id = v.id;

-- name: SaveAllocation :exec
UPDATE billing.balances SET allocated_seq = @allocated_seq, unfunded = @unfunded, unbatched = @unbatched
WHERE account_id = @account_id;

-- name: DueAccounts :many
-- The accounts with a batch whose time has come and which still holds something, after the account given, in order.
SELECT DISTINCT b.account_id FROM billing.credit_batches b
WHERE b.expires_at <= clock_timestamp() AND b.consumed + b.expired < b.amount AND b.account_id > @after
ORDER BY b.account_id
LIMIT @max;
