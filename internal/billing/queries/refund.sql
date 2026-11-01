-- name: AddFrozen :exec
-- The money writer owns the balance row lock; replay only writes the batch cache.
UPDATE billing.balances SET frozen = frozen + @delta::bigint WHERE account_id = @account_id;

-- name: RefundHistory :one
-- Read under the balance lock. The ledger is authoritative for cash and fees; the first successful refund fixes policy.
-- Chargebacks return cash the same way a refund does and a reversal takes it back, so all three count in what has
-- already been returned.
SELECT coalesce(b.refund_with_fee, @with_fee::boolean)::boolean AS with_fee,
    coalesce(h.returned, 0)::bigint AS returned, coalesce(h.fee, 0)::bigint AS fee
FROM billing.credit_batches b
LEFT JOIN LATERAL (
    SELECT -sum(e.amount) FILTER (WHERE e.book = 'cash') AS returned,
        sum(e.amount) FILTER (WHERE e.book = 'fee_income') AS fee
    FROM billing.transactions t JOIN billing.entries e ON e.transaction_id = t.id
    WHERE t.request_id = b.id AND t.attempt_seq = 0
        AND t.kind IN ('refund', 'chargeback', 'chargeback_reverse')
) h ON true
WHERE b.id = @batch;

-- name: NextChargeBackRevision :one
-- Under the balance lock, so two chargebacks of one batch cannot choose the same revision. A reversal keeps the
-- revision of the chargeback it undoes; this counts only chargebacks, so a later dispute gets the next one.
SELECT (coalesce(max(revision), 0) + 1)::bigint AS revision
FROM billing.transactions
WHERE request_id = @batch AND attempt_seq = 0 AND kind = 'chargeback';

-- name: SetRefundPolicy :exec
WITH policy AS (SELECT @with_fee::boolean AS with_fee)
UPDATE billing.credit_batches b SET refund_with_fee = p.with_fee FROM policy p
WHERE b.id = @batch AND b.refund_with_fee IS NULL;

-- name: RefundRecordedAt :one
SELECT created_at FROM billing.transactions WHERE request_id = @batch AND attempt_seq = 0 AND kind = 'refund'
ORDER BY revision DESC LIMIT 1;
