-- What the admin API reads of the ledger.

-- name: ReadCreditBatch :many
-- A gift's currency and offline channel read as empty.
SELECT id, site_id, account_id, source, amount, expires_at, created_at, coalesce(currency, '')::text AS currency, paid, fee,
    merchant_id, coalesce(offline_channel, '')::text AS offline_channel, rate_id
FROM billing.credit_batches WHERE id = @id;

-- name: PlatformCallMoney :one
-- One site's calls admitted in a window: what they were charged and, where it is known, what the supplier cost. The
-- amount is the reservation's, so a correction restates the call's own window rather than adding a second event; the
-- time axis is the call's, the same as the call log's (README.md「运营看板」).
SELECT
    coalesce(sum(charged::numeric), 0)::text AS revenue,
    coalesce(sum(charged::numeric - profit::numeric) FILTER (WHERE profit IS NOT NULL), 0)::text AS cost,
    count(*) FILTER (WHERE profit IS NULL)::bigint AS unknown_cost_calls
FROM billing.reservations
WHERE site_id = @site_id AND created_at >= @since AND created_at < @until
    AND state IN ('settled', 'cleanup_settled');

-- name: PlatformCash :many
-- The cash that moved for one site in a window, by payment currency: paid credits in, refunds and chargebacks out,
-- chargeback reversals in. The kinds keep the scan to those few transactions; a NULL currency is the site's accounting
-- currency.
SELECT coalesce(e.currency, '')::text AS currency, coalesce(sum(e.amount::numeric), 0)::text AS amount
FROM billing.transactions t
-- OFFSET 0 keeps this a loop over the window's few cash transactions: without it the planner may hash away the
-- subquery and read every entry, which is the call volume.
CROSS JOIN LATERAL (
    SELECT e.currency, e.amount FROM billing.entries e
    WHERE e.transaction_id = t.id AND e.book = 'cash'
    OFFSET 0
) e
WHERE t.site_id = @site_id AND t.created_at >= @since AND t.created_at < @until
    AND t.kind IN ('credit', 'refund', 'chargeback', 'chargeback_reverse')
GROUP BY e.currency
ORDER BY e.currency NULLS FIRST;


-- name: ListCashMovements :many
-- The cash behind the dashboard figure, by transaction time, with the batch that says whether it came through an order
-- or an offline channel. Refunds and chargebacks keep their own time, so one of an older order is in this window.
SELECT t.id, t.created_at, t.account_id, t.request_id AS batch_id, t.kind,
    coalesce(e.currency, b.currency, '')::text AS currency, e.amount::text AS amount,
    coalesce(b.offline_channel, '')::text AS offline_channel, (b.merchant_id IS NOT NULL)::boolean AS online
FROM billing.transactions t
CROSS JOIN LATERAL (
    SELECT e.currency, e.amount FROM billing.entries e
    WHERE e.transaction_id = t.id AND e.book = 'cash'
    OFFSET 0
) e
JOIN billing.credit_batches b ON b.id = t.request_id
WHERE t.site_id = @site_id AND t.created_at >= @since AND t.created_at < @until
    AND t.kind IN ('credit', 'refund', 'chargeback', 'chargeback_reverse')
    AND (sqlc.narg(account_id)::uuid IS NULL OR t.account_id = sqlc.narg(account_id))
    AND (t.created_at, t.id) < (@after_at::timestamptz, @after_id::uuid)
ORDER BY t.created_at DESC, t.id DESC
LIMIT @max;
