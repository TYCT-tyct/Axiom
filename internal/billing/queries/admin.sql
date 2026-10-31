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
    coalesce(sum(charged), 0)::bigint AS revenue,
    coalesce(sum(charged - profit) FILTER (WHERE profit IS NOT NULL), 0)::bigint AS cost,
    count(*) FILTER (WHERE profit IS NULL)::bigint AS unknown_cost_calls
FROM billing.reservations
WHERE site_id = @site_id AND created_at >= @since AND created_at < @until
    AND state IN ('settled', 'cleanup_settled');

-- name: PlatformCash :many
-- The cash that moved for one site in a window, by payment currency: paid credits in, refunds out. The kinds keep the
-- scan to those few transactions; a NULL currency is the site's accounting currency.
SELECT coalesce(e.currency, '')::text AS currency, sum(e.amount)::bigint AS amount
FROM billing.transactions t
-- OFFSET 0 keeps this a loop over the window's few cash transactions: without it the planner may hash away the
-- subquery and read every entry, which is the call volume.
CROSS JOIN LATERAL (
    SELECT e.currency, e.amount FROM billing.entries e
    WHERE e.transaction_id = t.id AND e.book = 'cash'
    OFFSET 0
) e
WHERE t.site_id = @site_id AND t.created_at >= @since AND t.created_at < @until
    AND t.kind IN ('credit', 'refund')
GROUP BY e.currency
ORDER BY e.currency NULLS FIRST;
