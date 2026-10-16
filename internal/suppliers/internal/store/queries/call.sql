-- name: InsertCostSchedule :one
INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
VALUES (@supplier_id, @endpoint_id, @native_unit, @schedule, @basis, @fingerprint)
RETURNING id;

-- name: SetEndpointCost :exec
UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = @cost_schedule_id, cost_state_reason = NULL
WHERE id = @id;

-- name: ListRoutable :many
-- Every credential that can serve an endpoint: the supplier in use, the group chosen, the endpoint enabled with a cost
-- known, and a key that is bound. Expiry and cooldown are judged by the clock when a credential is picked.
SELECT e.id AS endpoint_id, e.upstream_name, e.formats, e.supplier_id, s.kind, s.origin, s.native_unit,
    c.id AS credential_id, c.ciphertext, c.nonce, c.wrapped_dek, c.key_version, c.max_concurrency, c.expires_at,
    cs.id AS cost_schedule_id, cs.schedule
FROM suppliers.endpoints e
JOIN suppliers.suppliers s ON s.id = e.supplier_id
JOIN suppliers.supplier_groups g ON g.id = e.supplier_group_id AND g.selected AND g.present
JOIN suppliers.credentials c ON c.supplier_group_id = e.supplier_group_id AND c.status = 'active' AND c.ciphertext IS NOT NULL
JOIN suppliers.cost_schedules cs ON cs.id = e.cost_schedule_id
WHERE e.enabled AND e.cost_state = 'known' AND s.status IN ('active', 'low_balance')
ORDER BY e.id, c.id;

-- name: ListJournal :many
SELECT request_id, attempt_seq, site_id, account_id, supplier_id, credential_id, endpoint_id, cost_schedule_id, priced_at,
    charge_ref, committed
FROM suppliers.attempt_journal WHERE request_id = @request_id ORDER BY attempt_seq;

-- name: PurgeJournal :execrows
DELETE FROM suppliers.attempt_journal WHERE written_at < @before;

-- name: InsertLostRequest :exec
INSERT INTO suppliers.lost_requests (request_id, site_id, account_id, route_endpoint_ids)
VALUES (@request_id, @site_id, @account_id, @route_endpoint_ids)
ON CONFLICT (request_id) DO NOTHING;

-- name: InsertLostRequestResolution :exec
INSERT INTO suppliers.lost_request_resolutions (request_id, site_id, account_id)
VALUES (@request_id, @site_id, @account_id)
ON CONFLICT (request_id) DO NOTHING;

-- name: MatchCharges :many
-- Matches every charge not yet matched to the one attempt that has its key. A key two attempts share matches neither:
-- which one it belongs to is not known. The attempt is claimed first, and a charge is attributed only to an attempt
-- this statement claimed, so two matchers at once cannot give one attempt two charges or one charge two attempts. A
-- charge held or assigned before is moved to the attempt, and the old attribution is cleared in the same update.
WITH pairs AS (
    SELECT cr.id AS charge_id, ac.request_id, ac.attempt_seq
    FROM suppliers.charge_records cr
    JOIN suppliers.attempt_costs ac ON ac.supplier_id = cr.supplier_id AND ac.charge_ref = cr.charge_ref
    WHERE cr.state <> 'matched' AND ac.charge_record_id IS NULL
        AND (SELECT count(*) FROM suppliers.attempt_costs x
             WHERE x.supplier_id = cr.supplier_id AND x.charge_ref = cr.charge_ref) = 1
), claimed AS (
    UPDATE suppliers.attempt_costs ac SET charge_record_id = p.charge_id, state = 'matched'
    FROM pairs p
    WHERE ac.request_id = p.request_id AND ac.attempt_seq = p.attempt_seq AND ac.charge_record_id IS NULL
    RETURNING p.charge_id, ac.request_id, ac.attempt_seq, ac.endpoint_id, ac.estimated_native, ac.cost_flag
)
UPDATE suppliers.charge_records cr SET state = 'matched', attribution = 'attempt', attempt_request_id = claimed.request_id,
    attempt_seq = claimed.attempt_seq, lost_request_id = NULL, revision = cr.revision + 1
FROM claimed
WHERE cr.id = claimed.charge_id AND cr.state <> 'matched'
RETURNING cr.id AS charge_id, cr.revision, cr.amount_native AS actual_native, claimed.estimated_native, claimed.cost_flag,
    claimed.endpoint_id, claimed.request_id, claimed.attempt_seq,
    (SELECT s.native_unit FROM suppliers.suppliers s WHERE s.id = cr.supplier_id) AS native_unit;

-- name: HoldOverdueCharges :many
UPDATE suppliers.charge_records SET state = 'held', revision = revision + 1
WHERE state = 'new' AND occurred_at < @before
RETURNING id AS charge_id, revision, amount_native AS actual_native;

-- name: AssignChargeToLostRequest :many
UPDATE suppliers.charge_records SET state = 'assigned', attribution = 'lost_request', lost_request_id = @lost_request_id,
    revision = revision + 1
WHERE id = @id AND state = 'held'
RETURNING id AS charge_id, revision, amount_native AS actual_native;

-- name: AssignChargeUnattributed :many
UPDATE suppliers.charge_records SET state = 'assigned', attribution = 'unattributed', revision = revision + 1
WHERE id = @id AND state = 'held'
RETURNING id AS charge_id, revision, amount_native AS actual_native;

-- name: InsertChargeEvent :exec
INSERT INTO suppliers.charge_events (charge_record_id, revision, payload)
VALUES (@charge_record_id, @revision, @payload)
ON CONFLICT (charge_record_id, revision) DO NOTHING;

-- name: DelistEndpoint :execrows
UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = 'under_estimated'
WHERE id = @id AND cost_state = 'known';

-- name: ListUnmatchedAttempts :many
SELECT request_id, attempt_seq, charge_ref FROM suppliers.attempt_costs
WHERE state = 'pending' AND charge_ref IS NOT NULL AND finished_at < @before
ORDER BY finished_at, request_id, attempt_seq;
