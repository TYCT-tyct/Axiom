-- +goose Up

-- What syncing keeps in the database (internal/suppliers/README.md「同步」「持久化」): the state of each supplier's
-- engine with the lease that lets one node at a time write it, what each step told, the cost plans it worked out, and
-- the rate limits that nodes sharing an address share. All of it is the platform's: syncing runs in the platform scope.

-- One row per supplier, made when a node first takes the lease. version is the engine's fence: a step is stored only
-- over the version it read. lease_fence is the lease's: it rises with every take-over and never falls, so a holder
-- whose lease was taken from it, however late its write, names a number the row no longer has. The clock decides only
-- when a lease may be taken over, never whether a write is let in. state is the engine's State as jsonb, in the shape
-- format names.
CREATE TABLE suppliers.sync_states (
    supplier_id uuid PRIMARY KEY REFERENCES suppliers.suppliers,
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    format smallint NOT NULL DEFAULT 1 CHECK (format >= 1),
    state jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(state) = 'object'),
    -- When the supplier next needs a round, worked out from state by the commit that wrote it.
    next_due timestamptz NOT NULL,
    lease_holder text CHECK (lease_holder <> ''),
    lease_fence bigint NOT NULL DEFAULT 0 CHECK (lease_fence >= 0),
    lease_until timestamptz,
    CONSTRAINT lease_whole CHECK ((lease_holder IS NULL) = (lease_until IS NULL))
);
CREATE INDEX sync_states_next_due ON suppliers.sync_states (next_due);

-- What each step of the engine told, one row per event, written with the state that caused it and never changed: the
-- events are delivered from here, and a step that did not commit left none.
CREATE TABLE suppliers.sync_events (
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    version bigint NOT NULL CHECK (version >= 1),
    seq integer NOT NULL CHECK (seq >= 0),
    PRIMARY KEY (supplier_id, version, seq),
    kind text NOT NULL CHECK (kind <> ''),
    subject text NOT NULL,
    old_value text NOT NULL,
    new_value text NOT NULL,
    why text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The versions of the cost plans the engine worked out, in the order it worked them out; the latest of an endpoint is
-- the one in force. An endpoint here is "group/model" as the supplier names them: they become cost_schedules once the
-- models are mapped to the catalog and endpoints exist.
CREATE TABLE suppliers.sync_plans (
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    version bigint NOT NULL CHECK (version >= 1),
    seq integer NOT NULL CHECK (seq >= 0),
    PRIMARY KEY (supplier_id, version, seq),
    endpoint text NOT NULL CHECK (endpoint <> ''),
    fingerprint text NOT NULL CHECK (fingerprint <> ''),
    schedule jsonb NOT NULL CHECK (jsonb_typeof(schedule) = 'object'),
    basis text NOT NULL CHECK (basis IN ('published', 'confirmed'))
);

-- The supplier's rate limits as the nodes together have used them. scope says whom the supplier counts for: an address
-- ("ip:" and the origin and the exit) or an account ("account:" and the supplier's id).
CREATE TABLE suppliers.rate_buckets (
    scope text NOT NULL CHECK (scope <> ''),
    name text NOT NULL CHECK (name <> ''),
    PRIMARY KEY (scope, name),
    window_start timestamptz,
    used integer NOT NULL DEFAULT 0 CHECK (used >= 0),
    blocked_until timestamptz
);

ALTER TABLE suppliers.sync_states ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.sync_states USING (platform.in_platform_scope());
ALTER TABLE suppliers.sync_events ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.sync_events USING (platform.in_platform_scope());
ALTER TABLE suppliers.sync_plans ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.sync_plans USING (platform.in_platform_scope());
ALTER TABLE suppliers.rate_buckets ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.rate_buckets USING (platform.in_platform_scope());

-- No DELETE: events and plans are history, and the rest is one row per supplier or bucket for as long as it exists.
GRANT SELECT, INSERT ON suppliers.sync_states, suppliers.sync_events, suppliers.sync_plans, suppliers.rate_buckets
    TO axiom_app;
GRANT UPDATE (version, format, state, next_due, lease_holder, lease_fence, lease_until) ON suppliers.sync_states
    TO axiom_app;
GRANT UPDATE (window_start, used, blocked_until) ON suppliers.rate_buckets TO axiom_app;
