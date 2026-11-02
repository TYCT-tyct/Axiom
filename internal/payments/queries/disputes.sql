-- name: DisputeOrder :one
-- A chargeback names its PaymentIntent, which the payment recorded as the trade number. Unique per merchant, so this is
-- the order the dispute belongs to; no row means the payment was never credited here.
SELECT order_id, site_id, account_id FROM payments.order_payments
WHERE merchant_id = @merchant_id AND trade_no = @trade_no;

-- name: InsertDispute :execrows
-- A concurrent first event for the same dispute conflicts here; the caller then locks the row the other one inserted.
INSERT INTO payments.order_disputes (id, site_id, account_id, order_id, merchant_id, provider, provider_id,
    payment_intent, amount, currency, reason, status)
VALUES (@id, @site_id, @account_id, @order_id, @merchant_id, @provider, @provider_id,
    @payment_intent, @amount, @currency, @reason, @status)
ON CONFLICT (merchant_id, provider, provider_id) DO NOTHING;

-- name: DisputeByProvider :one
SELECT * FROM payments.order_disputes
WHERE merchant_id = @merchant_id AND provider = @provider AND provider_id = @provider_id
FOR UPDATE;

-- name: LockDispute :one
SELECT * FROM payments.order_disputes WHERE id = @id FOR UPDATE;

-- name: TouchDispute :execrows
UPDATE payments.order_disputes SET status = @status, reason = @reason, updated_at = clock_timestamp()
WHERE id = @id AND status <> 'won' AND (status <> @status OR reason <> @reason);

-- name: ApplyDispute :execrows
UPDATE payments.order_disputes SET state = 'applied', status = @status, reason = @reason, credits = @credits,
    cash = @cash, fee = @fee, chargeback_revision = @revision, applied_at = @applied_at, updated_at = clock_timestamp()
WHERE id = @id AND state = 'recorded';

-- name: ReverseDispute :execrows
UPDATE payments.order_disputes SET state = 'reversed', status = @status, reversed_at = @reversed_at,
    updated_at = clock_timestamp()
WHERE id = @id AND state = 'applied';

-- name: ReconcileDisputes :many
-- The chargebacks whose money moved in the window, in either direction. The checker subtracts cash on the day it was
-- applied and adds it back on the day it was reversed, matching chargeback and chargeback_reverse in the ledger.
SELECT currency, cash, state, applied_at, reversed_at
FROM payments.order_disputes
WHERE merchant_id = @merchant_id
    AND state IN ('applied', 'reversed')
    AND ((applied_at >= @since::timestamptz AND applied_at < @until::timestamptz)
    OR (reversed_at >= @since::timestamptz AND reversed_at < @until::timestamptz));

-- name: OtherDispute :one
-- A payment may be disputed more than once; only the first automatic chargeback is safe. A prior row that took or
-- reversed money makes the next one manual, even if the first was won and its cash came back.
SELECT count(*) FROM payments.order_disputes
WHERE order_id = @order_id AND id <> @id AND state IN ('applied', 'reversed');

-- name: MarkDisputeSuspended :execrows
-- The dispute that suspended the account is marked once, so a repeated event cannot suspend it again after an operator
-- lifts it.
UPDATE payments.order_disputes SET suspended_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = @id AND suspended_at IS NULL;

-- name: DisputesWithoutSuspension :many
-- Applied disputes an older node booked without recording this release's suspension; the poll suspends their accounts
-- until the mixed-version window's rows are drained.
SELECT id FROM payments.order_disputes
WHERE state = 'applied' AND suspended_at IS NULL
ORDER BY applied_at
LIMIT @batch;
