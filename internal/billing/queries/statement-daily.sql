-- name: StatementDailyKinds :many
-- Called only after StatementDailyBudget accepts the same read snapshot. Use StatementPage's balance entries and refund-hold deltas. Keep all dates through the balance window: a later
-- sequence can have an earlier timestamp, and filtering at the cursor first would corrupt historical balances.
WITH movements AS (
    SELECT t.id, t.seq, t.created_at, t.kind, coalesce(b.source, '')::text AS source,
        -coalesce(e.debit, 0) AS amount,
        CASE WHEN t.kind IN ('freeze', 'unfreeze') THEN
            (t.revision - coalesce((SELECT prior.revision FROM billing.transactions prior
                WHERE prior.account_id = @account_id AND prior.request_id = t.request_id AND prior.attempt_seq = 0
                    AND prior.kind = t.kind AND prior.revision < t.revision
                ORDER BY prior.revision DESC LIMIT 1), 0)) * CASE WHEN t.kind = 'freeze' THEN 1 ELSE -1 END
            WHEN t.kind = 'refund' THEN -coalesce(e.debit, 0) ELSE 0 END AS frozen_change
    FROM billing.transactions t
    LEFT JOIN LATERAL (SELECT sum(e.amount) AS debit FROM billing.entries e
        WHERE e.transaction_id = t.id AND e.book = 'user_balance') e ON true
    LEFT JOIN billing.credit_batches b ON t.kind = 'credit' AND b.id = t.request_id
    WHERE t.account_id = @account_id
), positioned AS (
    SELECT *, (created_at AT TIME ZONE @time_zone::text)::date AS day,
        CASE WHEN seq IS NOT NULL THEN
            (SELECT balance FROM billing.balances WHERE account_id = @account_id) -
            coalesce(sum(amount) OVER (ORDER BY seq DESC NULLS LAST, created_at DESC, id DESC
                ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0)
        END AS balance_after
    FROM movements
), visible AS (
    SELECT *, row_number() OVER (PARTITION BY day ORDER BY seq DESC NULLS LAST, created_at DESC, id DESC) AS day_position
    FROM positioned
    WHERE amount <> 0 OR frozen_change <> 0
), daily AS (
    SELECT day FROM visible
    WHERE @before_day::text = '' OR day < nullif(@before_day::text, '')::date
    GROUP BY day ORDER BY day DESC LIMIT @max
), closing AS (
    SELECT day, balance_after::bigint AS balance
    FROM visible WHERE day_position = 1 AND seq IS NOT NULL
), grouped AS (
    SELECT v.day, v.kind, v.source, sum(v.amount)::text AS amount
    FROM visible v JOIN daily d ON d.day = v.day
    GROUP BY v.day, v.kind, v.source
)
SELECT g.day::text AS day, g.kind, g.source, g.amount, c.balance AS closing_balance
FROM grouped g LEFT JOIN closing c ON c.day = g.day
ORDER BY g.day DESC, g.kind, g.source;

-- name: StatementDailyBudget :one
-- Bound the probe using the existing account/sequence index before reading entries or sorting movements.
SELECT count(*)::bigint FROM (
    SELECT seq FROM billing.transactions WHERE account_id = @account_id ORDER BY seq LIMIT @max
) candidates;
