-- +goose Up

-- The tables of the call path and of reconciliation (internal/suppliers/README.md「表」): cost schedules, the journal of
-- attempts in flight, the cost and reconciliation key of each finished attempt, the supplier's charge records, and the
-- events that tell billing a charge was settled.

ALTER TABLE suppliers.endpoints ADD CONSTRAINT endpoints_id_supplier_key UNIQUE (id, supplier_id);
ALTER TABLE suppliers.credentials ADD CONSTRAINT credentials_id_supplier_key UNIQUE (id, supplier_id);

-- A cost schedule is append-only: a change of price is a new row, and what an attempt was priced with stays readable.
CREATE TABLE suppliers.cost_schedules (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    endpoint_id uuid NOT NULL,
    FOREIGN KEY (endpoint_id, supplier_id) REFERENCES suppliers.endpoints (id, supplier_id),
    -- The unit the schedule's prices are in. The schedule's own currency is XXX, "no currency": its amounts are native.
    native_unit text NOT NULL CHECK (native_unit IN ('quota', 'usd')),
    schedule jsonb NOT NULL CHECK (jsonb_typeof(schedule) = 'object'),
    basis text NOT NULL CHECK (basis IN ('published', 'confirmed')),
    fingerprint text NOT NULL CHECK (fingerprint <> ''),
    effective_from timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, endpoint_id),
    UNIQUE (endpoint_id, fingerprint)
);
ALTER TABLE suppliers.endpoints ADD CONSTRAINT endpoints_cost_schedule_fkey
    FOREIGN KEY (cost_schedule_id, id) REFERENCES suppliers.cost_schedules (id, endpoint_id);

-- The cost and reconciliation key of each finished attempt, written by billing's settlement in its own transaction, so
-- it commits or fails with the cost billing records. The application may insert, and only set the two columns that
-- say which charge an attempt was matched with.
CREATE TABLE suppliers.attempt_costs (
    request_id uuid NOT NULL,
    attempt_seq smallint NOT NULL CHECK (attempt_seq BETWEEN 1 AND 3),
    PRIMARY KEY (request_id, attempt_seq),
    -- The tenant of the request, which is the scope billing settles in; NULL for a request of the platform's (a probe).
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
    -- The supplier's id of the request, from a response header; NULL when none came (a timeout).
    charge_ref text CHECK (charge_ref <> ''),
    usage jsonb NOT NULL CHECK (jsonb_typeof(usage) = 'object'),
    priced_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    cost_flag text NOT NULL CHECK (cost_flag IN ('complete', 'unpriced', 'no_usage')),
    estimated_native numeric NOT NULL CHECK (estimated_native >= 0 AND estimated_native NOT IN ('NaN', 'Infinity')),
    charge_record_id uuid UNIQUE,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'matched')),
    CONSTRAINT matched_has_charge CHECK ((state = 'matched') = (charge_record_id IS NOT NULL))
);
CREATE INDEX attempt_costs_charge_ref ON suppliers.attempt_costs (supplier_id, charge_ref) WHERE charge_ref IS NOT NULL;

-- A request whose node was lost before it settled: only the request is known, not its attempts. Inserted when billing
-- gives up on it; when a node comes back with the real attempts, a resolution is inserted. Neither is ever changed.
CREATE TABLE suppliers.lost_requests (
    request_id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    route_endpoint_ids uuid[],
    recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE suppliers.lost_request_resolutions (
    request_id uuid PRIMARY KEY REFERENCES suppliers.lost_requests,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    resolved_at timestamptz NOT NULL DEFAULT now()
);

-- The supplier's own record of one charge. state: new (not matched yet), matched (to an attempt), held (not matched in
-- the grace period: it books nothing and is reported until a person decides), assigned (a person attributed it to a
-- lost request, or to no one). revision rises with every change, so billing can tell a newer verdict from an older.
CREATE TABLE suppliers.charge_records (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    credential_id uuid,
    FOREIGN KEY (credential_id, supplier_id) REFERENCES suppliers.credentials (id, supplier_id),
    charge_ref text NOT NULL CHECK (charge_ref <> ''),
    occurred_at timestamptz NOT NULL,
    amount_native numeric NOT NULL CHECK (amount_native >= 0 AND amount_native NOT IN ('NaN', 'Infinity')),
    usage jsonb NOT NULL CHECK (jsonb_typeof(usage) = 'object'),
    funding text NOT NULL CHECK (funding IN ('wallet', 'other')),
    upstream_model text NOT NULL,
    state text NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'matched', 'held', 'assigned')),
    attribution text CHECK (attribution IN ('attempt', 'lost_request', 'unattributed')),
    attempt_request_id uuid,
    attempt_seq smallint,
    lost_request_id uuid REFERENCES suppliers.lost_requests,
    revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
    UNIQUE (supplier_id, charge_ref),
    UNIQUE (attempt_request_id, attempt_seq),
    FOREIGN KEY (attempt_request_id, attempt_seq) REFERENCES suppliers.attempt_costs (request_id, attempt_seq),
    -- Which of the three ids is set follows the attribution, and a record is reattributed in one statement that clears
    -- the old, so this always holds.
    CONSTRAINT attribution_shape CHECK (
        (attribution IS NULL OR attribution = 'unattributed')
            AND attempt_request_id IS NULL AND attempt_seq IS NULL AND lost_request_id IS NULL
        OR attribution = 'attempt' AND attempt_request_id IS NOT NULL AND attempt_seq IS NOT NULL AND lost_request_id IS NULL
        OR attribution = 'lost_request' AND lost_request_id IS NOT NULL AND attempt_request_id IS NULL AND attempt_seq IS NULL),
    CONSTRAINT state_follows_attribution CHECK (
        state IN ('new', 'held') AND attribution IS NULL
        OR state = 'matched' AND attribution = 'attempt'
        OR state = 'assigned' AND attribution IN ('lost_request', 'unattributed'))
);

-- The events billing reads: one per revision of a charge record, with what it needs to correct the books. Billing takes
-- (charge_record_id, revision) as its idempotency key, so an event is never rewritten.
CREATE TABLE suppliers.charge_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    charge_record_id uuid NOT NULL REFERENCES suppliers.charge_records,
    revision integer NOT NULL CHECK (revision >= 1),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (charge_record_id, revision)
);

-- What attempts in flight have done, written by nodes every second, so that a node that dies leaves what cleanup needs:
-- which attempts reached the supplier, with which key, and whether any content was handed to the client.
CREATE TABLE suppliers.attempt_journal (
    request_id uuid NOT NULL,
    attempt_seq smallint NOT NULL CHECK (attempt_seq BETWEEN 1 AND 3),
    PRIMARY KEY (request_id, attempt_seq),
    site_id uuid,
    account_id uuid,
    supplier_id uuid NOT NULL,
    credential_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    cost_schedule_id uuid NOT NULL,
    priced_at timestamptz NOT NULL,
    charge_ref text CHECK (charge_ref <> ''),
    committed boolean NOT NULL DEFAULT false,
    written_at timestamptz NOT NULL DEFAULT now()
);

-- Row-level security. Rows made in a request's tenant scope follow it; the rest are the platform's.
ALTER TABLE suppliers.cost_schedules ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.cost_schedules
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = cost_schedules.supplier_id));
ALTER TABLE suppliers.attempt_costs ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON suppliers.attempt_costs USING (platform.in_scope(site_id, account_id) OR platform.in_platform_scope());
ALTER TABLE suppliers.lost_requests ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON suppliers.lost_requests USING (platform.in_scope(site_id, account_id));
ALTER TABLE suppliers.lost_request_resolutions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON suppliers.lost_request_resolutions USING (platform.in_scope(site_id, account_id));
ALTER TABLE suppliers.charge_records ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.charge_records USING (platform.in_platform_scope());
ALTER TABLE suppliers.charge_events ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.charge_events USING (platform.in_platform_scope());
ALTER TABLE suppliers.attempt_journal ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.attempt_journal USING (platform.in_platform_scope());

-- Privileges. Schedules, lost requests, resolutions and events are written once. Attempt costs are inserted, then only
-- matched; charge records are inserted, then only moved through their states; the journal is rewritten while a request
-- runs and removed after it is settled.
GRANT SELECT, INSERT ON suppliers.cost_schedules, suppliers.lost_requests, suppliers.lost_request_resolutions,
    suppliers.charge_events TO axiom_app;
GRANT SELECT, INSERT ON suppliers.attempt_costs TO axiom_app;
GRANT UPDATE (charge_record_id, state) ON suppliers.attempt_costs TO axiom_app;
GRANT SELECT, INSERT ON suppliers.charge_records TO axiom_app;
GRANT UPDATE (state, attribution, attempt_request_id, attempt_seq, lost_request_id, revision, credential_id)
    ON suppliers.charge_records TO axiom_app;
GRANT SELECT, INSERT, DELETE ON suppliers.attempt_journal TO axiom_app;
GRANT UPDATE (charge_ref, committed, written_at) ON suppliers.attempt_journal TO axiom_app;
