-- The projection of a sync's state onto the tables routing and the back office read (internal/suppliers/README.md「持久化」).
-- Reads feed the projection's difference decisions. The row writes are the regression oracle for batch.go;
-- production applies only the collected differences through ProjectionWrites, so replay changes nothing.

-- name: ListProjectedGroups :many
-- The pool columns are what the last read of the pool reported: the projection compares them so that a listing read
-- twice writes no row, and a pool whose numbers did not change writes none either.
SELECT id, upstream_ref, name, ratio::text AS ratio, facts::text AS facts, present,
    pool_accounts, pool_failed, pool_usable, pool_headroom, pool_capacity, COALESCE(pool_low, false) AS pool_low, pool_observed_at
FROM suppliers.supplier_groups
WHERE supplier_id = @supplier_id
ORDER BY upstream_ref;

-- name: InsertProjectedGroup :exec
-- A group is made unselected: which groups to use is a person's choice, not the listing's.
INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, facts, last_seen_at)
VALUES (@id, @supplier_id, @upstream_ref, @name, @ratio::numeric, @facts::jsonb, @seen_at);

-- name: UpdateProjectedGroup :exec
UPDATE suppliers.supplier_groups
SET name = @name, ratio = @ratio::numeric, facts = @facts::jsonb, present = true, last_seen_at = @seen_at
WHERE id = @id;

-- name: SetProjectedGroupAbsent :exec
UPDATE suppliers.supplier_groups SET present = false WHERE id = @id AND present;

-- name: SetProjectedGroupPool :exec
-- What the pool's management API last reported of the group. NULL where a read has not reported it, which is how a
-- group that left the pool's answer is cleared.
UPDATE suppliers.supplier_groups
SET pool_accounts = @accounts, pool_failed = @failed, pool_usable = @usable, pool_headroom = @headroom,
    pool_capacity = @capacity, pool_low = @low::boolean, pool_observed_at = @observed_at
WHERE id = @id;

-- name: ClearProjectedGroupPool :exec
UPDATE suppliers.supplier_groups
SET pool_accounts = NULL, pool_failed = NULL, pool_usable = NULL, pool_headroom = NULL, pool_capacity = NULL,
    pool_low = NULL, pool_observed_at = NULL
WHERE id = @id;

-- name: ListGroupPoolCapacities :many
SELECT upstream_ref, pool_accounts, pool_failed, pool_usable, pool_headroom, pool_capacity, COALESCE(pool_low, false) AS pool_low
FROM suppliers.supplier_groups
WHERE supplier_id = @supplier_id AND pool_accounts IS NOT NULL
ORDER BY upstream_ref;

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
SELECT a.supplier_group_id, a.upstream_name, COALESCE(r.model_id, a.model_id) AS model_id
FROM suppliers.supplier_aliases a
LEFT JOIN LATERAL (SELECT model_id FROM suppliers.supplier_alias_revisions r WHERE r.alias_id = a.id ORDER BY revision DESC LIMIT 1) r ON true
WHERE a.supplier_id = @supplier_id;

-- name: ListProjectedEndpoints :many
-- The endpoints as they stand, with the fingerprint of the cost schedule in force: whether a new plan is a new row.
SELECT e.id, e.supplier_group_id, e.model_id, e.upstream_name, e.formats, e.cost_state, e.cost_state_reason,
    COALESCE(cs.fingerprint, '') AS fingerprint
FROM suppliers.endpoints e
LEFT JOIN suppliers.cost_schedules cs ON cs.id = e.cost_schedule_id
WHERE e.supplier_id = @supplier_id
ORDER BY e.id;

-- name: InsertProjectedEndpoint :exec
-- enabled is left at its default: it is a person's switch, and the projection only ever sets it when it makes the row.
INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
VALUES (@id, @supplier_id, @supplier_group_id, @model_id, @upstream_name, @formats);

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
