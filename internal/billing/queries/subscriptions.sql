-- Subscription plans, the account's subscription and its windows (docs/domains/billing.md「订阅套餐」,
-- internal/billing/README.md「订阅套餐」). Every write of a window's counters runs under the account's balance row lock,
-- the one serialisation point of the account's money.

-- name: InsertPlan :exec
INSERT INTO billing.subscription_plans (id, site_id, name, currency, price, quota, window_seconds, term_seconds)
VALUES (@id, @site_id, @name, @currency, @price, @quota, @window_seconds, @term_seconds);

-- name: ReadPlan :one
SELECT id, site_id, name, currency, price, quota, window_seconds, term_seconds, active, rev, created_at, updated_at
FROM billing.subscription_plans WHERE id = @id;

-- name: ListPlans :many
SELECT id, site_id, name, currency, price, quota, window_seconds, term_seconds, active, rev, created_at, updated_at
FROM billing.subscription_plans
WHERE site_id = @site_id AND (sqlc.arg(include_inactive)::boolean OR active)
ORDER BY name, id;

-- name: UpdatePlan :execrows
-- The revision moves only when it is the one the administrator read, so two saves cannot silently overwrite each other.
UPDATE billing.subscription_plans
SET name = @name, price = @price, quota = @quota, window_seconds = @window_seconds, term_seconds = @term_seconds,
    active = @active, rev = rev + 1, updated_at = clock_timestamp()
WHERE id = @id AND rev = @rev;

-- name: ActiveSubscription :one
SELECT id, site_id, account_id, plan_id, plan_name, starts_at, ends_at, current_seq, state, closed_at
FROM billing.subscriptions
WHERE account_id = @account_id AND state = 'active';

-- name: Subscription :one
SELECT id, site_id, account_id, plan_id, plan_name, starts_at, ends_at, current_seq, state, closed_at
FROM billing.subscriptions WHERE id = @id;

-- name: InsertSubscription :exec
INSERT INTO billing.subscriptions (id, site_id, account_id, plan_id, plan_name, starts_at, ends_at, current_seq)
VALUES (@id, @site_id, @account_id, @plan_id, @plan_name, @starts_at, @ends_at, 1);

-- name: UpdateSubscriptionTerm :execrows
UPDATE billing.subscriptions SET ends_at = @ends_at, current_seq = @current_seq WHERE id = @id;

-- name: ExpireSubscription :execrows
-- The term ended: no window of it may fund a new reservation. closed_at stays empty until the remaining liability is
-- written off, which waits for the subscription's pending holds to end.
UPDATE billing.subscriptions SET state = 'expired' WHERE id = @id AND state = 'active';

-- name: CloseSubscription :execrows
UPDATE billing.subscriptions SET closed_at = clock_timestamp()
WHERE id = @id AND state = 'expired' AND closed_at IS NULL;

-- name: SetCurrentWindow :execrows
UPDATE billing.subscriptions SET current_seq = @seq WHERE id = @id AND state = 'active';

-- name: BatchClock :one
-- The database's clock and whether a plan is in force, read after the balance lock so a grant that committed while
-- admission waited is visible. The lock statement's own snapshot predates that wait.
SELECT clock_timestamp()::timestamptz AS now,
    EXISTS (SELECT 1 FROM billing.subscriptions WHERE account_id = @account_id AND state = 'active') AS has_plan;

-- name: AvailablePlan :one
-- The account's subscription in force with its current window, as admission reads it under the balance row lock.
SELECT s.id AS subscription_id, s.site_id, s.account_id, s.ends_at AS term_ends, w.seq, w.starts_at AS window_starts,
    w.ends_at AS window_ends, w.quota, w.used, w.reserved
FROM billing.subscriptions s
JOIN billing.subscription_windows w ON w.subscription_id = s.id AND w.seq = s.current_seq
WHERE s.account_id = @account_id AND s.state = 'active' AND w.voided_at IS NULL;

-- name: InsertWindow :exec
-- One slice of the term: window seq starts at start and ends at end, clipped by the block's end so the last window may
-- be shorter than the plan's. The grant writes them all in one pipeline, with quota copied from the plan it sold.
INSERT INTO billing.subscription_windows (subscription_id, seq, site_id, account_id, starts_at, ends_at, quota)
VALUES (@subscription_id, @seq, @site_id, @account_id, @starts_at, @ends_at, @quota);

-- name: NextWindow :one
-- The window after seq that still funds, so a refunded tail is never entered.
SELECT subscription_id, seq, site_id, account_id, starts_at, ends_at, quota, used, reserved
FROM billing.subscription_windows
WHERE subscription_id = @subscription_id AND seq > @seq AND voided_at IS NULL
ORDER BY seq LIMIT 1;

-- name: Window :one
SELECT subscription_id, seq, site_id, account_id, starts_at, ends_at, quota, used, reserved
FROM billing.subscription_windows WHERE subscription_id = @subscription_id AND seq = @seq;

-- name: Windows :many
SELECT subscription_id, seq, site_id, account_id, starts_at, ends_at, quota, used, reserved
FROM billing.subscription_windows WHERE subscription_id = @subscription_id ORDER BY seq;

-- name: LastWindowSeq :one
SELECT coalesce(max(seq), 0)::integer AS seq FROM billing.subscription_windows WHERE subscription_id = @subscription_id;

-- Moves the frozen window's reserved amount, refusing to cross the window's quota: 0 rows means the caller's
-- accounting is wrong, not that the request must wait.
-- name: AddWindowReserved :execrows
UPDATE billing.subscription_windows SET reserved = reserved + @delta
WHERE subscription_id = @subscription_id AND seq = @seq
    AND reserved + @delta >= 0 AND used + reserved + @delta <= quota;

-- Settles a charge into the frozen window: the hold goes back and the charge is used. A charge above the hold is
-- refused by the check, as the caller caps at the hold.
-- name: SettleWindow :execrows
UPDATE billing.subscription_windows SET reserved = reserved - @held, used = used + @charged
WHERE subscription_id = @subscription_id AND seq = @seq AND @held >= 0 AND @charged >= 0;

-- name: ChargeWindowUsed :execrows
-- A correction moves what a window has used by delta, within its quota: the room is re-read under the balance row lock,
-- so corrections of the same window cannot together charge past it.
UPDATE billing.subscription_windows SET used = used + @delta
WHERE subscription_id = @subscription_id AND seq = @seq AND used + @delta >= 0 AND used + @delta <= quota;

-- name: SetReservationsWallet :exec
-- A plan reservation cleanup released settles late from the wallet: the window's hold is already back and its credit
-- may have lapsed, so the row must record the source that actually paid. Corrections then follow the wallet, not a
-- window that never paid (docs/domains/billing.md「订阅套餐」).
UPDATE billing.reservations SET funding = 'wallet', subscription_id = NULL, window_seq = NULL
WHERE request_id = ANY(@request_ids::uuid[]) AND funding <> 'wallet';

-- name: InsertGrant :execrows
INSERT INTO billing.subscription_grants (id, site_id, account_id, subscription_id, plan_id, plan_name, source, quota,
    window_seconds, term_seconds, windows, first_window, face, currency, paid, fee, merchant_id, offline_channel, rate_id)
VALUES (@id, @site_id, @account_id, @subscription_id, @plan_id, @plan_name, @source, @quota, @window_seconds,
    @term_seconds, @windows, @first_window, @face, @currency, @paid, @fee, @merchant_id, @offline_channel, @rate_id)
ON CONFLICT (id) DO NOTHING;

-- name: ReadGrant :one
SELECT id, site_id, account_id, subscription_id, plan_id, plan_name, source, quota, window_seconds, term_seconds,
    windows, first_window, face, currency, paid, fee, merchant_id, offline_channel, rate_id,
    refunded_value, refunded_paid, refunded_fee, refunded_windows, created_at
FROM billing.subscription_grants WHERE id = @id;

-- name: Liability :one
-- What a subscription still owes: the sum of its plan_liability entries, which is negative while credit remains
-- (a credit to the liability is negative). The grant and write-off reference the subscription as their request id; a call settlement and its
-- correction reference the call, so they join back through the reservation that froze the subscription.
SELECT coalesce(sum(e.amount), 0)::bigint AS credited
FROM billing.entries e
JOIN billing.transactions t ON t.id = e.transaction_id
LEFT JOIN billing.reservations r ON r.request_id = t.request_id
WHERE e.book = 'plan_liability'
    AND (t.request_id = @subscription_id OR r.subscription_id = @subscription_id);

-- name: PendingPlanHolds :one
SELECT coalesce(sum(amount), 0)::bigint AS held
FROM billing.reservations WHERE subscription_id = @subscription_id AND state = 'pending';

-- name: DueSubscriptions :many
-- Subscriptions a run must look at, oldest first: an active one whose term or current window ended (to pass the window
-- and expire the term), and an expired one not yet closed. An expired subscription still holding a pending plan
-- reservation is left out rather than returned and skipped, so a page full of such rows cannot starve the ones behind
-- them; the reservation's end makes it due again on the following run.
SELECT s.id, s.site_id, s.account_id, s.ends_at
FROM billing.subscriptions s
JOIN billing.subscription_windows w ON w.subscription_id = s.id AND w.seq = s.current_seq
WHERE (s.state = 'active' AND (s.ends_at <= clock_timestamp() OR w.ends_at <= clock_timestamp()))
    OR (s.state = 'expired' AND s.closed_at IS NULL AND NOT EXISTS (
        SELECT 1 FROM billing.reservations r WHERE r.subscription_id = s.id AND r.state = 'pending'))
ORDER BY s.ends_at, s.id
LIMIT @max;

-- name: SubscriptionsOfAccount :many
SELECT s.id, s.site_id, s.account_id, s.plan_id, s.plan_name, s.starts_at, s.ends_at, s.current_seq, s.state,
    s.closed_at, s.created_at,
    w.seq AS window_seq, w.starts_at AS window_start, w.ends_at AS window_end, w.quota, w.used, w.reserved
FROM billing.subscriptions s
JOIN billing.subscription_windows w ON w.subscription_id = s.id AND w.seq = s.current_seq
WHERE s.account_id = @account_id
ORDER BY s.created_at DESC, s.id
LIMIT @max;

-- name: ReadSubscription :one
SELECT id, site_id, account_id, plan_id, plan_name, starts_at, ends_at, current_seq, state, closed_at, created_at
FROM billing.subscriptions WHERE id = @id;

-- name: RefundWindows :many
-- The last windows of a grant's block, still in force, locked for the refund. The caller asks for at most the block's
-- remaining windows and checks that none holds a reservation.
SELECT subscription_id, seq, site_id, account_id, starts_at, ends_at, quota, used, reserved
FROM billing.subscription_windows
WHERE subscription_id = @subscription_id AND seq BETWEEN @first AND @last AND voided_at IS NULL
ORDER BY seq DESC
LIMIT @max
FOR UPDATE;

-- name: VoidWindow :execrows
UPDATE billing.subscription_windows SET voided_at = clock_timestamp()
WHERE subscription_id = @subscription_id AND seq = @seq AND voided_at IS NULL;

-- name: RefundGrant :execrows
-- The grant remembers what has been refunded, so a later partial refund takes back the rest and never more than was
-- paid. The amounts are cumulative.
UPDATE billing.subscription_grants
SET refunded_value = refunded_value + @credits, refunded_paid = refunded_paid + @paid,
    refunded_fee = refunded_fee + @fee, refunded_windows = refunded_windows + @windows
WHERE id = @id;

-- name: ShortenSubscription :execrows
-- A partial refund takes back only whole future windows, so the term ends at the last one that remains.
UPDATE billing.subscriptions SET ends_at = @ends_at
WHERE id = @id AND state = 'active' AND @ends_at > starts_at;

-- name: RevokeSubscription :execrows
-- Nothing remains, so the subscription ends now. closed_at is set because the refund has already taken back the unused
-- liability; the expiry job must not write it off a second time.
UPDATE billing.subscriptions SET state = 'expired', ends_at = @ends_at, current_seq = @current_seq,
    closed_at = clock_timestamp()
WHERE id = @id AND state = 'active' AND @ends_at > starts_at;

-- name: InsertSubscriptionRefund :execrows
INSERT INTO billing.subscription_refunds (id, grant_id, site_id, account_id, subscription_id, credits, paid, fee,
    currency, windows, unused, revoked, actor, reason)
VALUES (@id, @grant_id, @site_id, @account_id, @subscription_id, @credits, @paid, @fee, @currency, @windows, @unused,
    @revoked, @actor, @reason)
ON CONFLICT (id) DO NOTHING;

-- name: ReadSubscriptionRefund :one
SELECT id, grant_id, site_id, account_id, subscription_id, credits, paid, fee, currency, windows, unused, revoked,
    actor, reason, created_at
FROM billing.subscription_refunds WHERE id = @id;

-- name: LastLiveWindow :one
-- The highest window a refund did not void; 0 means the subscription has nothing left and is revoked.
SELECT coalesce(max(seq), 0)::integer AS seq
FROM billing.subscription_windows WHERE subscription_id = @subscription_id AND voided_at IS NULL;
