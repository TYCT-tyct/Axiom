-- name: AuditDigestNext :one
-- The UTC day the next digest is of: the day after the last one made, or yesterday before the first.
SELECT coalesce(((SELECT max(day) FROM operations.audit_digests) + 1)::timestamp AT TIME ZONE 'UTC',
    date_trunc('day', now(), 'UTC') - interval '1 day')::timestamptz AS day_start;

-- name: AuditDayOperations :many
-- The agent's operations proposed in [@day_start, @day_end), by tool and where each is now.
SELECT tool, state, count(*)::integer AS count FROM operations.operations
WHERE created_at >= @day_start AND created_at < @day_end
GROUP BY tool, state ORDER BY tool, state;

-- name: AuditDayApprovals :many
-- The approvals asked in [@day_start, @day_end), by where each is now.
SELECT state, count(*)::integer AS count FROM operations.approvals
WHERE requested_at >= @day_start AND requested_at < @day_end
GROUP BY state ORDER BY state;

-- name: AuditDayRefusals :many
-- The calls of [@day_start, @day_end) the guards or the target refused (4xx), by error code. The log's IDs are UUIDv7
-- made when the call was logged, so the day is a range of the primary key: the least UUIDv7 of a moment is its
-- milliseconds as 12 hex digits followed by zeros, at or below every ID made from then on.
WITH b AS (
    SELECT (lpad(to_hex((extract(epoch FROM @day_start::timestamptz) * 1000)::bigint), 12, '0') || repeat('0', 20))::uuid AS first_id,
        (lpad(to_hex((extract(epoch FROM @day_end::timestamptz) * 1000)::bigint), 12, '0') || repeat('0', 20))::uuid AS end_id
)
SELECT coalesce(l.error_code, '')::text AS code, count(*)::integer AS count FROM operations.operation_log l, b
WHERE l.id >= b.first_id AND l.id < b.end_id AND l.status BETWEEN 400 AND 499
GROUP BY 1 ORDER BY 1;

-- name: AuditDayMoney :many
-- Every cap policy with what its category's counter of the day starting @day_start holds, zero where there is none.
SELECT p.category, p.site_id, p.currency, p.daily_max, p.hard_daily_max, p.daily_count_max,
    coalesce(d.reserved, 0)::bigint AS reserved, coalesce(d.committed, 0)::bigint AS committed,
    coalesce(d.count, 0)::integer AS count
FROM operations.cap_policies p
LEFT JOIN operations.cap_counters d ON d.category = p.category AND d.site_id IS NOT DISTINCT FROM p.site_id
    AND d.window_kind = 'day' AND d.window_start = @day_start AND d.object_kind = ''
ORDER BY p.category, p.site_id NULLS FIRST;

-- name: AuditChainState :one
-- How many faults have stopped the chain and when the last check of the whole chain finished.
SELECT f.n AS faults, k.finished_at AS checked_at
FROM (SELECT count(*)::bigint AS n FROM operations.audit_chain_faults) f
LEFT JOIN operations.audit_chain_checks k ON true;

-- name: InsertAuditDigest :execrows
-- 0 rows when the day's digest was made already, by another node or an earlier attempt.
INSERT INTO operations.audit_digests (day, body)
VALUES ((sqlc.arg(day_start)::timestamptz AT TIME ZONE 'UTC')::date, sqlc.arg(body))
ON CONFLICT (day) DO NOTHING;

-- name: AuditDigests :many
-- The digests newest first, those of the days before the one starting at @before when it is set.
SELECT body FROM operations.audit_digests
WHERE sqlc.narg(before)::timestamptz IS NULL OR day < (sqlc.narg(before)::timestamptz AT TIME ZONE 'UTC')::date
ORDER BY day DESC LIMIT sqlc.arg(n)::bigint;
