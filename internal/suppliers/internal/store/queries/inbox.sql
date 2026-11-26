-- The suppliers' to-do queue and each supplier's intake rule (docs/domains/suppliers.md「待处理队列」「接入规则」).

-- name: ObserveInbox :exec
-- Brings the queue of one supplier in line with what its tables say now, for the kinds named: a condition found and not
-- queued is added, one queued and no longer found is deleted. A sighting is an observation by the sync: it shows an
-- ignored item again and stamps it seen; an item whose figures changed is stamped too. Anything else leaves a row alone,
-- so an observation that finds the same writes nothing. A ratio change counts from ratio_since, the spend a balance is
-- divided by from spend_since; a day's cost is its matched charges' against their estimates, from charges_since, and
-- relative is the reconciliation's tolerance.
WITH cur (kind, subject, detail) AS (
    SELECT 'key_invalid'::text, c.id::text, jsonb_build_object('credential_id', c.id::text, 'group_id', c.supplier_group_id::text)
    FROM suppliers.credentials c
    WHERE c.supplier_id = @supplier_id AND c.status = 'rejected' AND 'key_invalid' = ANY(@kinds::text[])
    UNION ALL
    SELECT 'group_added', g.id::text, jsonb_build_object('group_id', g.id::text, 'ratio', g.ratio::text)
    FROM suppliers.supplier_groups g
    WHERE g.supplier_id = @supplier_id AND g.present AND NOT g.selected AND g.codename IS NULL
        AND 'group_added' = ANY(@kinds::text[])
    UNION ALL
    SELECT 'unknown_model', m.supplier_group_id::text || '/' || m.upstream_name,
        jsonb_build_object('group_id', m.supplier_group_id::text, 'upstream_name', m.upstream_name)
    FROM suppliers.supplier_models m
    JOIN suppliers.supplier_groups g ON g.id = m.supplier_group_id
    WHERE m.supplier_id = @supplier_id AND m.model_id IS NULL AND m.present AND g.present
        AND 'unknown_model' = ANY(@kinds::text[])
        AND NOT EXISTS (
            SELECT FROM suppliers.supplier_aliases a
            WHERE a.supplier_id = m.supplier_id AND a.upstream_name = m.upstream_name
                AND (a.supplier_group_id IS NULL OR a.supplier_group_id = m.supplier_group_id))
    UNION ALL
    SELECT 'ratio_changed', r.group_id::text, jsonb_build_object('group_id', r.group_id::text, 'previous_ratio', r.old_value, 'ratio', r.new_value)
    FROM (
        SELECT DISTINCT ON (e.subject) g.id AS group_id, e.old_value, e.new_value
        FROM suppliers.sync_events e
        JOIN suppliers.supplier_groups g ON g.supplier_id = e.supplier_id AND g.upstream_ref = e.subject AND g.selected AND g.present
        WHERE e.supplier_id = @supplier_id AND e.kind = 'ratio_changed' AND e.created_at > @ratio_since::timestamptz
            AND 'ratio_changed' = ANY(@kinds::text[])
        ORDER BY e.subject, e.version DESC
    ) r
    UNION ALL
    SELECT 'low_balance', 'balance', jsonb_build_object('balance_native', s.balance_native::text,
        'balance_days', CASE WHEN sp.spent > 0 THEN floor(s.balance_native / sp.spent)::bigint END)
    FROM suppliers.suppliers s
    LEFT JOIN LATERAL (
        SELECT sum(a.estimated_native) AS spent FROM suppliers.attempt_costs a
        WHERE a.supplier_id = s.id AND a.finished_at > @spend_since::timestamptz
    ) sp ON true
    WHERE s.id = @supplier_id AND s.status = 'low_balance' AND s.balance_native IS NOT NULL
        AND 'low_balance' = ANY(@kinds::text[])
    UNION ALL
    SELECT 'cost_mismatch', d.day::text, jsonb_build_object('date', d.day::text, 'estimated_native', d.estimated::text, 'charged_native', d.charged::text)
    FROM (
        SELECT (c.occurred_at AT TIME ZONE 'UTC')::date AS day, sum(a.estimated_native) AS estimated, sum(c.amount_native) AS charged
        FROM suppliers.charge_records c
        JOIN suppliers.attempt_costs a ON a.request_id = c.attempt_request_id AND a.attempt_seq = c.attempt_seq
        WHERE c.supplier_id = @supplier_id AND c.state = 'matched' AND 'cost_mismatch' = ANY(@kinds::text[])
            AND c.occurred_at >= @charges_since::timestamptz
        GROUP BY 1
    ) d
    WHERE abs(d.charged - d.estimated) > d.estimated * @relative::numeric
), seen AS (
    INSERT INTO suppliers.inbox_items AS i (supplier_id, kind, subject, detail, first_seen_at, last_seen_at)
    SELECT @supplier_id, cur.kind, cur.subject, cur.detail, @now, @now FROM cur
    ON CONFLICT (supplier_id, kind, subject) DO UPDATE
    SET detail = excluded.detail, last_seen_at = excluded.last_seen_at,
        ignored_at = CASE WHEN @sighting::boolean THEN NULL ELSE i.ignored_at END
    WHERE i.detail IS DISTINCT FROM excluded.detail OR (@sighting::boolean AND i.ignored_at IS NOT NULL)
    RETURNING 1
)
DELETE FROM suppliers.inbox_items i
WHERE i.supplier_id = @supplier_id AND i.kind = ANY(@kinds::text[])
    AND NOT EXISTS (SELECT FROM cur WHERE cur.kind = i.kind AND cur.subject = i.subject);

-- name: InboxPage :many
-- The items that show, most recently seen first, of the platform's own suppliers that are not removed; a page starts
-- after the last item of the one before.
SELECT i.id, i.supplier_id, i.kind, i.detail, i.first_seen_at, i.last_seen_at, i.ignored_at
FROM suppliers.inbox_items i
JOIN suppliers.suppliers s ON s.id = i.supplier_id
WHERE i.ignored_at IS NULL AND s.owner_scope = 'platform' AND s.status NOT IN ('removing', 'removed')
    AND (sqlc.narg(kind)::text IS NULL OR i.kind = sqlc.narg(kind))
    AND (sqlc.narg(supplier_id)::uuid IS NULL OR i.supplier_id = sqlc.narg(supplier_id))
    AND (sqlc.narg(after_seen)::timestamptz IS NULL OR (i.last_seen_at, i.id) < (sqlc.narg(after_seen), sqlc.narg(after_id)::uuid))
ORDER BY i.last_seen_at DESC, i.id DESC
LIMIT @page_size::int;

-- name: InboxCounts :many
SELECT i.kind, count(*)::bigint AS items
FROM suppliers.inbox_items i
JOIN suppliers.suppliers s ON s.id = i.supplier_id
WHERE i.ignored_at IS NULL AND s.owner_scope = 'platform' AND s.status NOT IN ('removing', 'removed')
GROUP BY i.kind;

-- name: LockInboxItem :many
SELECT i.id, i.supplier_id, i.kind, i.detail, i.first_seen_at, i.last_seen_at, i.ignored_at
FROM suppliers.inbox_items i
JOIN suppliers.suppliers s ON s.id = i.supplier_id
WHERE i.id = @id AND s.owner_scope = 'platform'
FOR UPDATE OF i;

-- name: IgnoreInboxItem :exec
UPDATE suppliers.inbox_items SET ignored_at = @now WHERE id = @id;

-- name: GetIntakeRule :many
SELECT mode, include, exclude, rev FROM suppliers.intake_rules WHERE supplier_id = @supplier_id;

-- name: LockIntakeRule :many
SELECT mode, include, exclude, rev FROM suppliers.intake_rules WHERE supplier_id = @supplier_id FOR UPDATE;

-- name: PutIntakeRule :exec
INSERT INTO suppliers.intake_rules (supplier_id, mode, include, exclude, rev)
VALUES (@supplier_id, @mode, @include::text[], @exclude::text[], @rev)
ON CONFLICT (supplier_id) DO UPDATE SET mode = excluded.mode, include = excluded.include, exclude = excluded.exclude, rev = excluded.rev;
