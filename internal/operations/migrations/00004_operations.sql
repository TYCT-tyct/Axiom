-- +goose Up

-- The operations the agent asks for through a write of the operations API, and the approvals the ones that need a
-- person wait on (docs/domains/operations.md「运营接口」「风险分级与审批」). One operation acts on one object and is
-- never reused: a retry with the same Idempotency-Key finds it, any other request is a new one. `request_hash` is the
-- SHA-256 of the canonical tool, target and parameters, so the same key with another request is told apart.
-- `params` holds what the operation does, the target's `rev` included; `reason` is the agent's own words, untrusted.
CREATE TABLE operations.operations (
    id uuid PRIMARY KEY,
    credential_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    tool text NOT NULL CHECK (tool ~ '^[a-z][a-z0-9_]{0,63}$'),
    target_kind text NOT NULL CHECK (target_kind ~ '^[a-z][a-z0-9_]{0,63}$'),
    target_id text NOT NULL CHECK (char_length(target_id) BETWEEN 1 AND 128),
    params json NOT NULL CHECK (octet_length(params::text) <= 8192),
    risk text NOT NULL CHECK (risk IN ('routine', 'delisting')),
    state text NOT NULL CHECK (state IN (
        'pending_approval', 'approved', 'executing', 'succeeded', 'failed', 'unknown', 'canceled', 'expired', 'rejected',
        'reverted')),
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1000),
    run_id text CHECK (run_id ~ '^[A-Za-z0-9._:-]{1,64}$'),
    result json CHECK (octet_length(result::text) <= 8192),
    error_code text CHECK (error_code ~ '^[a-z][a-z0-9_]{0,63}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (credential_id, idempotency_key)
);
-- A credential lists its own operations newest first (IDs are UUIDv7).
CREATE INDEX operations_of_credential ON operations.operations (credential_id, id DESC);
-- The stop and a revocation cancel the proposals still waiting on a person.
CREATE INDEX operations_pending ON operations.operations (credential_id) WHERE state = 'pending_approval';

-- An approval binds one operation as it was proposed: the application may not update an operation's tool, target or
-- parameters, so what a person approves is what runs. `summary` is what the server read of the target, never the
-- agent's words. A pending one expires at `expires_at`, at most 30 minutes after it was asked and never past the end
-- of its UTC day.
CREATE TABLE operations.approvals (
    id uuid PRIMARY KEY,
    operation_id uuid NOT NULL UNIQUE REFERENCES operations.operations (id),
    tier text NOT NULL CHECK (tier IN ('human')),
    state text NOT NULL CHECK (state IN ('pending', 'approved', 'rejected', 'expired', 'canceled')),
    summary json NOT NULL CHECK (octet_length(summary::text) <= 8192),
    requested_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    decided_by text CHECK (char_length(decided_by) BETWEEN 1 AND 128),
    decision_reason text CHECK (char_length(decision_reason) BETWEEN 1 AND 1000),
    CHECK (expires_at > requested_at),
    CHECK ((state = 'pending') = (decided_at IS NULL)),
    CHECK ((state = 'pending') = (decided_by IS NULL))
);
-- The owner's queue, newest first, and the sweep of the ones past their expiry.
CREATE INDEX approvals_newest ON operations.approvals (id DESC);
CREATE INDEX approvals_due ON operations.approvals (expires_at) WHERE state = 'pending';

-- A write's entry in the operation log names the operation it made or found. Old nodes write no writes, so leave it
-- empty.
ALTER TABLE operations.operation_log ADD COLUMN operation_id uuid;

-- Platform data: the operations listener and the admin write and read it in the platform scope; no tenant scope sees
-- it. Nothing is deleted: a finished operation stays as the record of what was done.
ALTER TABLE operations.operations ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.operations FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.operations FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.operations FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
ALTER TABLE operations.approvals ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.approvals FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.approvals FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.approvals FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON operations.operations, operations.approvals TO axiom_app;
GRANT UPDATE (state, result, error_code, updated_at) ON operations.operations TO axiom_app;
GRANT UPDATE (state, decided_at, decided_by, decision_reason) ON operations.approvals TO axiom_app;

-- +goose Down
ALTER TABLE operations.operation_log DROP COLUMN operation_id;
DROP TABLE operations.approvals;
DROP TABLE operations.operations;
