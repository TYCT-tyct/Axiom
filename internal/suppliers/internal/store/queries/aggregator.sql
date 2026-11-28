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
SELECT e.id, e.upstream_name, e.model_id,
    coalesce(array_agg(DISTINCT c.call_names ->> e.upstream_name) FILTER (WHERE c.call_names ? e.upstream_name), '{}')::text[] AS call_names
FROM suppliers.endpoints e
LEFT JOIN suppliers.credentials c ON c.supplier_group_id = e.supplier_group_id AND c.status = 'active'
WHERE e.supplier_group_id = @supplier_group_id
GROUP BY e.id, e.upstream_name, e.model_id
ORDER BY e.id;

-- name: CeilingLookups :many
-- The attempts at a ceiling plan that settled without the aggregator's figure, finished before @before, oldest first:
-- each with the key it ran on, which its generation's record is read with, and the line's ratio it ran at. Each key
-- gives at most @per_key of them, so the attempts of a key whose records cannot be read now do not keep the others'
-- out of the batch.
SELECT d.generation_id, d.finished_at, d.credential_id, d.supplier_id, d.estimated_native, d.line_ratio, s.kind, s.origin, c.settings, c.egress_id
FROM (
    SELECT a.generation_id::text AS generation_id, a.finished_at, a.credential_id, a.supplier_id,
        a.estimated_native::text AS estimated_native, a.line_ratio::text AS line_ratio,
        row_number() OVER (PARTITION BY a.credential_id ORDER BY a.finished_at, a.generation_id) AS n
    FROM suppliers.attempt_costs a
    WHERE a.check_state = 'lookup' AND a.finished_at < @before
) d
JOIN suppliers.credentials c ON c.id = d.credential_id
JOIN suppliers.suppliers s ON s.id = d.supplier_id
WHERE d.n <= @per_key::bigint
ORDER BY d.finished_at, d.generation_id
LIMIT @max_rows;

-- name: EndStaleLookups :execrows
-- The attempts whose record no read found within the day (finished before @before): they stay booked at the bound.
UPDATE suppliers.attempt_costs SET check_state = 'done'
WHERE check_state = 'lookup' AND finished_at < @before;

-- name: EndKeyLookups :execrows
-- The attempts of a key that no longer opens, erased or sealed under a key the node does not hold: nothing reads their
-- records, and they stay booked at the bound.
UPDATE suppliers.attempt_costs SET check_state = 'done'
WHERE credential_id = @credential_id AND check_state = 'lookup';

-- name: SettleLookup :execrows
-- Books the aggregator's record of the generation against its attempt, once: the cost the record makes it and the
-- figure, after which the attempt waits to be judged. A second time finds no attempt still to look up and changes nothing.
UPDATE suppliers.attempt_costs
SET actual_native = @actual_native::numeric, reported_cost = @reported_cost::numeric, cost_report = @cost_report, check_state = 'judge'
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
