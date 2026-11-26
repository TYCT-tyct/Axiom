-- What a supplier's sync and reconciliation found, as the admin reads it back (docs/domains/suppliers.md「同步记录」
-- 「对账」).

-- name: SyncRunsPage :many
-- The commits of the sync that recorded a difference, newest first; a page starts below the version of the one before.
SELECT e.version, max(e.created_at)::timestamptz AS at
FROM suppliers.sync_events e
WHERE e.supplier_id = @supplier_id AND (sqlc.narg(before)::bigint IS NULL OR e.version < sqlc.narg(before))
GROUP BY e.version
ORDER BY e.version DESC
LIMIT @page_size::int;

-- name: SyncEventsOf :many
-- The differences of those commits, with the group each is about while Axiom still has it: the subject is a group's ref
-- for a group's kinds and "ref/model" for a model's.
SELECT e.version, e.seq, e.kind, e.subject, e.old_value, e.new_value, e.why, g.id AS group_id
FROM suppliers.sync_events e
LEFT JOIN suppliers.supplier_groups g ON g.supplier_id = e.supplier_id AND g.upstream_ref = CASE
    WHEN e.kind IN ('group_added', 'group_removed', 'ratio_changed', 'group_changed') THEN e.subject
    WHEN e.kind IN ('model_added', 'model_removed', 'model_changed', 'plan_changed') THEN split_part(e.subject, '/', 1)
END
WHERE e.supplier_id = @supplier_id AND e.version = ANY(@versions::bigint[])
ORDER BY e.version DESC, e.seq;

-- name: HasChargeRecords :one
SELECT EXISTS (SELECT FROM suppliers.charge_records WHERE supplier_id = @supplier_id)::boolean;

-- name: ReconciliationDays :many
-- Each UTC day from from_at to last_day, midnights, newest first (to_at is the midnight after last_day): what the day's attempts were estimated at, and what the supplier's
-- charge records of the day add up to, with the counts the matching reached. A charge above the estimate is one
-- reconciliation judged "under" (we estimated too little), below it "over". An attempt is unmatched once the grace is
-- over without a charge claiming it.
WITH days AS (
    SELECT (d AT TIME ZONE 'UTC')::date AS day
    FROM generate_series(@from_at::timestamptz, @last_day::timestamptz, interval '1 day') d
), estimated AS (
    SELECT (a.finished_at AT TIME ZONE 'UTC')::date AS day, sum(a.estimated_native) AS amount,
        count(*) FILTER (WHERE a.charge_ref IS NOT NULL AND a.state = 'pending' AND a.finished_at < @grace_before::timestamptz) AS unmatched
    FROM suppliers.attempt_costs a
    WHERE a.supplier_id = @supplier_id AND a.finished_at >= @from_at::timestamptz AND a.finished_at < @to_at::timestamptz
    GROUP BY 1
), charged AS (
    SELECT (c.occurred_at AT TIME ZONE 'UTC')::date AS day, sum(c.amount_native) AS amount, count(*) AS charges,
        count(*) FILTER (WHERE c.state = 'matched') AS matched, count(*) FILTER (WHERE c.state = 'held') AS held,
        count(*) FILTER (WHERE v.payload ->> 'verdict' = 'under') AS above, count(*) FILTER (WHERE v.payload ->> 'verdict' = 'over') AS below
    FROM suppliers.charge_records c
    LEFT JOIN suppliers.charge_events v ON c.state = 'matched' AND v.charge_record_id = c.id AND v.revision = c.revision
    WHERE c.supplier_id = @supplier_id AND c.occurred_at >= @from_at::timestamptz AND c.occurred_at < @to_at::timestamptz
    GROUP BY 1
)
SELECT d.day::text AS day, coalesce(e.amount, 0)::text AS estimated_native, coalesce(c.amount, 0)::text AS charged_native,
    coalesce(c.charges, 0)::bigint AS charges, coalesce(c.matched, 0)::bigint AS matched, coalesce(c.held, 0)::bigint AS held,
    coalesce(c.above, 0)::bigint AS charged_above, coalesce(c.below, 0)::bigint AS charged_below,
    coalesce(e.unmatched, 0)::bigint AS unmatched_attempts
FROM days d
LEFT JOIN estimated e ON e.day = d.day
LEFT JOIN charged c ON c.day = d.day
ORDER BY d.day DESC;
