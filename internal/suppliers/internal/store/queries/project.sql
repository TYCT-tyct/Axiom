-- The projection of a sync's state onto the tables routing and the back office read (internal/suppliers/README.md「持久化」).
-- Every statement here is written by the projection only, and only when what the listing says differs from the row: a
-- listing read twice changes nothing.

-- name: ListProjectedGroups :many
SELECT id, upstream_ref, name, ratio::text AS ratio, facts::text AS facts, present
FROM suppliers.supplier_groups
WHERE supplier_id = @supplier_id
ORDER BY upstream_ref;

-- name: InsertProjectedGroup :one
-- A group is made unselected: which groups to use is a person's choice, not the listing's.
INSERT INTO suppliers.supplier_groups (supplier_id, upstream_ref, name, ratio, facts, last_seen_at)
VALUES (@supplier_id, @upstream_ref, @name, @ratio::numeric, @facts::jsonb, @seen_at)
RETURNING id;

-- name: UpdateProjectedGroup :exec
UPDATE suppliers.supplier_groups
SET name = @name, ratio = @ratio::numeric, facts = @facts::jsonb, present = true, last_seen_at = @seen_at
WHERE id = @id;

-- name: SetProjectedGroupAbsent :exec
UPDATE suppliers.supplier_groups SET present = false WHERE id = @id AND present;

-- name: ListProjectedModels :many
SELECT supplier_group_id, upstream_name, model_id, formats, present
FROM suppliers.supplier_models
WHERE supplier_id = @supplier_id;

-- name: UpsertProjectedModel :exec
-- first_seen_at is left as the insert of the row set it: it is how long a name has waited to be mapped.
INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, model_id, formats)
VALUES (@supplier_id, @supplier_group_id, @upstream_name, @model_id, @formats)
ON CONFLICT (supplier_group_id, upstream_name) DO UPDATE
SET model_id = excluded.model_id, formats = excluded.formats, present = true;

-- name: SetProjectedModelAbsent :exec
UPDATE suppliers.supplier_models SET present = false
WHERE supplier_group_id = @supplier_group_id AND upstream_name = @upstream_name AND present;

-- name: ListProjectedAliases :many
SELECT supplier_group_id, upstream_name, model_id FROM suppliers.supplier_aliases WHERE supplier_id = @supplier_id;

-- name: ListProjectedEndpoints :many
-- The endpoints as they stand, with the fingerprint of the cost schedule in force: whether a new plan is a new row.
SELECT e.id, e.supplier_group_id, e.model_id, e.upstream_name, e.formats, e.cost_state, e.cost_state_reason,
    COALESCE(cs.fingerprint, '') AS fingerprint
FROM suppliers.endpoints e
LEFT JOIN suppliers.cost_schedules cs ON cs.id = e.cost_schedule_id
WHERE e.supplier_id = @supplier_id
ORDER BY e.id;

-- name: InsertProjectedEndpoint :one
-- enabled is left at its default: it is a person's switch, and the projection only ever sets it when it makes the row.
INSERT INTO suppliers.endpoints (supplier_id, supplier_group_id, model_id, upstream_name, formats)
VALUES (@supplier_id, @supplier_group_id, @model_id, @upstream_name, @formats)
RETURNING id;

-- name: UpdateProjectedEndpoint :exec
UPDATE suppliers.endpoints SET upstream_name = @upstream_name, formats = @formats WHERE id = @id;

-- name: EnsureCostSchedule :one
-- A cost schedule is append-only and an endpoint has one row per fingerprint: a plan the endpoint already has a row for
-- is that row, so a listing read twice adds none.
WITH made AS (
    INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
    VALUES (@supplier_id, @endpoint_id, @native_unit, @schedule::jsonb, @basis, @fingerprint)
    ON CONFLICT (endpoint_id, fingerprint) DO NOTHING
    RETURNING id
)
SELECT id FROM made
UNION ALL
SELECT id FROM suppliers.cost_schedules WHERE endpoint_id = @endpoint_id AND fingerprint = @fingerprint
LIMIT 1;

-- name: SetProjectedEndpointCost :exec
-- Puts the endpoint's cost schedule in force. The caller decides when: a plan that is already in force is only written
-- back over a reason the projection itself wrote, so a verdict of reconciliation (an estimate the supplier's charges
-- exceeded) stands until a new estimate or a person lifts it.
UPDATE suppliers.endpoints
SET cost_state = 'known', cost_schedule_id = @cost_schedule_id, cost_state_reason = NULL
WHERE id = @id;

-- name: UnsetProjectedEndpointCost :exec
-- The endpoint has no cost to route by: it is not routable, and the row and its enabled switch stay for when it has.
UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = @reason
WHERE id = @id AND cost_state = 'known';
