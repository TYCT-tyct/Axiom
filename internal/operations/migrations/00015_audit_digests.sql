-- +goose Up

-- The owner's daily digest of one UTC day (docs/domains/operations.md「每天的摘要现在的实现」), made once every
-- transaction that could still write into the day has ended: what the agent did, the money against the caps, the
-- approvals, the refused calls, and how far the audit chain had got. Each is announced as an incident of its own, keyed
-- by the day. The body has no size limit: its money part has a row per cap policy, which nothing bounds, and a limit
-- would stop that day and every day after it. Kept as written; the application can neither change nor remove one.
CREATE TABLE operations.audit_digests (
    day date PRIMARY KEY,
    body jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE operations.audit_digests ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.audit_digests FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.audit_digests FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT (day, body) ON operations.audit_digests TO axiom_app;

-- +goose Down
DROP TABLE operations.audit_digests;
