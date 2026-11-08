-- +goose Up

-- How far the audit chain has been copied to the bucket outside the deployment (docs/domains/operations.md「审计」):
-- every link up to replicated_seq is in an object there, the last of which is object_key. One row; none before the
-- first copy. The copy moves it forward only from where it read it, so two runs never both claim one range.
CREATE TABLE operations.audit_replica (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    replicated_seq bigint NOT NULL CHECK (replicated_seq >= 1),
    object_key text NOT NULL CHECK (char_length(object_key) BETWEEN 1 AND 1024),
    replicated_at timestamptz NOT NULL DEFAULT now()
);

-- The owner's daily digest of one UTC day, made once every transaction that could still write into the day has ended:
-- what the agent did, the money against the caps, the approvals, the refused calls, and how far the chain and its copy
-- had got. Each is announced as an incident of its own, keyed by the day, which closed_at records closing a day later.
-- The body is kept as written; the application can neither change nor remove one.
CREATE TABLE operations.audit_digests (
    day date PRIMARY KEY,
    body jsonb NOT NULL CHECK (octet_length(body::text) <= 65536),
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz
);

ALTER TABLE operations.audit_replica ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.audit_replica FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.audit_replica FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.audit_replica FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT (replicated_seq, object_key), UPDATE (replicated_seq, object_key, replicated_at)
    ON operations.audit_replica TO axiom_app;

ALTER TABLE operations.audit_digests ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.audit_digests FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.audit_digests FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.audit_digests FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT (day, body), UPDATE (closed_at) ON operations.audit_digests TO axiom_app;

-- +goose Down
DROP TABLE operations.audit_digests;
DROP TABLE operations.audit_replica;
