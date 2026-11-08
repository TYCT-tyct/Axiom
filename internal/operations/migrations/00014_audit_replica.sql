-- +goose Up

-- How far the audit chain has been copied to each bucket outside the deployment (docs/domains/operations.md「审计」),
-- by target, the bucket's endpoint and name and never its credentials: every link up to replicated_seq is in an object
-- in that bucket, the last of which is object_key. A bucket the copy has not planned for has no row, so pointing the
-- deployment at a new bucket starts it from the first link instead of from another bucket's progress.
--
-- pending_last is the batch planned and not yet confirmed, links replicated_seq + 1 to pending_last: it is recorded
-- before its object is written, and the copy finishes that exact batch before it plans another, whatever the chain's
-- head has become. So a retry writes the same key and bytes it wrote before, and a key holding other bytes stops the
-- copy until someone deals with it, instead of being stepped over by a batch named after a later head. Both moves
-- happen only from the state a run read, so two runs never both plan or confirm one batch.
CREATE TABLE operations.audit_replica (
    target text PRIMARY KEY CHECK (char_length(target) BETWEEN 1 AND 2048),
    replicated_seq bigint NOT NULL CHECK (replicated_seq >= 0),
    object_key text CHECK (char_length(object_key) BETWEEN 1 AND 1024),
    pending_last bigint CHECK (pending_last > replicated_seq),
    replicated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE operations.audit_replica ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.audit_replica FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.audit_replica FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.audit_replica FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT (target, replicated_seq, pending_last),
    UPDATE (replicated_seq, object_key, pending_last, replicated_at) ON operations.audit_replica TO axiom_app;

-- +goose Down
DROP TABLE operations.audit_replica;
