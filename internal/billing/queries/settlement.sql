-- name: LockReservation :one
-- Run after the account's balance row is locked: every change to a reservation happens under that lock.
SELECT r.site_id, r.account_id, r.workspace_id, r.key_id, r.workspace_window, r.key_window, r.amount, r.state,
    r.prices, r.price_versions, r.charged_seq, r.entry, r.charged, r.price, r.waived, r.estimated, r.final_usage,
    r.fallbacks, r.models, r.attempt_entries, r.endpoints, r.profit,
    coalesce(r.pricing_tiers, -2)::smallint AS pricing_tiers
FROM billing.reservations r
WHERE r.request_id = @request_id;

-- name: LockReservations :many
-- The batch form of LockReservation, run after the account's balance row is locked. The rows come back in no order;
-- the caller keeps its items' order by request id.
SELECT r.request_id, r.site_id, r.account_id, r.workspace_id, r.key_id, r.workspace_window, r.key_window, r.amount, r.state,
    r.prices, r.price_versions, r.charged_seq, r.entry, r.charged, r.price, r.waived, r.estimated, r.final_usage,
    r.fallbacks, r.models, r.attempt_entries, r.endpoints, r.profit,
    coalesce(r.pricing_tiers, -2)::smallint AS pricing_tiers
FROM billing.reservations r
WHERE r.request_id = ANY(@request_ids::uuid[]);

-- name: PostLedgerTransactions :execrows
-- The first of the batch's three ledger statements: every event, in the batch's order so seq follows it. A duplicate
-- event is an error here, not a silent skip: the reservation state says it cannot happen, and the caller retries the
-- batch one item at a time if it does.
INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, attempt_seq, revision)
SELECT (@ids::uuid[])[i], @site_id, @account_id, (@kinds::text[])[i], (@request_ids::uuid[])[i],
    (@attempt_seqs::integer[])[i], (@revisions::bigint[])[i]
FROM generate_subscripts(@ids::uuid[], 1) AS i
ORDER BY i;

-- name: PostLedgerEntries :execrows
-- The second: every entry of those events, each against the transaction id its event already inserted.
INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount, currency)
SELECT (@transaction_ids::uuid[])[i], @site_id, @account_id, (@books::text[])[i], (@amounts::bigint[])[i],
    nullif((@currencies::text[])[i], '')
FROM generate_subscripts(@transaction_ids::uuid[], 1) AS i
ORDER BY i;

-- name: SetBalance :execrows
-- The third: set the balance to the exact value the batch's user-balance entries leave. The caller sums in platform.Exact,
-- so a total past int64 does not clamp; a final value past int64 is an error and rolls the batch back.
UPDATE billing.balances SET balance = @balance WHERE account_id = @account_id;

-- name: ReservationAccount :one
SELECT account_id FROM billing.reservations WHERE request_id = @request_id;

-- name: EndReservation :one
-- Moves the reservation's three counters and writes its outcome together. The charged attempt and entry, and the
-- attempts' entries, stay as they were when none is given. It returns the database's time, which a terminal state's EndRequest records, so every clock the request's end
-- is judged by is the database's.
WITH balance AS (
    UPDATE billing.balances b SET reserved = b.reserved + @reserved WHERE b.account_id = @account_id
), spend AS (
    UPDATE billing.spend s SET reserved = s.reserved + @reserved, spent = s.spent + @spent
    WHERE s.subject = @workspace_id AND s.window_start = @workspace_window
        OR s.subject = @key_id AND s.window_start = @key_window
)
UPDATE billing.reservations
SET state = @state, charged_seq = coalesce(sqlc.narg(charged_seq), charged_seq), entry = coalesce(sqlc.narg(entry), entry),
    charged = @charged, price = @price, waived = @waived, estimated = @estimated,
    final_usage = @final_usage, attempt_entries = coalesce(sqlc.narg(attempt_entries)::integer[], attempt_entries),
    profit = sqlc.narg(profit)::bigint, pricing_tiers = nullif(@pricing_tiers::smallint, -2)
WHERE request_id = @request_id
RETURNING clock_timestamp()::timestamptz AS ended_at;

-- name: EndReservationBatch :many
-- The batch form of EndReservation: every reservation of one completion batch ends in one statement. The balance gets
-- one sum and each spend window its own, so a batch's counters move exactly as its items' would one by one. final_usage
-- is the JSON text, NULL to keep what the row has; attempt_entries is a JSON array, NULL to keep the row's.
WITH input AS (
    SELECT i AS ord, (@request_ids::uuid[])[i] AS request_id, (@states::text[])[i] AS state,
        (@charged_seqs::integer[])[i] AS charged_seq, (@entries::integer[])[i] AS entry,
        (@charged::bigint[])[i] AS charged, (@prices::bigint[])[i] AS price,
        (@waived::boolean[])[i] AS waived, (@estimated::boolean[])[i] AS estimated,
        (@final_usages::text[])[i] AS final_usage, (@final_usage_set::boolean[])[i] AS final_usage_set,
        (@attempt_entries::text[])[i] AS attempt_entries, (@attempt_entries_set::boolean[])[i] AS attempt_entries_set,
        (@profits::bigint[])[i] AS profit, (@profit_set::boolean[])[i] AS profit_set,
        (@pricing_tiers::smallint[])[i] AS pricing_tiers,
        (@reserved::bigint[])[i] AS reserved, (@spent::bigint[])[i] AS spent,
        (@workspace_ids::uuid[])[i] AS workspace_id, (@workspace_windows::timestamptz[])[i] AS workspace_window,
        (@key_ids::uuid[])[i] AS key_id, (@key_windows::timestamptz[])[i] AS key_window
    FROM generate_subscripts(@request_ids::uuid[], 1) AS i
),
balance AS (
    UPDATE billing.balances b SET reserved = b.reserved + (SELECT sum(reserved) FROM input)
    WHERE b.account_id = @account_id
),
spend AS (
    UPDATE billing.spend s SET reserved = s.reserved + v.reserved, spent = s.spent + v.spent
    FROM (
        SELECT subject, window_start, sum(reserved) AS reserved, sum(spent) AS spent
        FROM (
            SELECT workspace_id AS subject, workspace_window AS window_start, reserved, spent FROM input
            UNION ALL
            SELECT key_id, key_window, reserved, spent FROM input
            WHERE NOT (key_id = workspace_id AND key_window = workspace_window)
        ) windows
        GROUP BY subject, window_start
    ) v
    WHERE s.subject = v.subject AND s.window_start = v.window_start
),
updated AS (
    UPDATE billing.reservations r
    SET state = i.state, charged_seq = coalesce(nullif(i.charged_seq, -1), r.charged_seq),
        entry = coalesce(nullif(i.entry, -1), r.entry),
        charged = i.charged, price = i.price, waived = i.waived, estimated = i.estimated,
        final_usage = CASE WHEN i.final_usage_set THEN i.final_usage::jsonb ELSE r.final_usage END,
        attempt_entries = CASE WHEN i.attempt_entries_set
            THEN ARRAY(SELECT jsonb_array_elements_text(i.attempt_entries::jsonb)::integer) ELSE r.attempt_entries END,
        profit = CASE WHEN i.profit_set THEN i.profit ELSE NULL END,
        pricing_tiers = nullif(i.pricing_tiers::smallint, -2)
    FROM input i
    WHERE r.request_id = i.request_id
    RETURNING r.request_id, clock_timestamp()::timestamptz AS ended_at
)
SELECT request_id, ended_at FROM updated;

-- name: PostLedger :execrows
-- Lock before allocating the transaction sequence. Every write depends on the inserted event, so a duplicate
-- changes neither entries nor balance. Entries in the site's own credits carry NULL currency.
WITH locked AS MATERIALIZED (
    SELECT 1 FROM billing.balances b WHERE b.account_id = @account_id FOR UPDATE
), posted AS (
    INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, attempt_seq, revision)
    SELECT @id, @site_id, @account_id, @kind, @request_id, @attempt_seq, @revision FROM locked
    ON CONFLICT (request_id, attempt_seq, kind, revision) DO NOTHING
    RETURNING id
), entries AS (
    INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount, currency)
    SELECT p.id, @site_id, @account_id, unnest(@books::text[]), unnest(@amounts::bigint[]),
        nullif(unnest(@currencies::text[]), '')
    FROM posted p
), balance AS (
    UPDATE billing.balances b SET balance = b.balance - @user_debit
    WHERE b.account_id = @account_id AND @user_debit::bigint <> 0 AND EXISTS (SELECT 1 FROM posted)
)
-- The command tag distinguishes a new event from a duplicate without allocating a result row.
SELECT 1 FROM posted;

-- name: AttemptCost :one
SELECT coalesce(sum(e.amount), 0)::bigint FROM billing.entries e JOIN billing.transactions t ON t.id = e.transaction_id
WHERE t.request_id = @request_id AND t.attempt_seq = @attempt_seq AND e.book = 'supplier_cost';

-- name: InsertCreditBatch :exec
-- A gift or redeemed code leaves what a payment has NULL (an empty currency or offline channel is NULL); a paid batch
-- sets them as credit_batches_source_check requires.
INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, expires_at, currency, paid, fee, merchant_id,
    offline_channel, rate_id)
VALUES (@id, @site_id, @account_id, @source, @amount, sqlc.narg(expires_at), nullif(@currency::text, ''), sqlc.narg(paid),
    sqlc.narg(fee), sqlc.narg(merchant_id), nullif(@offline_channel::text, ''), sqlc.narg(rate_id));

-- name: DueReservations :many
-- What cleanup must take over: pending past the deadline or under a reaped holder, the page that follows the request after.
SELECT r.request_id, r.account_id, r.state FROM billing.reservations r JOIN billing.holders h ON h.id = r.holder_id
WHERE r.state = 'pending' AND r.request_id > @after AND (r.deadline < clock_timestamp() OR h.reaped_at IS NOT NULL)
ORDER BY r.request_id
LIMIT @max;

-- name: Unbalanced :many
-- Daily reconciliation: transactions whose entries do not sum to zero in some currency, and balances that differ from
-- their book. A transaction balances in each currency on its own (NULL, the site's credits, groups as one); each side is
-- aggregated once, by a grouped scan, rather than summed again per row; the user-balance book reads from its own
-- partial index.
WITH transactions AS (
    SELECT DISTINCT e.transaction_id FROM billing.entries e GROUP BY e.transaction_id, e.currency HAVING sum(e.amount) <> 0
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

-- name: CorrectionCursor :one
SELECT after_at, after_id FROM billing.charge_correction_cursor WHERE singleton;

-- name: AdvanceCorrectionCursor :execrows
-- Only forward: a second worker's older page may not move the cursor back over events it has not read.
UPDATE billing.charge_correction_cursor SET after_at = @after_at, after_id = @after_id
WHERE singleton AND (after_at, after_id) < (@after_at, @after_id);

-- name: DueCorrections :many
SELECT event_id, revision, request_id, attempts, next_at FROM billing.charge_corrections
WHERE next_at <= clock_timestamp()
ORDER BY next_at, event_id
LIMIT @max;

-- name: UpsertCorrection :exec
INSERT INTO billing.charge_corrections (event_id, revision, request_id, next_at, attempts, last_error)
VALUES (@event_id, @revision, @request_id, @next_at, 1, @last_error)
ON CONFLICT (event_id) DO UPDATE SET revision = excluded.revision, request_id = excluded.request_id,
    next_at = excluded.next_at, attempts = billing.charge_corrections.attempts + 1, last_error = excluded.last_error;

-- name: DeleteCorrection :exec
DELETE FROM billing.charge_corrections WHERE event_id = @event_id;

-- name: LockChargeEvents :exec
-- The consumer side of the shared charge-event commit order (suppliers' LockChargeEvents): read the cursor and the
-- events only after every producer that inserted one has committed.
SELECT pg_advisory_xact_lock(hashtext('axiom charge events'));
