-- name: MerchantCash :many
WITH cash AS (
    SELECT coalesce(e.currency,b.currency)::text AS currency, e.amount
    FROM billing.credit_batches b JOIN billing.transactions t ON t.request_id = b.id AND t.kind = 'credit'
    JOIN billing.entries e ON e.transaction_id = t.id AND e.book = 'cash'
    WHERE b.merchant_id = @merchant_id AND b.created_at >= @since::timestamptz AND b.created_at < @until::timestamptz
    UNION ALL
    SELECT coalesce(e.currency,b.currency)::text AS currency, e.amount
    FROM billing.transactions t JOIN billing.credit_batches b ON b.id = t.request_id
    JOIN billing.entries e ON e.transaction_id = t.id AND e.book = 'cash'
    WHERE t.kind IN ('refund', 'chargeback', 'chargeback_reverse')
        AND t.created_at >= @since AND t.created_at < @until AND b.merchant_id = @merchant_id
)
SELECT currency, sum(amount)::text AS amount FROM cash GROUP BY currency;

-- name: MerchantBatches :many
SELECT b.id FROM billing.credit_batches b WHERE b.merchant_id = @merchant_id
    AND b.created_at >= @since::timestamptz AND b.created_at < @until::timestamptz
    AND (b.created_at,b.id) > (coalesce((SELECT c.created_at FROM billing.credit_batches c WHERE c.id = @after),@since),@after::uuid)
ORDER BY b.created_at,b.id LIMIT 100;

-- name: ReadCreditBatches :many
SELECT id, site_id, account_id, source, amount, expires_at, created_at, coalesce(currency, '')::text AS currency, paid, fee,
    merchant_id, coalesce(offline_channel, '')::text AS offline_channel, rate_id
FROM billing.credit_batches WHERE id = ANY(@ids::uuid[]);
