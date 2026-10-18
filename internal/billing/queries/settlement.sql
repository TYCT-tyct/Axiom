-- name: LockReservation :one
-- Run after the account's balance row is locked: every change to a reservation happens under that lock.
SELECT r.site_id, r.account_id, r.workspace_id, r.key_id, r.workspace_window, r.key_window, r.amount, r.state,
    r.prices, r.charged_seq, r.entry, r.charged, r.price, r.waived, r.estimated, r.final_usage,
    r.fallbacks, r.models, r.attempt_entries, r.endpoints
FROM billing.reservations r
WHERE r.request_id = @request_id;

-- name: ReservationAccount :one
SELECT account_id FROM billing.reservations WHERE request_id = @request_id;

-- name: EndReservation :one
-- Writes a charge's outcome. The charged attempt and entry, and the attempts' entries, stay as they were when none is
-- given. It returns the database's time, which a terminal state's EndRequest records, so every clock the request's end
-- is judged by is the database's.
UPDATE billing.reservations
SET state = @state, charged_seq = coalesce(sqlc.narg(charged_seq), charged_seq), entry = coalesce(sqlc.narg(entry), entry),
    charged = @charged, price = @price, waived = @waived, estimated = @estimated,
    final_usage = @final_usage, attempt_entries = coalesce(sqlc.narg(attempt_entries)::integer[], attempt_entries)
WHERE request_id = @request_id
RETURNING clock_timestamp()::timestamptz AS ended_at;

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
-- user_balance) in the same statement, and is left alone, unlocked by it, when they do not move it.
WITH entry AS (
    INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
    SELECT @transaction_id, @site_id, @account_id, unnest(@books::text[]), unnest(@amounts::bigint[])
)
UPDATE billing.balances b SET balance = b.balance - @user_debit
WHERE b.account_id = @account_id AND @user_debit::bigint <> 0;

-- name: AttemptCost :one
SELECT coalesce(sum(e.amount), 0)::bigint FROM billing.entries e JOIN billing.transactions t ON t.id = e.transaction_id
WHERE t.request_id = @request_id AND t.attempt_seq = @attempt_seq AND e.book = 'supplier_cost';

-- name: InsertCreditBatch :exec
INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, expires_at)
VALUES (@id, @site_id, @account_id, 'gift', @amount, sqlc.narg(expires_at));

-- name: DueReservations :many
-- What cleanup must take over: pending past the deadline or under a reaped holder.
SELECT r.request_id, r.account_id, r.state FROM billing.reservations r JOIN billing.holders h ON h.id = r.holder_id
WHERE r.state = 'pending' AND (r.deadline < clock_timestamp() OR h.reaped_at IS NOT NULL)
ORDER BY r.request_id
LIMIT @max;

-- name: Unbalanced :many
-- Daily reconciliation: transactions whose entries do not sum to zero, and balances that differ from their book. Each
-- side is aggregated once, by a grouped scan, rather than summed again per row; the user-balance book reads from its
-- own partial index.
WITH transactions AS (
    SELECT e.transaction_id FROM billing.entries e GROUP BY e.transaction_id HAVING sum(e.amount) <> 0
), books AS (
    SELECT e.account_id, sum(e.amount) AS amount FROM billing.entries e WHERE e.book = 'user_balance' GROUP BY e.account_id
)
SELECT p.problem::text FROM (
    SELECT 'transaction ' || t.transaction_id::text AS problem FROM transactions t
    UNION ALL
    SELECT 'balance of ' || b.account_id::text FROM billing.balances b LEFT JOIN books k ON k.account_id = b.account_id
    WHERE b.balance <> -coalesce(k.amount, 0)
) p
ORDER BY 1;
