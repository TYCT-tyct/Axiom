-- What the admin's model library reads of a supplier (#948): its names sorted into piles, and an endpoint's cost.

-- name: ListSupplierNames :many
-- Every name the supplier's groups list now, by group and name, with the model the projection mapped it to and the
-- endpoint made for the name, when there is one, with its cost state; and the group's ref the sync knows it by.
SELECT m.supplier_group_id, m.upstream_name, m.formats, m.first_seen_at, m.model_id, e.id AS endpoint_id,
    e.cost_state AS endpoint_cost_state, e.cost_state_reason AS endpoint_cost_reason, g.upstream_ref
FROM suppliers.supplier_models m
JOIN suppliers.supplier_groups g ON g.id = m.supplier_group_id
LEFT JOIN suppliers.endpoints e
    ON e.supplier_group_id = m.supplier_group_id AND e.model_id = m.model_id AND e.upstream_name = m.upstream_name
WHERE m.supplier_id = @supplier_id AND m.present AND g.present
ORDER BY m.supplier_group_id, m.upstream_name;

-- name: GetEndpointPlan :many
-- An endpoint of the supplier with the cost plan in force, none when its cost is not known.
SELECT e.id, e.model_id, e.cost_state, cs.id AS plan_id, cs.schedule, cs.basis, COALESCE(cs.derivation::text, '')::text AS derivation
FROM suppliers.endpoints e
LEFT JOIN suppliers.cost_schedules cs ON cs.id = e.cost_schedule_id
WHERE e.id = @id AND e.supplier_id = @supplier_id;
