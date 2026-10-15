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
SELECT balance, floor, reserved FROM billing.balances WHERE account_id = @account_id FOR UPDATE;

-- Run after LockBalance, so it starts after the lock and its snapshot holds every change committed before it. A
-- window is the UTC day, week or month of a limit that resets (accounts' daily, weekly, monthly) by the database's
-- clock, and the epoch, which no window of a limit that resets can start at, for one that never resets or no limit.
-- name: ReadUsage :one
WITH windows AS (
    SELECT
        CASE @workspace_reset::text
            WHEN 'daily' THEN date_trunc('day', clock_timestamp(), 'UTC')
            WHEN 'weekly' THEN date_trunc('week', clock_timestamp(), 'UTC')
            WHEN 'monthly' THEN date_trunc('month', clock_timestamp(), 'UTC')
            ELSE 'epoch' END AS workspace_window,
        CASE @key_reset::text
            WHEN 'daily' THEN date_trunc('day', clock_timestamp(), 'UTC')
            WHEN 'weekly' THEN date_trunc('week', clock_timestamp(), 'UTC')
            WHEN 'monthly' THEN date_trunc('month', clock_timestamp(), 'UTC')
            ELSE 'epoch' END AS key_window
)
SELECT
    w.workspace_window::timestamptz AS workspace_window,
    w.key_window::timestamptz AS key_window,
    coalesce((SELECT s.spent + s.reserved FROM billing.spend s
        WHERE s.subject = @workspace_id AND s.window_start = w.workspace_window), 0)::bigint AS workspace_used,
    coalesce((SELECT s.spent + s.reserved FROM billing.spend s
        WHERE s.subject = @key_id AND s.window_start = w.key_window), 0)::bigint AS key_used
FROM windows w;

-- One statement writes the reservation and its three counters.
-- name: InsertReservation :exec
WITH reservation AS (
    INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
        workspace_window, key_window, segment, amount, state, deadline)
    VALUES (@request_id, @site_id, @account_id, @workspace_id, @key_id, @holder_id,
        @workspace_window, @key_window, 1, @amount, 'pending', clock_timestamp() + make_interval(0, 0, 0, 0, 0, 0, @lifetime_seconds::float8))
), balance AS (
    UPDATE billing.balances b SET reserved = b.reserved + @amount WHERE b.account_id = @account_id
)
INSERT INTO billing.spend AS s (subject, window_start, site_id, account_id, reserved)
VALUES (@workspace_id, @workspace_window, @site_id, @account_id, @amount),
    (@key_id, @key_window, @site_id, @account_id, @amount)
ON CONFLICT (subject, window_start) DO UPDATE SET reserved = s.reserved + excluded.reserved;

-- name: GetReservation :one
SELECT r.segment, r.amount, r.state, r.workspace_window, r.key_window,
    coalesce((SELECT s.spent + s.reserved FROM billing.spend s
        WHERE s.subject = r.workspace_id AND s.window_start = r.workspace_window), 0)::bigint AS workspace_used,
    coalesce((SELECT s.spent + s.reserved FROM billing.spend s
        WHERE s.subject = r.key_id AND s.window_start = r.key_window), 0)::bigint AS key_used
FROM billing.reservations r
WHERE r.request_id = @request_id;

-- Compare and write: only the next segment of a pending reservation, so a retried extension changes nothing.
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
    'epoch', 'epoch', 0, 0, 'released', clock_timestamp());
