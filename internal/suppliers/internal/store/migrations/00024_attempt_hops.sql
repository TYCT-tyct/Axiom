-- +goose Up

-- Rolling upgrade keeps the old write contract: attempt_costs remains the one-row-per-attempt table old nodes insert
-- with ON CONFLICT (request_id, attempt_seq). The detail table is additive; a new node writes both the old aggregate row
-- and one detail row per supplier request, so an old node can still settle while the new one reconciles each hop.
--
-- charge_records keeps its foreign key to attempt_costs (the aggregate row exists for every attempt). Its uniqueness
-- gains the hop so two charges of one gateway attempt can be attributed separately; old rows default to hop 0.
ALTER TABLE suppliers.charge_records ADD COLUMN attempt_hop smallint NOT NULL DEFAULT 0;
ALTER TABLE suppliers.charge_records ADD CONSTRAINT charge_records_attempt_hop_check CHECK (attempt_hop BETWEEN 0 AND 255);
GRANT UPDATE (attempt_hop) ON suppliers.charge_records TO axiom_app;

ALTER TABLE suppliers.charge_records DROP CONSTRAINT IF EXISTS charge_records_attempt_request_id_attempt_seq_key;
ALTER TABLE suppliers.charge_records ADD CONSTRAINT charge_records_attempt_request_id_attempt_seq_hop_key
    UNIQUE (attempt_request_id, attempt_seq, attempt_hop);

CREATE TABLE suppliers.attempt_cost_hops (
    request_id uuid NOT NULL,
    attempt_seq smallint NOT NULL CHECK (attempt_seq BETWEEN 1 AND 3),
    hop smallint NOT NULL CHECK (hop BETWEEN 0 AND 255),
    PRIMARY KEY (request_id, attempt_seq, hop),
    site_id uuid,
    account_id uuid,
    CONSTRAINT tenant_whole CHECK ((site_id IS NULL) = (account_id IS NULL)),
    supplier_id uuid NOT NULL,
    credential_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    cost_schedule_id uuid NOT NULL,
    FOREIGN KEY (credential_id, supplier_id) REFERENCES suppliers.credentials (id, supplier_id),
    FOREIGN KEY (endpoint_id, supplier_id) REFERENCES suppliers.endpoints (id, supplier_id),
    FOREIGN KEY (cost_schedule_id, endpoint_id) REFERENCES suppliers.cost_schedules (id, endpoint_id),
    charge_ref text CHECK (charge_ref <> ''),
    usage jsonb NOT NULL CHECK (jsonb_typeof(usage) = 'object'),
    priced_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    cost_flag text NOT NULL CHECK (cost_flag IN ('complete', 'unpriced', 'no_usage')),
    estimated_native numeric NOT NULL CHECK (estimated_native >= 0 AND estimated_native NOT IN ('NaN', 'Infinity')),
    charge_record_id uuid UNIQUE,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'matched')),
    unit_value_id uuid,
    exchange_rate_id uuid,
    conversion_from text,
    conversion_to text,
    conversion_state text,
    test boolean NOT NULL DEFAULT false,
    CONSTRAINT matched_has_charge CHECK ((state = 'matched') = (charge_record_id IS NOT NULL)),
    CONSTRAINT conversion_state_check CHECK (conversion_state IN (
        'same_currency', 'converted', 'missing_rate', 'overflow', 'unpriced', 'not_executed'))
);
CREATE INDEX attempt_cost_hops_charge_ref ON suppliers.attempt_cost_hops (supplier_id, charge_ref)
    WHERE charge_ref IS NOT NULL;
CREATE INDEX attempt_cost_hops_pending ON suppliers.attempt_cost_hops (finished_at, request_id, attempt_seq, hop)
    WHERE state = 'pending' AND charge_ref IS NOT NULL;

ALTER TABLE suppliers.attempt_cost_hops ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON suppliers.attempt_cost_hops
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );
GRANT SELECT, INSERT ON suppliers.attempt_cost_hops TO axiom_app;
GRANT UPDATE (charge_record_id, state) ON suppliers.attempt_cost_hops TO axiom_app;
