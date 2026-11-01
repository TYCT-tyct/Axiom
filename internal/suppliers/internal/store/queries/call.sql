-- name: InsertCostSchedule :one
INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
VALUES (@supplier_id, @endpoint_id, @native_unit, @schedule, @basis, @fingerprint)
RETURNING id;

-- name: SetEndpointCost :exec
UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = @cost_schedule_id, cost_state_reason = NULL
WHERE id = @id;

-- name: ListRoutable :many
-- Every credential that can serve an endpoint: the supplier in use with a balance above zero and a unit value
-- confirmed, the group chosen, the endpoint enabled with a cost known, and a key that is bound. A supplier whose balance
-- was never read is out, as one without a confirmed unit value is. The unit value is the latest confirmed for this
-- currency and native unit. Expiry and cooldown are judged by the clock when a credential is picked. upstream_name is the model's name at the supplier, as a
-- call must send it; model_id is the catalog model the snapshot of routing names it by.
SELECT e.id AS endpoint_id, e.upstream_name, e.model_id, e.formats, e.supplier_id, g.codename, g.pool_low, s.kind, s.origin, s.native_unit, s.owner_scope, s.site_id, s.account_id,
    s.balance_native::text AS balance_native, s.balance_observed_at, s.settlement_currency, v.id AS unit_value_id, v.value::text AS unit_value,
    c.id AS credential_id, c.ciphertext, c.nonce, c.wrapped_dek, c.key_version, c.max_concurrency, c.expires_at,
    c.requests_per_minute,
    cs.id AS cost_schedule_id, cs.schedule
FROM suppliers.endpoints e
JOIN suppliers.suppliers s ON s.id = e.supplier_id
JOIN LATERAL (SELECT u.id, u.value FROM suppliers.unit_values u WHERE u.supplier_id = s.id AND u.currency = s.settlement_currency AND u.native_unit = s.native_unit ORDER BY u.id DESC LIMIT 1) v ON true
JOIN suppliers.supplier_groups g ON g.id = e.supplier_group_id AND g.selected AND g.present
JOIN suppliers.credentials c ON c.supplier_group_id = e.supplier_group_id AND c.status = 'active' AND c.ciphertext IS NOT NULL
    AND (c.allowed_models IS NULL OR e.upstream_name = ANY(c.allowed_models))
JOIN suppliers.cost_schedules cs ON cs.id = e.cost_schedule_id
WHERE e.enabled AND e.cost_state = 'known' AND s.status IN ('active', 'low_balance') AND s.balance_native > 0
ORDER BY e.id, c.id;

-- name: InsertEndedRequest :exec
INSERT INTO suppliers.ended_requests (request_id, site_id, account_id, ended_at)
VALUES (@request_id, @site_id, @account_id, @ended_at)
ON CONFLICT (request_id) DO NOTHING;

-- name: EnabledSupplierCurrencies :many
-- Include enabled suppliers without a routable endpoint: configuration problems must be visible before a call.
SELECT id, settlement_currency FROM suppliers.suppliers WHERE status IN ('active', 'low_balance');

-- name: InsertLostRequest :exec
INSERT INTO suppliers.lost_requests (request_id, site_id, account_id, route_endpoint_ids)
VALUES (@request_id, @site_id, @account_id, @route_endpoint_ids)
ON CONFLICT (request_id) DO NOTHING;

-- The resolution is the lost request's own tenant, read from it rather than given again: a second copy could disagree.
-- Resolving again changes nothing. It returns how many lost requests the id names, so that a request never recorded as
-- lost, which has nothing to resolve and writes no row, is told from one resolved already.
-- name: InsertLostRequestResolution :one
WITH lost AS (
    SELECT l.request_id, l.site_id, l.account_id FROM suppliers.lost_requests l WHERE l.request_id = @request_id
), resolved AS (
    INSERT INTO suppliers.lost_request_resolutions (request_id, site_id, account_id)
    SELECT request_id, site_id, account_id FROM lost
    ON CONFLICT (request_id) DO NOTHING
)
SELECT count(*)::int AS named FROM lost;

-- name: MatchCharges :many
-- Matches every charge not yet matched to the one attempt that has its key. A key two attempts share matches neither:
-- which one it belongs to is not known. The attempt is claimed first, and a charge is attributed only to an attempt
-- this statement claimed, so two matchers at once cannot give one attempt two charges or one charge two attempts. A
-- charge held or assigned before is moved to the attempt, and the old attribution is cleared in the same update. A test
-- generation's row is matched like any other, so the supplier's charge for a probe is attributed and never held for a
-- person; its flag is returned, because the estimate of a probe is an upper bound and judging the endpoint by it would
-- say nothing (H-I9).
WITH pairs AS (
    SELECT cr.id AS charge_id, ac.request_id, ac.attempt_seq, ac.hop
    FROM suppliers.charge_records cr
    JOIN suppliers.attempt_costs ac ON ac.supplier_id = cr.supplier_id AND ac.charge_ref = cr.charge_ref
    WHERE cr.state <> 'matched' AND cr.funding = 'wallet' AND ac.charge_record_id IS NULL
        AND (SELECT count(*) FROM suppliers.attempt_costs x
             WHERE x.supplier_id = cr.supplier_id AND x.charge_ref = cr.charge_ref) = 1
    ORDER BY cr.id
    LIMIT @batch::int
), claimed AS (
    UPDATE suppliers.attempt_costs ac SET charge_record_id = p.charge_id, state = 'matched'
    FROM pairs p
    WHERE ac.request_id = p.request_id AND ac.attempt_seq = p.attempt_seq AND ac.hop = p.hop AND ac.charge_record_id IS NULL
    RETURNING p.charge_id, ac.request_id, ac.attempt_seq, ac.hop, ac.endpoint_id, ac.cost_schedule_id, ac.estimated_native, ac.cost_flag, ac.test
)
UPDATE suppliers.charge_records cr SET state = 'matched', attribution = 'attempt', attempt_request_id = claimed.request_id,
    attempt_seq = claimed.attempt_seq, attempt_hop = claimed.hop, lost_request_id = NULL, revision = cr.revision + 1
FROM claimed
WHERE cr.id = claimed.charge_id AND cr.state <> 'matched'
RETURNING cr.id AS charge_id, cr.revision, cr.amount_native AS actual_native, claimed.estimated_native, claimed.cost_flag,
    claimed.endpoint_id, claimed.cost_schedule_id, claimed.request_id, claimed.attempt_seq, claimed.test,
    (SELECT s.native_unit FROM suppliers.suppliers s WHERE s.id = cr.supplier_id) AS native_unit;

-- name: HoldOverdueCharges :many
-- At most one batch of the wallet charges that waited past the grace, oldest first; the rest are for the next batch.
WITH due AS (
    SELECT c.id FROM suppliers.charge_records c
    WHERE c.state = 'new' AND c.funding = 'wallet' AND c.occurred_at < @before
    ORDER BY c.occurred_at, c.id
    LIMIT @batch::int
    FOR UPDATE SKIP LOCKED
)
UPDATE suppliers.charge_records cr SET state = 'held', revision = cr.revision + 1
FROM due
WHERE cr.id = due.id
RETURNING cr.id AS charge_id, cr.revision, cr.amount_native AS actual_native;

-- name: PauseSupplierForFunding :execrows
-- A charge that is not on the wallet (a subscription) is a cost this system does not model: the supplier stops being
-- routed to until a person looks at it.
UPDATE suppliers.suppliers SET status = 'needs_attention', status_reason = 'funding_not_wallet', config_gen = config_gen + 1
WHERE id = @id AND status IN ('connecting', 'active', 'low_balance');

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
WHERE id = @id AND cost_state = 'known' AND cost_schedule_id = @cost_schedule_id;

-- name: ListUnmatchedAttempts :many
-- One batch of the attempts waiting past the grace, in a fixed order, after the position of the last one given before.
-- A test generation is not one: whether the supplier bills a probe at all is nothing a person can chase, and its cost is
-- the platform's own (H-I9).
SELECT request_id, attempt_seq, hop, charge_ref, finished_at FROM suppliers.attempt_costs
WHERE state = 'pending' AND charge_ref IS NOT NULL AND finished_at < @before AND NOT test
    AND (finished_at, request_id, attempt_seq, hop) > (@after_at::timestamptz, @after_request::uuid, @after_seq::smallint, @after_hop::smallint)
ORDER BY finished_at, request_id, attempt_seq, hop
LIMIT @batch::int;

-- name: ListPriceCosts :many
-- Costs depend on configured eligibility, never on a node's adapter or key availability.
SELECT e.id AS endpoint_id, e.model_id, s.settlement_currency,
 v.id AS unit_value_id, v.value::text AS unit_value, cs.schedule
FROM suppliers.endpoints e
JOIN suppliers.suppliers s ON s.id=e.supplier_id
JOIN suppliers.supplier_groups g ON g.id=e.supplier_group_id AND g.selected AND g.present
JOIN LATERAL (SELECT u.id,u.value FROM suppliers.unit_values u WHERE u.supplier_id=s.id AND u.currency=s.settlement_currency AND u.native_unit=s.native_unit ORDER BY u.id DESC LIMIT 1) v ON true
JOIN suppliers.cost_schedules cs ON cs.id=e.cost_schedule_id
WHERE e.enabled AND e.cost_state='known' AND s.status IN ('active','low_balance') AND s.balance_native>0
AND EXISTS (SELECT 1 FROM suppliers.credentials c WHERE c.supplier_group_id=e.supplier_group_id AND c.status='active' AND c.ciphertext IS NOT NULL
    AND (c.allowed_models IS NULL OR e.upstream_name = ANY(c.allowed_models)))
ORDER BY e.id;

-- name: ChargeEvents :many
-- The events billing has not consumed yet, with the charge's stored usage and the event's attribution. A record that
-- is not attributed to an attempt has nothing for billing to correct, and the join drops charges that were reattributed
-- away. The cursor is (created_at, id), so an event is never read twice and a late one is still read.
SELECT e.id, e.charge_record_id, e.revision, e.created_at, e.payload, cr.usage,
    cr.attempt_request_id, coalesce(cr.attempt_seq, 0)::smallint AS attempt_seq
FROM suppliers.charge_events e
JOIN suppliers.charge_records cr ON cr.id = e.charge_record_id
WHERE (e.created_at, e.id) > (@after_at::timestamptz, @after_id::uuid)
    AND cr.attribution = 'attempt' AND cr.attempt_request_id IS NOT NULL
ORDER BY e.created_at, e.id
LIMIT @max;

-- name: GetChargeEvent :one
-- One event by id, for a correction that was not ready when it was first read. The same attribution rule applies.
SELECT e.id, e.charge_record_id, e.revision, e.created_at, e.payload, cr.usage,
    cr.attempt_request_id, coalesce(cr.attempt_seq, 0)::smallint AS attempt_seq
FROM suppliers.charge_events e
JOIN suppliers.charge_records cr ON cr.id = e.charge_record_id
WHERE e.id = @id AND cr.attribution = 'attempt' AND cr.attempt_request_id IS NOT NULL;

-- name: LockChargeEvents :exec
-- Charge events and their consumer share this transaction lock. A producer keeps it from before inserting the event
-- until commit, and the consumer takes it before reading its cursor, so an event cannot commit behind a cursor it
-- would be older than. The event's timestamp is the insertion's clock_timestamp, after the lock, not now(), the
-- transaction's start. A test holds one transaction's lock while another tries to write.
SELECT pg_advisory_xact_lock(hashtext('axiom charge events'));
