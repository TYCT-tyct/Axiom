-- The order row as the module passes it around; the fraction columns are read as text, which is how a value beyond
-- int64 survives (README.md「数据」).
-- name: ReadOrder :many
SELECT id, site_id, account_id, user_id, merchant_id, method, state, fingerprint, credits, site_currency, pay_currency,
    total, fee, trade_no, checkout, expires_at, created_at, updated_at
FROM payments.orders WHERE id = @id;

-- An idempotency key as the account used it: the order it made, and the fingerprint of the request that made it.
-- name: OrderByKey :many
SELECT id, fingerprint FROM payments.orders WHERE account_id = @account_id AND idem_key = @idem_key;

-- The order's row, locked for a change of its state: the serialization point of everything that writes an order.
-- name: LockOrder :many
SELECT id, site_id, account_id, user_id, merchant_id, method, state, credits, site_currency, pay_currency, total, fee,
    trade_no, expires_at, created_at
FROM payments.orders WHERE id = @id FOR UPDATE;

-- name: InsertOrder :one
INSERT INTO payments.orders (id, site_id, account_id, user_id, merchant_id, method, state, idem_key, fingerprint,
    credits, site_currency, pay_currency, total, fee, fee_ppm, fee_fixed, rate_id, ref_num, ref_den, applied_num,
    applied_den, expires_at, client_ip)
VALUES (@id, @site_id, @account_id, @user_id, @merchant_id, @method, 'pending', @idem_key, @fingerprint, @credits,
    @site_currency, @pay_currency, @total, @fee, @fee_ppm, @fee_fixed, sqlc.narg(rate_id),
    @ref_num::numeric, @ref_den::numeric, @applied_num::numeric, @applied_den::numeric,
    transaction_timestamp() + make_interval(secs => @ttl_seconds::integer), @client_ip::inet)
RETURNING created_at, expires_at, updated_at;

-- The conditional transition: a row changes only from a state the transition table allows, and reports whether it did.
-- name: MoveOrder :execrows
UPDATE payments.orders SET state = @state, updated_at = clock_timestamp(),
    trade_no = CASE WHEN @trade_no::text <> '' THEN @trade_no ELSE trade_no END
WHERE id = @id AND state = ANY (@from_states::text[]);

-- name: SaveCheckout :many
UPDATE payments.orders SET trade_no = @trade_no, checkout = @checkout::jsonb, updated_at = clock_timestamp()
WHERE id = @id AND state = 'pending'
RETURNING updated_at;

-- name: OrderEvent :exec
INSERT INTO payments.order_events (id, order_id, site_id, account_id, source, kind, from_state, to_state, trade_no, amount, raw)
VALUES (@id, @order_id, @site_id, @account_id, @source, @kind, @from_state, @to_state, @trade_no, sqlc.narg(amount)::bigint, @raw);

-- What the account's order limits count: its open orders, the cancellations that still bar a new order, the credits it
-- asked to buy today, and whether it ever paid.
-- name: OrderCounts :one
SELECT count(*) FILTER (WHERE state = 'pending') AS pending,
    count(*) FILTER (WHERE state = 'cancelled'
        AND updated_at > transaction_timestamp() - make_interval(secs => @cancel_window::bigint)) AS cancelled,
    coalesce(sum(credits) FILTER (WHERE state NOT IN ('cancelled', 'expired', 'failed')
        AND created_at >= date_trunc('day', transaction_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0)::bigint AS daily,
    count(*) FILTER (WHERE state IN ('paid', 'partially_refunded', 'refunded')) AS paid
FROM payments.orders WHERE account_id = @account_id;

-- The account's limits are checked under its own transaction lock, so two orders at once cannot both see the last
-- place free.
-- name: LockAccountOrders :exec
SELECT pg_advisory_xact_lock(@key::bigint);

-- Configuration saves exclude new orders until their revision and in-flight checks commit.
-- name: LockMerchantIdentity :exec
SELECT pg_advisory_xact_lock(@key::bigint);

-- name: HoldMerchantIdentity :exec
SELECT pg_advisory_xact_lock_shared(@key::bigint);

-- name: MerchantOfferCurrent :one
SELECT EXISTS (SELECT 1 FROM payments.merchants WHERE id = @id AND rev = @rev AND enabled) AS current;

-- The reservation belongs to the day the order was created, even when it ends after UTC midnight.
-- name: ReleaseUsage :execrows
UPDATE payments.merchant_usage u SET used = u.used - o.total
FROM payments.orders o
WHERE o.id = @id AND u.merchant_id = o.merchant_id AND u.site_id = o.site_id
    AND u.day = (o.created_at AT TIME ZONE 'UTC')::date;

-- A merchant account with an order still in flight keeps its identity: changing its keys or gateway would leave that
-- order unable to verify or query (docs/domains/payments.md「后台配置」).
-- name: OpenMerchantOrders :one
SELECT EXISTS (
    SELECT 1 FROM payments.orders WHERE merchant_id = @merchant_id AND state IN ('pending', 'review')
) AS open;

-- The orders a run of the expiry job takes: the oldest first, so one that fails to close cannot hold the queue.
-- name: ExpiringOrders :many
SELECT id, merchant_id, trade_no FROM payments.orders
WHERE state = 'pending' AND expires_at <= transaction_timestamp()
ORDER BY expires_at, id LIMIT @batch;
