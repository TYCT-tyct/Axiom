-- name: PaymentOrder :many
SELECT id FROM payments.orders WHERE id = @id AND merchant_id = @merchant_id;

-- Read after acquiring the order lock: the policy clock must not precede a lock wait.
-- name: PaymentTerms :one
SELECT o.rate_id, o.fee_ppm, o.fee_fixed, clock_timestamp()::timestamptz AS observed_at,
    coalesce(s.grace_seconds, 300)::bigint AS grace_seconds,
    coalesce(s.auto_credit_seconds, 604800)::bigint AS auto_credit_seconds
FROM payments.orders o LEFT JOIN payments.settings s ON s.site_id = o.site_id WHERE o.id = @id;

-- name: RecordPayment :exec
INSERT INTO payments.order_payments (id, order_id, site_id, account_id, merchant_id, trade_no, amount, currency, source)
VALUES (@id, @order_id, @site_id, @account_id, @merchant_id, @trade_no, @amount, @currency, @source)
ON CONFLICT (merchant_id, trade_no) DO NOTHING;

-- name: RecordedPayment :many
SELECT order_id, amount, currency FROM payments.order_payments WHERE merchant_id = @merchant_id AND trade_no = @trade_no;

-- name: PaymentCount :one
SELECT count(*) FROM payments.order_payments WHERE order_id = @order_id;

-- name: RecordDiscrepancy :execrows
INSERT INTO payments.discrepancies (id, site_id, account_id, order_id, merchant_id, kind, dedupe, detail)
SELECT @id::uuid, m.site_id, sqlc.narg(account_id)::uuid, sqlc.narg(order_id)::uuid, m.id, @kind, @dedupe, @detail::jsonb
FROM payments.merchants m WHERE m.id = @merchant_id
ON CONFLICT (merchant_id, kind, dedupe) DO NOTHING;

-- name: CreditOrder :execrows
UPDATE payments.orders SET state = 'paid', paid_total = @paid_total, paid_fee = @paid_fee, credited = @credited,
    paid_at = clock_timestamp(), updated_at = clock_timestamp(), trade_no = @trade_no,
    arrival_rate_id = sqlc.narg(arrival_rate_id), arrival_ref_num = sqlc.narg(arrival_ref_num)::text::numeric,
    arrival_ref_den = sqlc.narg(arrival_ref_den)::text::numeric, arrival_num = sqlc.narg(arrival_num)::text::numeric,
    arrival_den = sqlc.narg(arrival_den)::text::numeric
WHERE id = @id AND state = ANY (@from_states::text[]);

-- A late payment restores the reservation to the creation day, even if that day is no longer today.
-- name: RestoreUsage :execrows
UPDATE payments.merchant_usage u SET used = u.used + o.total FROM payments.orders o
WHERE o.id = @id AND u.merchant_id = o.merchant_id AND u.site_id = o.site_id
    AND u.day = (o.created_at AT TIME ZONE 'UTC')::date;

-- name: TakeVerify :execrows
UPDATE payments.orders SET next_verify_at = clock_timestamp() + interval '1 minute'
WHERE id = @id AND next_verify_at <= clock_timestamp();

-- name: QueryAllowed :one
SELECT EXISTS (SELECT 1 FROM payments.orders o LEFT JOIN payments.settings s ON s.site_id = o.site_id
WHERE o.id = @id AND (o.state = 'pending' OR
    (o.state IN ('expired', 'cancelled') OR (o.state = 'failed' AND o.trade_no <> ''))
    AND o.expires_at + make_interval(secs => coalesce(s.post_expiry_query_seconds, 604800)) >= clock_timestamp())) AS allowed;

-- name: QueryOrders :many
SELECT o.id FROM payments.orders o LEFT JOIN payments.settings s ON s.site_id = o.site_id
WHERE o.next_query_at <= transaction_timestamp() AND
    (o.state = 'pending' OR (o.state IN ('expired', 'cancelled') OR (o.state = 'failed' AND o.trade_no <> ''))
        AND o.expires_at + make_interval(secs => coalesce(s.post_expiry_query_seconds, 604800)) >= transaction_timestamp())
ORDER BY o.next_query_at, o.id LIMIT @batch;

-- name: ScheduleQuery :execrows
UPDATE payments.orders SET next_query_at = clock_timestamp() + make_interval(secs => query_delay_seconds),
    query_delay_seconds = least(query_delay_seconds * 2, CASE WHEN expires_at > clock_timestamp() THEN 600 ELSE 3600 END)
WHERE id = @id AND next_query_at <= clock_timestamp();
