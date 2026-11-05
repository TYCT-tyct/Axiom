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

-- name: CapUsage :many
-- Every policy with what its category's counters of the current UTC day and ISO week hold, zero where there is no row.
SELECT sqlc.embed(p), w.day_start, w.week_start,
    coalesce(d.reserved, 0)::bigint AS day_reserved, coalesce(d.committed, 0)::bigint AS day_committed,
    coalesce(d.count, 0)::integer AS day_count,
    coalesce(k.reserved, 0)::bigint AS week_reserved, coalesce(k.committed, 0)::bigint AS week_committed,
    coalesce(k.count, 0)::integer AS week_count
FROM operations.cap_policies p
CROSS JOIN (SELECT date_trunc('day', clock_timestamp(), 'UTC')::timestamptz AS day_start,
    date_trunc('week', clock_timestamp(), 'UTC')::timestamptz AS week_start) w
LEFT JOIN operations.cap_counters d ON d.category = p.category AND d.site_id IS NOT DISTINCT FROM p.site_id
    AND d.window_kind = 'day' AND d.window_start = w.day_start AND d.object_kind = ''
LEFT JOIN operations.cap_counters k ON k.category = p.category AND k.site_id IS NOT DISTINCT FROM p.site_id
    AND k.window_kind = 'week' AND k.window_start = w.week_start AND k.object_kind = ''
ORDER BY p.category, p.site_id NULLS FIRST;

-- name: CapDrift :many
-- The counters of the current UTC day and ISO week that differ from what the money actions reserved in them add up
-- to: every action counts once in its day, its week and its object's day, reserved or committed by its state, and in
-- the count whatever its state. The windows are the caller's (CapWindows), so a repair recomputes the rows it locked
-- even across midnight. Of every scope with @every, otherwise of @category and @site_id. One statement, so it sees
-- each transaction that moved an action and its counters whole or not at all.
WITH w AS (
    SELECT sqlc.arg(day_start)::timestamptz AS day_start, sqlc.arg(week_start)::timestamptz AS week_start
), entries AS (
    SELECT c.category, c.site_id, c.window_kind, c.window_start, c.object_kind, c.object_id, c.reserved, c.committed,
        c.count, 0::bigint AS want_reserved, 0::bigint AS want_committed, 0 AS want_count
    FROM operations.cap_counters c, w
    WHERE (c.window_kind = 'day' AND c.window_start = w.day_start) OR (c.window_kind = 'week' AND c.window_start = w.week_start)
    UNION ALL
    SELECT m.category, m.site_id, k.window_kind, k.window_start, k.object_kind, k.object_id, 0, 0, 0,
        CASE WHEN m.state = 'reserved' THEN m.amount ELSE 0 END, CASE WHEN m.state = 'committed' THEN m.amount ELSE 0 END, 1
    FROM operations.money_actions m
    CROSS JOIN w
    CROSS JOIN LATERAL (VALUES ('day', m.day_start, '', ''), ('week', m.week_start, '', ''),
        ('day', m.day_start, m.object_kind, m.object_id)) k (window_kind, window_start, object_kind, object_id)
    WHERE m.week_start = w.week_start
        AND ((k.window_kind = 'day' AND k.window_start = w.day_start) OR (k.window_kind = 'week' AND k.window_start = w.week_start))
)
SELECT r.category::text AS category, r.site_id AS site_id, r.window_kind::text AS window_kind,
    r.window_start::timestamptz AS window_start, r.object_kind::text AS object_kind, r.object_id::text AS object_id,
    sum(r.reserved)::bigint AS reserved, sum(r.committed)::bigint AS committed, sum(r.count)::integer AS count,
    sum(r.want_reserved)::bigint AS want_reserved, sum(r.want_committed)::bigint AS want_committed,
    sum(r.want_count)::integer AS want_count
FROM entries r
WHERE @every::bool OR (r.category = @category::text AND r.site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid)
GROUP BY r.category, r.site_id, r.window_kind, r.window_start, r.object_kind, r.object_id
HAVING (sum(r.reserved), sum(r.committed), sum(r.count)) IS DISTINCT FROM
    (sum(r.want_reserved), sum(r.want_committed), sum(r.want_count))
ORDER BY 1, 2 NULLS FIRST, 5, 3, 6;

-- name: LockScopeCounters :exec
-- Every counter of a scope and category in a UTC day and ISO week, in the order every path locks its own
-- three (LockCapCounters), the object's days by object after them.
SELECT c.id FROM operations.cap_counters c
WHERE c.category = @category AND c.site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid AND (
    (c.window_kind = 'day' AND c.window_start = @day_start) OR (c.window_kind = 'week' AND c.window_start = @week_start))
ORDER BY c.object_kind, c.window_kind, c.object_id
FOR UPDATE;

-- name: SetCapCounter :exec
INSERT INTO operations.cap_counters (category, window_kind, window_start, object_kind, object_id, site_id, reserved,
    committed, count)
VALUES (@category, @window_kind, @window_start, @object_kind, @object_id, sqlc.narg(site_id)::uuid, @reserved,
    @committed, @count)
ON CONFLICT (category, window_kind, window_start, object_kind, object_id, site_id)
DO UPDATE SET reserved = excluded.reserved, committed = excluded.committed, count = excluded.count;

-- name: SetCapPaused :execrows
-- Pauses (@paused) or resumes a policy the caller holds FOR UPDATE; no row when it already is.
UPDATE operations.cap_policies SET paused_at = CASE WHEN @paused::bool THEN now() END
WHERE category = @category AND site_id IS NOT DISTINCT FROM sqlc.narg(site_id)::uuid AND (paused_at IS NULL) = @paused::bool;
