-- An aggregator's line, costed at its providers' highest price (docs/domains/suppliers-direct.md「OpenRouter 的成本」).

-- name: NameCeilings :many
-- The highest price of each name of the line that the catalog maps, as last read, and when; none for a name never read
-- or read as unpriced.
SELECT upstream_name, ceiling, ceiling_read_at FROM suppliers.supplier_models
WHERE supplier_group_id = @supplier_group_id AND present AND model_id IS NOT NULL
ORDER BY upstream_name;

-- name: SetNameCeiling :exec
UPDATE suppliers.supplier_models SET ceiling = @ceiling::jsonb, ceiling_read_at = @read_at
WHERE supplier_group_id = @supplier_group_id AND upstream_name = @upstream_name;

-- name: ClearNameCeiling :exec
UPDATE suppliers.supplier_models SET ceiling = NULL, ceiling_read_at = NULL
WHERE supplier_group_id = @supplier_group_id AND upstream_name = @upstream_name;

-- name: EndpointCallNames :many
-- Every endpoint of the line with each name a call to it is sent by: its own, and the one each key in service lists
-- it as (credentials.call_names).
SELECT e.id, e.upstream_name,
    coalesce(array_agg(DISTINCT c.call_names ->> e.upstream_name) FILTER (WHERE c.call_names ? e.upstream_name), '{}')::text[] AS call_names
FROM suppliers.endpoints e
LEFT JOIN suppliers.credentials c ON c.supplier_group_id = e.supplier_group_id AND c.status = 'active'
WHERE e.supplier_group_id = @supplier_group_id
GROUP BY e.id, e.upstream_name
ORDER BY e.id;

-- name: CeilingLookups :many
-- The attempts at a ceiling plan that settled without the aggregator's figure, finished before @before, oldest first:
-- each with the key it ran on, which its generation's record is read with, and the line's ratio.
SELECT a.generation_id::text AS generation_id, a.finished_at, a.credential_id, a.supplier_id, a.estimated_native::text AS estimated_native,
    s.kind, s.origin, c.settings, c.egress_id, g.ratio::text AS line_ratio
FROM suppliers.attempt_costs a
JOIN suppliers.credentials c ON c.id = a.credential_id
JOIN suppliers.suppliers s ON s.id = a.supplier_id
JOIN suppliers.supplier_groups g ON g.id = c.supplier_group_id
WHERE a.check_state = 'lookup' AND a.finished_at < @before
ORDER BY a.finished_at, a.generation_id
LIMIT @max_rows;

-- name: SettleLookup :execrows
-- Books the aggregator's record of the generation against its attempt, once: the cost the record makes it and the
-- figure, after which the attempt waits to be judged. A second time finds no attempt still to look up and changes nothing.
UPDATE suppliers.attempt_costs
SET actual_native = @actual_native::numeric, reported_cost = @reported_cost::numeric, cost_report = @cost_report, check_state = 'judge'
WHERE generation_id = @generation_id AND check_state = 'lookup';

-- name: EndLookup :execrows
-- An attempt whose generation's record never came: it stays booked at the bound.
UPDATE suppliers.attempt_costs SET check_state = 'done'
WHERE generation_id = @generation_id AND check_state = 'lookup';

-- name: CeilingJudgments :many
-- The attempts whose figure is to be judged against their bound, oldest first, locked so two runs judge each once.
SELECT request_id, attempt_seq, endpoint_id, cost_schedule_id, coalesce(reported_cost::text, '')::text AS reported_cost,
    coalesce(bound_cost::text, '')::text AS bound_cost,
    coalesce(cost_report, '')::text AS cost_report, cost_flag
FROM suppliers.attempt_costs
WHERE check_state = 'judge'
ORDER BY finished_at, request_id, attempt_seq
LIMIT @max_rows
FOR UPDATE SKIP LOCKED;

-- name: EndJudgment :exec
UPDATE suppliers.attempt_costs SET check_state = 'done'
WHERE request_id = @request_id AND attempt_seq = @attempt_seq AND check_state = 'judge';

-- name: DelistCeilingEndpoint :execrows
-- Takes an endpoint out of routing for a charge its plan did not bound, while the endpoint is still on that plan: one
-- priced again since is judged by its new plan. The verdict stands over a re-read at the same price
-- (SetProjectedEndpointCost).
UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = 'under_estimated'
WHERE id = @id AND cost_schedule_id = @cost_schedule_id AND cost_state = 'known';
