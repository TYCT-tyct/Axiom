-- name: AuditReplicaState :many
-- How far the copy to target has got, and the last link of the batch planned and not yet confirmed (0 for none); no
-- row before the first plan.
SELECT replicated_seq, coalesce(pending_last, 0)::bigint AS pending_last FROM operations.audit_replica WHERE target = @target;

-- name: PlanAuditReplica :execrows
-- Records the batch from_seq + 1 to last_seq for target, from the state the run read: nothing planned and the copy at
-- from_seq (no row is from_seq 0). 0 rows when another run planned or moved first.
INSERT INTO operations.audit_replica (target, replicated_seq, pending_last) VALUES (@target, @from_seq, @last_seq::bigint)
ON CONFLICT (target) DO UPDATE SET pending_last = EXCLUDED.pending_last
WHERE operations.audit_replica.replicated_seq = @from_seq AND operations.audit_replica.pending_last IS NULL;

-- name: AdvanceAuditReplica :execrows
-- Confirms the planned batch to last_seq, whose object is object_key: 0 rows when another run confirmed it first.
UPDATE operations.audit_replica SET replicated_seq = @last_seq, object_key = @object_key::text, pending_last = NULL,
    replicated_at = now()
WHERE target = @target AND replicated_seq = @from_seq AND pending_last = @last_seq;

-- name: AuditReplicaLag :one
-- The chain's head, how far the copy to target has got, and when the oldest link not yet copied there was chained
-- (none when the copy has the head), by the database's clock.
SELECT now()::timestamptz AS now, coalesce(r.replicated_seq, 0)::bigint AS replicated_seq, r.replicated_at,
    h.seq AS head_seq, c.chained_at AS pending_since
FROM (SELECT coalesce(max(seq), 0)::bigint AS seq FROM operations.audit_chain) h
LEFT JOIN operations.audit_replica r ON r.target = @target
LEFT JOIN operations.audit_chain c ON c.seq = coalesce(r.replicated_seq, 0) + 1;
