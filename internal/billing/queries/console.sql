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
-- A page of the account's statement (README.md「控制台的读取」): what each of its transactions did to the user's balance,
-- newest first, with the balance it left. Only the user's own balance book is read, so a statement shows the money that
-- moved and nothing of where it went; a transaction that moved none of it (an attempt's cost, a released hold) takes no
-- row. The two regions of the ledger are read together, numbered rows by transactions_by_account_seq and rows from
-- before the sequence by transactions_legacy_by_account, each bounded to a page before its entries are summed, so what a
-- page reads grows with the page and not with the history around it. The balance is read in the same statement as the
-- rows, so a page and the balance its newest row starts from are one snapshot.
WITH page AS (
    (SELECT t.id, t.seq, t.created_at, t.kind, t.request_id
    FROM billing.transactions t
    WHERE t.account_id = @account_id AND t.seq < @after_seq::bigint
        AND EXISTS (SELECT 1 FROM billing.entries e WHERE e.transaction_id = t.id AND e.book = 'user_balance')
    ORDER BY t.seq DESC
    LIMIT @max)
    UNION ALL
    (SELECT t.id, t.seq, t.created_at, t.kind, t.request_id
    FROM billing.transactions t
    WHERE t.account_id = @account_id AND t.seq IS NULL AND (t.created_at, t.id) < (@after_at::timestamptz, @after_id::uuid)
        AND EXISTS (SELECT 1 FROM billing.entries e WHERE e.transaction_id = t.id AND e.book = 'user_balance')
    ORDER BY t.created_at DESC, t.id DESC
    LIMIT @max)
)
SELECT p.id, p.created_at, p.kind, p.request_id, coalesce(p.seq, 0)::bigint AS seq,
    (-sum(e.amount))::bigint AS amount, coalesce(b.source, '')::text AS source,
    (SELECT coalesce(ba.balance, 0)::bigint FROM billing.balances ba WHERE ba.account_id = @account_id) AS balance
FROM page p
JOIN billing.entries e ON e.transaction_id = p.id AND e.book = 'user_balance'
LEFT JOIN billing.credit_batches b ON p.kind = 'credit' AND b.id = p.request_id
GROUP BY p.id, p.seq, p.created_at, p.kind, p.request_id, b.source
ORDER BY p.seq DESC NULLS LAST, p.created_at DESC, p.id DESC;
