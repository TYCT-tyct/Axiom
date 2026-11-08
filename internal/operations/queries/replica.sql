-- name: AuditReplicaState :many
-- How far the copy to target has got; no row before its first object.
SELECT replicated_seq, object_key, replicated_at FROM operations.audit_replica WHERE target = @target;

-- name: AdvanceAuditReplica :execrows
-- Moves the copy to target from from_seq, where the run read it, to last_seq: 0 rows when another run moved it first.
-- from_seq 0 is no copy yet, which only the first insert matches, since replicated_seq is at least 1.
INSERT INTO operations.audit_replica (target, replicated_seq, object_key) VALUES (@target, @last_seq, @object_key)
ON CONFLICT (target) DO UPDATE SET replicated_seq = EXCLUDED.replicated_seq, object_key = EXCLUDED.object_key,
    replicated_at = now()
WHERE operations.audit_replica.replicated_seq = @from_seq;

-- name: AuditReplicaLag :one
-- The chain's head, how far the copy to target has got, and when the oldest link not yet copied there was chained
-- (none when the copy has the head), by the database's clock.
SELECT now()::timestamptz AS now, coalesce(r.replicated_seq, 0)::bigint AS replicated_seq, r.replicated_at,
    h.seq AS head_seq, c.chained_at AS pending_since
FROM (SELECT coalesce(max(seq), 0)::bigint AS seq FROM operations.audit_chain) h
LEFT JOIN operations.audit_replica r ON r.target = @target
LEFT JOIN operations.audit_chain c ON c.seq = coalesce(r.replicated_seq, 0) + 1;
