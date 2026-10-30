-- name: PaymentAdministrator :one
SELECT EXISTS (SELECT 1 WHERE (SELECT platform.in_platform_scope()) OR @site_id::uuid = ANY ((SELECT platform.allowed_sites())::uuid[])) AS allowed;

-- name: ReadRefund :many
SELECT * FROM payments.refunds WHERE id = @id;

-- name: ListRefunds :many
-- A page's orders' refunds, oldest first, with the currency each returns cash in. One statement for the page instead of
-- one per order; the account scope already limits it to the account's own rows.
SELECT sqlc.embed(r), o.pay_currency FROM payments.refunds r JOIN payments.orders o ON o.id = r.order_id
WHERE r.order_id = ANY (@order_ids::uuid[])
ORDER BY r.created_at, r.id;

-- name: RefundByKey :many
SELECT * FROM payments.refunds WHERE order_id = @order_id AND idem_key = @idem_key;

-- name: LockRefund :one
SELECT * FROM payments.refunds WHERE id = @id AND order_id = @order_id FOR UPDATE;

-- name: RefundTerms :one
SELECT o.credited, o.refund_with_fee, s.refund_with_fee AS site_with_fee, m.refund_enabled, m.user_refund,
    coalesce((SELECT sum(r.credits) FROM payments.refunds r WHERE r.order_id = o.id AND r.state <> 'failed'), 0)::bigint AS used,
    EXISTS (SELECT FROM payments.refunds r WHERE r.order_id = o.id AND r.state IN ('frozen','sending','pending')) AS inflight
FROM payments.orders o JOIN payments.merchants m ON m.id = o.merchant_id
LEFT JOIN payments.settings s ON s.site_id = o.site_id WHERE o.id = @id;

-- name: InsertRefund :one
INSERT INTO payments.refunds(id, order_id, site_id, account_id, state, via, credits, requested_credits, amount,
    with_fee, refund_key, reason, requested_by, idem_key)
VALUES (@id, @order_id, @site_id, @account_id, 'frozen', @via, @credits, @requested_credits, @amount,
    @with_fee, @refund_key, @reason, @requested_by, @idem_key) RETURNING *;

-- name: ApproveRefund :exec
UPDATE payments.refunds SET approved_by = @approved_by WHERE id = @id AND approved_by IS NULL;

-- The future due time is also a lease: the five-second call finishes before another worker can claim this attempt.
-- name: ClaimRefund :one
UPDATE payments.refunds SET state = 'sending', sent_at = coalesce(sent_at, clock_timestamp()), attempts = attempts + 1,
    next_query_at = clock_timestamp() + interval '1 minute'
WHERE id = @id RETURNING *;

-- name: FinishRefund :exec
UPDATE payments.refunds SET state = @state, provider_ref = @provider_ref,
    next_query_at = clock_timestamp() + interval '1 minute',
    finished_at = CASE WHEN @state::text IN ('succeeded','failed') THEN @finished_at::timestamptz ELSE NULL END
WHERE id = @id;

-- name: RefundOrderPolicy :exec
UPDATE payments.orders SET refund_with_fee = @with_fee WHERE id = @id;

-- name: SuccessfulRefundCredits :one
SELECT coalesce(sum(credits), 0)::bigint AS credits FROM payments.refunds WHERE order_id = @order_id AND state = 'succeeded';

-- name: DueRefunds :many
SELECT id FROM payments.refunds WHERE via = 'channel' AND NOT manual_only AND state IN ('frozen','sending','pending') AND approved_by IS NOT NULL
    AND next_query_at <= clock_timestamp() ORDER BY next_query_at, id LIMIT 100;

-- name: ReadResolution :many
SELECT * FROM payments.resolutions WHERE order_id = @order_id AND idem_key = @idem_key;

-- name: InsertResolution :exec
INSERT INTO payments.resolutions(id, order_id, site_id, account_id, refund_id, outcome, trade_no, amount,
    evidence, reason, requested_by, idem_key)
VALUES (@id, @order_id, @site_id, @account_id, sqlc.narg(refund_id), @outcome, @trade_no, @amount,
    @evidence, @reason, @requested_by, @idem_key);

-- name: ApproveResolution :exec
UPDATE payments.resolutions SET approved_by = @approved_by WHERE id = @id;

-- name: SuccessfulRefundCash :many
SELECT o.pay_currency AS currency, sum(r.amount)::text AS amount FROM payments.refunds r
JOIN payments.orders o ON o.id = r.order_id
WHERE o.merchant_id = @merchant_id AND r.state = 'succeeded' AND r.finished_at >= @since::timestamptz AND r.finished_at < @until::timestamptz
GROUP BY o.pay_currency;

-- name: RefundClock :one
SELECT clock_timestamp()::timestamptz AS now;

-- name: ParkRefund :exec
UPDATE payments.refunds SET manual_only = true WHERE id = @id;
