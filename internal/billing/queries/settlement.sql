-- name: LockReservation :one
-- Run after the account's balance row is locked: every change to a reservation happens under that lock.
SELECT r.site_id, r.account_id, r.workspace_id, r.key_id, r.workspace_window, r.key_window, r.amount, r.state,
    r.prices, r.entry, r.charged, r.price, r.applied_revision,
    coalesce(r.correct_by < clock_timestamp(), false)::boolean AS correction_closed
FROM billing.reservations r
WHERE r.request_id = @request_id;

-- name: ReservationAccount :one
SELECT account_id FROM billing.reservations WHERE request_id = @request_id;

-- name: EndReservation :exec
-- Writes a charge's outcome; hold_seconds, when positive, opens the correction window of a held state.
UPDATE billing.reservations
SET state = @state, entry = coalesce(sqlc.narg(entry), entry), charged = @charged, price = @price,
    correct_by = CASE WHEN @hold_seconds::float8 > 0
        THEN clock_timestamp() + make_interval(0, 0, 0, 0, 0, 0, @hold_seconds::float8) ELSE correct_by END
WHERE request_id = @request_id;

-- name: ApplyRevision :execrows
-- A correction applies once, and never after a newer one (River delivers at least once, in any order).
UPDATE billing.reservations SET applied_revision = @revision
WHERE request_id = @request_id AND applied_revision < @revision;

-- name: MoveCounters :exec
-- Moves a reservation's counters: reserved by reserved, spent by spent, in the windows admission locked.
WITH balance AS (
    UPDATE billing.balances b SET reserved = b.reserved + @reserved WHERE b.account_id = @account_id
)
UPDATE billing.spend s SET reserved = s.reserved + @reserved, spent = s.spent + @spent
WHERE s.subject = @workspace_id AND s.window_start = @workspace_window
    OR s.subject = @key_id AND s.window_start = @key_window;

-- name: InsertTransaction :execrows
-- 0 rows: the event was posted already, and its entries with it.
INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, attempt_seq, revision)
VALUES (@id, @site_id, @account_id, @kind, @request_id, @attempt_seq, @revision)
ON CONFLICT (request_id, attempt_seq, kind, revision) DO NOTHING;

-- name: InsertEntries :exec
-- One statement for a transaction's entries; the user's balance follows its book (user_debit, the entries' sum on
-- user_balance) in the same statement.
WITH entry AS (
    INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
    SELECT @transaction_id, @site_id, @account_id, unnest(@books::text[]), unnest(@amounts::bigint[])
)
UPDATE billing.balances b SET balance = b.balance - @user_debit WHERE b.account_id = @account_id;

-- name: AttemptCost :one
SELECT coalesce(sum(e.amount), 0)::bigint FROM billing.entries e JOIN billing.transactions t ON t.id = e.transaction_id
WHERE t.request_id = @request_id AND t.attempt_seq = @attempt_seq AND e.book = 'supplier_cost';

-- name: AddWaiver :one
-- Adds a waived call's cost to the account's day if the cap allows it, and reports whether it did.
INSERT INTO billing.waivers AS w (account_id, day, site_id, cost)
SELECT @account_id, (clock_timestamp() AT TIME ZONE 'UTC')::date, @site_id, @cost
WHERE @cost::bigint <= @cap::bigint
ON CONFLICT (account_id, day) DO UPDATE SET cost = w.cost + excluded.cost WHERE w.cost + excluded.cost <= @cap::bigint
RETURNING true;

-- name: InsertCreditBatch :exec
INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, paid_price, paid_currency, expires_at)
VALUES (@id, @site_id, @account_id, @source, @amount, nullif(@paid_price::text, ''), nullif(@paid_currency::text, ''),
    sqlc.narg(expires_at));

-- name: DueReservations :many
-- What cleanup must take over: pending past the deadline or under a reaped holder, and held states past correct_by.
SELECT r.request_id, r.account_id, r.state FROM billing.reservations r JOIN billing.holders h ON h.id = r.holder_id
WHERE r.state = 'pending' AND (r.deadline < clock_timestamp() OR h.reaped_at IS NOT NULL)
    OR r.state IN ('estimated', 'cleanup_pending') AND r.correct_by < clock_timestamp()
ORDER BY r.request_id
LIMIT @max;

-- name: Unbalanced :many
-- Daily reconciliation: transactions whose entries do not sum to zero, and balances that differ from their book.
SELECT p.problem::text FROM (
    SELECT 'transaction ' || t.id::text AS problem FROM billing.transactions t
    WHERE (SELECT coalesce(sum(e.amount), 0) FROM billing.entries e WHERE e.transaction_id = t.id) <> 0
    UNION ALL
    SELECT 'balance of ' || b.account_id::text FROM billing.balances b
    WHERE b.balance <> -(SELECT coalesce(sum(e.amount), 0) FROM billing.entries e
        WHERE e.account_id = b.account_id AND e.book = 'user_balance')
) p
ORDER BY 1;
