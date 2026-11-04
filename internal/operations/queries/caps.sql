-- name: CapPolicies :many
SELECT * FROM operations.cap_policies ORDER BY category, site_id NULLS FIRST;

-- name: CapPolicyOf :many
SELECT * FROM operations.cap_policies WHERE category = @category AND site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid;

-- name: LockCapPolicy :many
-- FOR SHARE by a proposal, so a change of the policy waits for the proposals weighed against it; FOR UPDATE by the
-- change.
SELECT * FROM operations.cap_policies WHERE category = @category AND site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid
FOR SHARE;

-- name: LockCapPolicyForUpdate :many
SELECT * FROM operations.cap_policies WHERE category = @category AND site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid
FOR UPDATE;

-- name: InsertCapPolicy :one
INSERT INTO operations.cap_policies (site_id, category, currency, single_max, daily_max, weekly_max, object_daily_max,
    daily_count_max, hard_single_max, hard_daily_max, hard_weekly_max, rev)
VALUES (sqlc.narg(site_id)::uuid, @category, @currency, @single_max, @daily_max, @weekly_max, @object_daily_max,
    @daily_count_max, @hard_single_max, @hard_daily_max, @hard_weekly_max, 1)
RETURNING *;

-- name: UpdateCapPolicy :one
UPDATE operations.cap_policies SET single_max = @single_max, daily_max = @daily_max, weekly_max = @weekly_max,
    object_daily_max = @object_daily_max, daily_count_max = @daily_count_max, hard_single_max = @hard_single_max,
    hard_daily_max = @hard_daily_max, hard_weekly_max = @hard_weekly_max, rev = rev + 1, updated_at = now()
WHERE category = @category AND site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid
RETURNING *;

-- name: CapWindows :one
-- The UTC day and ISO week a proposal falls in, and the end of the day, by the database's clock as it is now: now() is
-- the transaction's start, and a proposal that began before midnight may get here after it.
SELECT date_trunc('day', c.at, 'UTC')::timestamptz AS day_start, date_trunc('week', c.at, 'UTC')::timestamptz AS week_start,
    (date_trunc('day', c.at, 'UTC') + interval '1 day')::timestamptz AS day_end
FROM (SELECT clock_timestamp() AS at) c;

-- name: WindowEnded :one
-- Whether the UTC day that starts at @day_start is over by the database's clock as it is now.
SELECT clock_timestamp() >= sqlc.arg(day_start)::timestamptz + interval '1 day' AS ended;

-- name: EnsureCapCounters :exec
-- The three rows a money action moves, made at zero if they are not there yet, in the order they are locked.
INSERT INTO operations.cap_counters (category, window_kind, window_start, object_kind, object_id, site_id)
VALUES (@category, 'day', @day_start, '', '', sqlc.narg(site_id)::uuid),
    (@category, 'week', @week_start, '', '', sqlc.narg(site_id)::uuid),
    (@category, 'day', @day_start, @object_kind, @object_id, sqlc.narg(site_id)::uuid)
ON CONFLICT DO NOTHING;

-- name: LockCapCounters :many
-- The category's day, then its week, then the object's day: every path locks them in this order.
SELECT * FROM operations.cap_counters
WHERE category = @category AND site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid AND (
    (window_kind = 'day' AND window_start = @day_start AND object_kind = '')
    OR (window_kind = 'week' AND window_start = @week_start AND object_kind = '')
    OR (window_kind = 'day' AND window_start = @day_start AND object_kind = @object_kind AND object_id = @object_id))
ORDER BY object_kind, window_kind
FOR UPDATE;

-- name: MoveCapCounters :exec
UPDATE operations.cap_counters SET reserved = reserved + @reserved, committed = committed + @committed, count = count + @count
WHERE id = ANY (@ids::bigint[]);

-- name: InsertMoneyAction :exec
INSERT INTO operations.money_actions (operation_id, category, site_id, object_kind, object_id, amount, currency, basis,
    policy_rev, day_start, week_start, expires_at, state)
VALUES (@operation_id, @category, sqlc.narg(site_id)::uuid, @object_kind, @object_id, @amount, @currency, @basis,
    @policy_rev, @day_start, @week_start, @expires_at, 'reserved');

-- name: LockMoneyAction :many
-- The reservation of an operation its caller has locked, and whether its windows have ended by the database's clock as
-- it is now.
SELECT sqlc.embed(m), (m.expires_at <= clock_timestamp())::bool AS due FROM operations.money_actions m
WHERE m.operation_id = @operation_id FOR UPDATE;

-- name: MoneyActionOf :many
SELECT * FROM operations.money_actions WHERE operation_id = @operation_id;

-- name: LockDueMoneyActions :many
-- Reservations past the end of their windows, each locked with its operation first, as every other path takes them;
-- one whose operation or reservation another transaction holds is skipped, not waited for, and its state is rechecked
-- on the row's new version.
SELECT m.operation_id FROM operations.money_actions m JOIN operations.operations o ON o.id = m.operation_id
WHERE m.state = 'reserved' AND m.expires_at <= clock_timestamp()
ORDER BY m.operation_id
LIMIT @batch
FOR UPDATE OF o, m SKIP LOCKED;

-- name: SettleMoneyAction :exec
UPDATE operations.money_actions SET state = @state, proof = @proof, confirmed_by = @confirmed_by, settled_at = now()
WHERE operation_id = @operation_id;
