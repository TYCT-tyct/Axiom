-- What the console reads of an account's money (README.md「控制台的读取」). They run in the account's scope and take no
-- lock: a page may show a figure a moment old.

-- name: ReadBalance :many
SELECT balance, floor, reserved, frozen FROM billing.balances WHERE account_id = @account_id;

-- name: SpentThisMonth :one
-- What the subjects, the account's workspaces, have been charged since the first of the month (UTC, by the database's
-- clock). Every charge is counted on its workspace once, and on its key, which is why keys are not summed too.
SELECT coalesce(sum(spent), 0)::bigint FROM billing.spend
WHERE subject = ANY (@subjects::uuid[]) AND window_start >= date_trunc('month', clock_timestamp(), 'UTC');

-- name: ReadKeysUsed :many
-- What each key has spent and holds reserved in the window of its own reset that holds the database's today, as
-- admission counts it (ReadUsage).
WITH k AS (
    SELECT (@keys::uuid[])[i]::uuid AS key_id, (@resets::text[])[i]::text AS reset
    FROM generate_subscripts(@keys::uuid[], 1) AS i
)
SELECT k.key_id, coalesce(sum(s.spent + s.reserved), 0)::bigint AS used
FROM k
LEFT JOIN billing.spend s ON s.subject = k.key_id
    AND s.window_start >= billing.window_from(k.reset, clock_timestamp())
    AND s.window_start < billing.window_to(k.reset, clock_timestamp())
GROUP BY k.key_id;
