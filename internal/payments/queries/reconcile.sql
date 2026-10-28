-- name: InitQueryHealth :exec
INSERT INTO payments.query_health (merchant_id, site_id)
SELECT id, site_id FROM payments.merchants WHERE id = @merchant_id
ON CONFLICT (merchant_id) DO NOTHING;

-- name: AcquireQuery :execrows
UPDATE payments.query_health SET
    first_token = CASE WHEN first_until <= transaction_timestamp() THEN @token::uuid ELSE first_token END,
    first_until = CASE WHEN first_until <= transaction_timestamp() THEN transaction_timestamp() + interval '10 seconds' ELSE first_until END,
    second_token = CASE WHEN first_until > transaction_timestamp() THEN @token::uuid ELSE second_token END,
    second_until = CASE WHEN first_until > transaction_timestamp() THEN transaction_timestamp() + interval '10 seconds' ELSE second_until END
WHERE merchant_id = @merchant_id AND paused_until <= transaction_timestamp()
    AND (first_until <= transaction_timestamp() OR second_until <= transaction_timestamp());

-- name: LockQueryHealth :one
-- The policy clock must follow the state lock, so a lock wait cannot consume the pause.
WITH locked AS MATERIALIZED (
    SELECT failures, paused_until, first_token, second_token
    FROM payments.query_health WHERE merchant_id = @merchant_id FOR UPDATE
)
SELECT locked.*, clock_timestamp()::timestamptz AS observed_at FROM locked;

-- name: FinishQuery :exec
UPDATE payments.query_health SET failures = @failures, paused_until = @paused_until,
    first_token = CASE WHEN first_token = @token::uuid THEN NULL ELSE first_token END,
    first_until = CASE WHEN first_token = @token::uuid THEN '1970-01-01 UTC' ELSE first_until END,
    second_token = CASE WHEN second_token = @token::uuid THEN NULL ELSE second_token END,
    second_until = CASE WHEN second_token = @token::uuid THEN '1970-01-01 UTC' ELSE second_until END
WHERE merchant_id = @merchant_id;

-- name: ReconcileMerchants :many
SELECT id FROM payments.merchants WHERE id > @after ORDER BY id LIMIT 100;

-- name: ReconcileUsage :many
WITH expected AS (
    SELECT (created_at AT TIME ZONE 'UTC')::date AS day, sum(total)::numeric AS used
    FROM payments.orders o WHERE o.merchant_id = @merchant_id AND state NOT IN ('expired','cancelled','failed')
    GROUP BY 1
), actual AS (
    SELECT day, used::numeric FROM payments.merchant_usage u WHERE u.merchant_id = @merchant_id
)
SELECT coalesce(e.day, a.day)::text AS day, coalesce(e.used, 0)::text AS expected, coalesce(a.used, 0)::text AS actual
FROM expected e FULL JOIN actual a USING (day) WHERE coalesce(e.used, 0) <> coalesce(a.used, 0);

-- name: ReconcileOrders :many
SELECT id, site_id, account_id, state, pay_currency, paid_total, paid_fee, credited, coalesce(paid_at, '1970-01-01 UTC'::timestamptz)::timestamptz AS paid_at FROM payments.orders
WHERE merchant_id = @merchant_id AND id > @after ORDER BY id LIMIT 100;

-- name: ReconcileQueries :many
SELECT o.id FROM payments.orders o JOIN payments.order_windows w ON w.id = o.id
WHERE o.merchant_id = @merchant_id AND o.id > @after AND (
    (o.created_at < @until AND o.state IN ('pending','review'))
    OR (o.updated_at >= @since AND o.updated_at < @until)
    OR (o.state IN ('expired','cancelled','failed') AND w.queryable))
ORDER BY o.id LIMIT 100;

-- name: ListDiscrepancies :many
SELECT * FROM payments.discrepancies WHERE (@state::text = '' OR state = @state)
    AND (@before::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR id < @before)
ORDER BY id DESC LIMIT 100;

-- name: GetDiscrepancy :one
SELECT * FROM payments.discrepancies WHERE id = @id;

-- name: InvestigateDiscrepancy :one
UPDATE payments.discrepancies SET state = 'resolved', investigated_by = @actor, investigated_at = transaction_timestamp(), note = @note
WHERE id = @id AND state = 'open' RETURNING *;

-- name: HasPaymentBatchOwner :one
SELECT (EXISTS(SELECT 1 FROM payments.orders o WHERE o.id = @id AND o.merchant_id = @merchant_id)
    OR EXISTS(SELECT 1 FROM payments.discrepancies d WHERE d.id = @id AND d.merchant_id = @merchant_id))::boolean AS owned;
