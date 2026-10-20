-- name: RequestLedger :many
-- A request's ledger transactions with their entries, oldest first: the ledger half of a call's chain (README.md
-- 「结算与账本」「按请求读账本」). Every transaction of a call carries the call's request ID, and the unique key
-- (request_id, attempt_seq, kind, revision) makes that filter a range scan. Entries are aggregated so one transaction
-- is one row; a transaction that moved nothing has none.
SELECT t.id::text AS id, t.kind, t.attempt_seq, t.revision, t.created_at,
    coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object('book', e.book, 'amount', e.amount, 'currency', e.currency))
        ORDER BY e.currency NULLS FIRST, e.book, e.amount)
        FILTER (WHERE e.transaction_id IS NOT NULL), '[]'::jsonb)::text AS entries
FROM billing.transactions t
LEFT JOIN billing.entries e ON e.transaction_id = t.id
WHERE t.request_id = @request_id
GROUP BY t.id, t.kind, t.attempt_seq, t.revision, t.created_at
ORDER BY t.created_at, t.id;
