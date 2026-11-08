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

-- The day a call counts in is the database's: the log's IDs come from the node's clock, and a node running behind
-- would date a call into a day already digested. logged_at is the logging transaction's start, the clock the digest's
-- boundary is read on; old nodes, which do not name it, get it from the default. Calls logged before it are dated by
-- when they ended.
ALTER TABLE operations.operation_log ADD COLUMN logged_at timestamptz;
UPDATE operations.operation_log SET logged_at = ended_at;
ALTER TABLE operations.operation_log ALTER COLUMN logged_at SET DEFAULT now(), ALTER COLUMN logged_at SET NOT NULL;

-- A day's digest reads that day's refused calls, operations and approvals alone, however long the history.
CREATE INDEX operation_log_refused ON operations.operation_log (logged_at) WHERE status BETWEEN 400 AND 499;
CREATE INDEX operations_by_created ON operations.operations (created_at);
CREATE INDEX approvals_by_requested ON operations.approvals (requested_at);

-- When a round of the check of the whole chain last finished, kept while the next round runs (finished_at is cleared
-- when it starts).
ALTER TABLE operations.audit_chain_checks ADD COLUMN last_finished_at timestamptz;
UPDATE operations.audit_chain_checks SET last_finished_at = finished_at;
GRANT UPDATE (last_finished_at) ON operations.audit_chain_checks TO axiom_app;

-- +goose Down
ALTER TABLE operations.audit_chain_checks DROP COLUMN last_finished_at;
DROP INDEX operations.approvals_by_requested;
DROP INDEX operations.operations_by_created;
DROP INDEX operations.operation_log_refused;
ALTER TABLE operations.operation_log DROP COLUMN logged_at;
DROP TABLE operations.audit_digests;
