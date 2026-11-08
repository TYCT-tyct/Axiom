-- +goose Up

-- How far the audit chain has been copied to each bucket outside the deployment (docs/domains/operations.md「审计」):
-- every link up to replicated_seq is in an object in the bucket named by target (its endpoint and bucket, never its
-- credentials), the last of which is object_key. A bucket the copy has not written to has no row, so pointing the
-- deployment at a new bucket starts it from the first link instead of from another bucket's progress. The copy moves
-- a row forward only from where it read it, so two runs never both claim one range.
CREATE TABLE operations.audit_replica (
    target text PRIMARY KEY CHECK (char_length(target) BETWEEN 1 AND 2048),
    replicated_seq bigint NOT NULL CHECK (replicated_seq >= 1),
    object_key text NOT NULL CHECK (char_length(object_key) BETWEEN 1 AND 1024),
    replicated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE operations.audit_replica ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.audit_replica FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.audit_replica FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.audit_replica FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT (target, replicated_seq, object_key), UPDATE (replicated_seq, object_key, replicated_at)
    ON operations.audit_replica TO axiom_app;

-- +goose Down
DROP TABLE operations.audit_replica;
