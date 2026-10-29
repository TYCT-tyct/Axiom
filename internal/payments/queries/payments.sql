-- Read after acquiring the order lock: the policy clock must not precede a lock wait.
-- name: PaymentTerms :one
SELECT o.rate_id, o.fee_ppm, o.fee_fixed, clock_timestamp()::timestamptz AS observed_at,
    w.grace_seconds, w.auto_credit_seconds
FROM payments.orders o JOIN payments.order_windows w ON w.id = o.id WHERE o.id = @id;

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
SELECT EXISTS (SELECT 1 FROM payments.order_windows WHERE id = @id AND queryable) AS allowed;

-- name: QueryOrders :many
WITH due AS (
    SELECT id, merchant_id, next_query_at,
        CASE WHEN state = 'pending' AND expires_at > transaction_timestamp() THEN 0
            WHEN state = 'pending' THEN 1 ELSE 2 END AS priority
    FROM payments.order_windows
    WHERE queryable AND (next_query_at <= transaction_timestamp() OR (state = 'pending' AND expires_at <= transaction_timestamp()))
        AND (state = 'pending' OR expires_at >= transaction_timestamp() - interval '30 days')
), ranked AS (
    SELECT *, row_number() OVER (PARTITION BY merchant_id, priority ORDER BY priority, next_query_at, id) AS turn FROM due
)
SELECT id, merchant_id FROM ranked ORDER BY priority, turn, next_query_at, id LIMIT @batch;

-- name: ScheduleQuery :execrows
UPDATE payments.orders AS o SET next_query_at = clock_timestamp() + make_interval(secs => query_delay_seconds),
    query_delay_seconds = least(query_delay_seconds * 2, CASE WHEN expires_at > clock_timestamp() THEN 600 ELSE 3600 END)
WHERE o.id = @id AND (o.next_query_at <= clock_timestamp() OR (o.state = 'pending' AND o.expires_at <= clock_timestamp()))
    AND EXISTS (SELECT 1 FROM payments.order_windows w WHERE w.id = o.id AND w.queryable);
