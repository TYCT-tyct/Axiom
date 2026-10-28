-- What the console reads of an account's money (README.md「控制台的读取」). They run in the account's scope and take no
-- lock: a page may show a figure a moment old.

-- name: ReadBalance :many
SELECT balance, floor, reserved, frozen FROM billing.balances WHERE account_id = @account_id;

-- name: SpentThisMonth :one
-- What the subjects, the account's workspaces, have been charged since the first of the month (UTC, by the database's
-- clock). Every charge is counted on its workspace once, and on its key, which is why keys are not summed too.
SELECT coalesce(sum(spent), 0)::bigint FROM billing.spend
WHERE subject = ANY (@subjects::uuid[]) AND window_start >= date_trunc('month', clock_timestamp(), 'UTC');

-- name: ReadKeysUsed :many
-- What each key has spent and holds reserved in the window of its own reset that holds the database's today, as
-- admission counts it (ReadUsage).
WITH k AS (
    SELECT (@keys::uuid[])[i]::uuid AS key_id, (@resets::text[])[i]::text AS reset
    FROM generate_subscripts(@keys::uuid[], 1) AS i
)
SELECT k.key_id, coalesce(sum(s.spent + s.reserved), 0)::bigint AS used
FROM k
LEFT JOIN billing.spend s ON s.subject = k.key_id
    AND s.window_start >= billing.window_from(k.reset, clock_timestamp())
    AND s.window_start < billing.window_to(k.reset, clock_timestamp())
GROUP BY k.key_id;

-- name: StatementPage :many
-- Every branch is limited using only transaction columns, then the merged page is materialized before any entry
-- aggregation. Putting an entry predicate inside either limit makes deep pages aggregate the whole prefix.
WITH page AS MATERIALIZED (
    SELECT * FROM (
        (SELECT t.id, t.seq, t.created_at, t.kind, t.request_id, t.revision
        FROM billing.transactions t
        WHERE t.account_id = @account_id AND t.seq < @after_seq::bigint
        ORDER BY t.seq DESC
        LIMIT @max)
        UNION ALL
        (SELECT t.id, t.seq, t.created_at, t.kind, t.request_id, t.revision
        FROM billing.transactions t
        WHERE t.account_id = @account_id AND t.seq IS NULL AND (t.created_at, t.id) < (@after_at::timestamptz, @after_id::uuid)
        ORDER BY t.created_at DESC, t.id DESC
        LIMIT @max)
    ) candidates
    ORDER BY seq DESC NULLS LAST, created_at DESC, id DESC
    LIMIT @max
)
SELECT p.id, p.created_at, p.kind, p.request_id, coalesce(p.seq, 0)::bigint AS seq,
    (-coalesce(e.debit, 0))::bigint AS amount, coalesce(b.source, '')::text AS source,
    (CASE WHEN p.kind IN ('freeze', 'unfreeze') THEN
        (p.revision - coalesce((SELECT t.revision FROM billing.transactions t
            WHERE t.account_id = @account_id AND t.request_id = p.request_id AND t.attempt_seq = 0
                AND t.kind = p.kind AND t.revision < p.revision
            ORDER BY t.revision DESC LIMIT 1), 0)) * CASE WHEN p.kind = 'freeze' THEN 1 ELSE -1 END
        WHEN p.kind = 'refund' THEN -coalesce(e.debit, 0) ELSE 0 END)::bigint AS frozen_change,
    (SELECT ba.balance FROM billing.balances ba WHERE ba.account_id = @account_id)::bigint AS balance,
    (SELECT ba.frozen FROM billing.balances ba WHERE ba.account_id = @account_id)::bigint AS frozen
FROM page p
LEFT JOIN LATERAL (SELECT sum(e.amount)::bigint AS debit FROM billing.entries e
    WHERE e.transaction_id = p.id AND e.book = 'user_balance') e ON true
LEFT JOIN billing.credit_batches b ON p.kind = 'credit' AND b.id = p.request_id
ORDER BY p.seq DESC NULLS LAST, p.created_at DESC, p.id DESC;
