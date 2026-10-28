-- name: MerchantCash :many
SELECT coalesce(e.currency,b.currency)::text AS currency, sum(e.amount)::text AS amount
FROM billing.credit_batches b JOIN billing.transactions t ON t.request_id = b.id
JOIN billing.entries e ON e.transaction_id = t.id
WHERE b.merchant_id = @merchant_id AND e.book = 'cash' AND (CASE WHEN t.kind = 'credit' THEN b.created_at ELSE t.created_at END) >= @since::timestamptz
    AND (CASE WHEN t.kind = 'credit' THEN b.created_at ELSE t.created_at END) < @until::timestamptz
GROUP BY coalesce(e.currency,b.currency);

-- name: MerchantBatches :many
SELECT id FROM billing.credit_batches WHERE merchant_id = @merchant_id AND id > @after ORDER BY id LIMIT 100;
