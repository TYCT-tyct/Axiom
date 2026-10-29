-- name: InsertHolder :exec
INSERT INTO billing.holders (id, expires_at) VALUES (@id, clock_timestamp() + make_interval(0, 0, 0, 0, 0, 0, @ttl_seconds::float8));

-- Heartbeat does not ask whether the holder has expired: one that outlived a database outage unreaped carries on.
-- name: Heartbeat :execrows
UPDATE billing.holders SET expires_at = clock_timestamp() + make_interval(0, 0, 0, 0, 0, 0, @ttl_seconds::float8) WHERE id = @id AND reaped_at IS NULL;

-- FOR UPDATE waits for every write fenced by FOR KEY SHARE to end and keeps new ones out until the reap commits.
-- name: LockExpiredHolders :many
SELECT id FROM billing.holders
WHERE reaped_at IS NULL AND expires_at < clock_timestamp() - make_interval(0, 0, 0, 0, 0, 0, @grace_seconds::float8)
ORDER BY id
FOR UPDATE;

-- name: ReapHolders :exec
UPDATE billing.holders SET reaped_at = clock_timestamp() WHERE id = ANY (@ids::uuid[]);

-- The fence: the holder is neither reaped nor expired after the lock is granted, by the clock at that moment.
-- name: FenceHolder :one
SELECT id FROM billing.holders
WHERE id = @id AND reaped_at IS NULL AND expires_at > clock_timestamp()
FOR KEY SHARE;

-- name: InsertBalance :exec
INSERT INTO billing.balances (account_id, site_id, floor) VALUES (@account_id, @site_id, @floor);

-- name: LockBalance :one
SELECT balance, floor, reserved, frozen FROM billing.balances WHERE account_id = @account_id FOR UPDATE;

-- Run after LockBalance, so it starts after the lock and its snapshot holds every change committed before it.
-- Counters are kept per UTC day; a limit counts the days of its window that holds day, its UTC day, week or month,
-- or every day for a limit that never resets, so changing how often a limit resets counts all that the new window
-- covers. Admission passes no day, meaning today by the database's clock; an extension passes its reservation's day,
-- which it writes back to, so it checks and books in the same window.
-- name: ReadUsage :one
WITH d AS (SELECT date_trunc('day', coalesce(sqlc.narg(day)::timestamptz, clock_timestamp()), 'UTC') AS day)
SELECT
    d.day::timestamptz AS day,
    coalesce((SELECT sum(s.spent + s.reserved) FROM billing.spend s
        WHERE s.subject = @workspace_id AND s.window_start >= billing.window_from(@workspace_reset::text, d.day)
            AND s.window_start < billing.window_to(@workspace_reset::text, d.day)), 0)::bigint AS workspace_used,
    coalesce((SELECT sum(s.spent + s.reserved) FROM billing.spend s
        WHERE s.subject = @key_id AND s.window_start >= billing.window_from(@key_reset::text, d.day)
            AND s.window_start < billing.window_to(@key_reset::text, d.day)), 0)::bigint AS key_used
FROM d;

-- One statement writes the reservation and its three counters. prices, fallbacks, models and endpoints are by route
-- index, the zero UUID for a dropped entry: endpoints are what cleanup hands suppliers as the route of a request whose
-- node was lost.
-- Let the insert columns type the UUID parameters, so sqlc retains their typed array codec overrides.
-- name: InsertReservation :exec
WITH reservation AS (
    INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
        workspace_window, key_window, segment, amount, state, deadline, prices, fallbacks, models, endpoints, exchange_rates, price_versions)
    VALUES (@request_id, @site_id, @account_id, @workspace_id, @key_id, @holder_id,
        @workspace_window, @key_window, 1, @amount, 'pending', clock_timestamp() + make_interval(0, 0, 0, 0, 0, 0, @lifetime_seconds::float8),
        @prices::text[], @fallbacks::text[], @models, @endpoints, @exchange_rates::jsonb, @price_versions)
), balance AS (
    UPDATE billing.balances b SET reserved = b.reserved + @amount WHERE b.account_id = @account_id
)
INSERT INTO billing.spend AS s (subject, window_start, site_id, account_id, reserved)
VALUES (@workspace_id, @workspace_window, @site_id, @account_id, @amount),
    (@key_id, @key_window, @site_id, @account_id, @amount)
ON CONFLICT (subject, window_start) DO UPDATE SET reserved = s.reserved + excluded.reserved;

-- name: GetReservation :one
SELECT segment, amount, state, workspace_window, key_window FROM billing.reservations WHERE request_id = @request_id;

-- Compare and write on the segment read under the lock.
-- name: ExtendReservation :execrows
UPDATE billing.reservations SET segment = segment + 1, amount = amount + @delta
WHERE request_id = @request_id AND segment = @previous_segment AND state = 'pending';

-- name: ReleaseReservation :execrows
UPDATE billing.reservations SET state = 'released' WHERE request_id = @request_id AND state = 'pending';

-- Moves the three counters of a reservation by delta, negative to release.
-- name: AddReserved :exec
WITH balance AS (
    UPDATE billing.balances b SET reserved = b.reserved + @delta WHERE b.account_id = @account_id
)
UPDATE billing.spend s SET reserved = s.reserved + @delta
WHERE s.subject = @workspace_id AND s.window_start = @workspace_window
    OR s.subject = @key_id AND s.window_start = @key_window;

-- name: InsertTombstone :exec
INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
    workspace_window, key_window, segment, amount, state, deadline)
VALUES (@request_id, @site_id, @account_id, @workspace_id, @key_id, @holder_id,
    date_trunc('day', clock_timestamp(), 'UTC'), date_trunc('day', clock_timestamp(), 'UTC'), 0, 0, 'released',
    clock_timestamp());

-- name: Now :one
SELECT clock_timestamp()::timestamptz;

-- The account's balance and what it has been charged in all, for the read-only endpoints: no lock, and no row means a
-- balance of 0, which is how Reserve treats it too. Spent is summed over the account's workspaces only; each charge is
-- also counted under its key.
-- name: ReadFunds :one
SELECT b.balance,
    coalesce((SELECT sum(s.spent) FROM billing.spend s WHERE s.subject = ANY (@workspaces::uuid[])), 0)::bigint AS spent
FROM billing.balances b
WHERE b.account_id = @account_id;

-- What a key has spent: all time, and in the current UTC day, week and month, plus what its limit counts in the
-- current window of its reset (spent and reserved together, as admission does). An empty reset is the window of every
-- day. A key that never spent has no rows, and its sums are 0.
-- name: ReadKeyUsage :one
WITH t AS (SELECT clock_timestamp() AS now)
SELECT
    coalesce(sum(s.spent), 0)::bigint AS total,
    coalesce(sum(s.spent) FILTER (WHERE s.window_start >= billing.window_from('daily', t.now)), 0)::bigint AS daily,
    coalesce(sum(s.spent) FILTER (WHERE s.window_start >= billing.window_from('weekly', t.now)), 0)::bigint AS weekly,
    coalesce(sum(s.spent) FILTER (WHERE s.window_start >= billing.window_from('monthly', t.now)), 0)::bigint AS monthly,
    coalesce(sum(s.spent + s.reserved) FILTER (WHERE s.window_start >= billing.window_from(@reset::text, t.now)
        AND s.window_start < billing.window_to(@reset::text, t.now)), 0)::bigint AS limited
FROM billing.spend s, t
WHERE s.subject = @key_id;
