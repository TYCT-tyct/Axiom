-- name: InsertOperation :many
-- No row: the credential used the key before, and the caller reads that operation.
INSERT INTO operations.operations (id, credential_id, idempotency_key, request_hash, tool, target_kind, target_id, params, risk, state, reason, run_id)
VALUES (@id, @credential_id, @idempotency_key, @request_hash, @tool, @target_kind, @target_id, @params, @risk, @state, @reason, @run_id)
ON CONFLICT (credential_id, idempotency_key) DO NOTHING
RETURNING id;

-- name: OperationByKey :one
SELECT * FROM operations.operations WHERE credential_id = @credential_id AND idempotency_key = @idempotency_key;

-- name: OperationByID :many
SELECT * FROM operations.operations WHERE id = @id;

-- name: LockOperation :many
SELECT * FROM operations.operations WHERE id = @id FOR UPDATE;

-- name: OperationsOf :many
-- A credential's operations newest first, a page after the cursor's ID, in one state or all.
SELECT * FROM operations.operations
WHERE credential_id = @credential_id AND (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text)
  AND (@after::uuid = '00000000-0000-0000-0000-000000000000' OR id < @after::uuid)
ORDER BY id DESC
LIMIT @page_size;

-- name: OperationsByID :many
SELECT * FROM operations.operations WHERE id = ANY (@ids::uuid[]);

-- name: LockLiveOperations :many
-- The proposals waiting on a person, a credential's or every one's, locked in ID order.
SELECT id FROM operations.operations
WHERE state = 'pending_approval' AND (@every::bool OR credential_id = @credential_id)
ORDER BY id
FOR UPDATE;

-- name: LockDueOperations :many
-- Operations whose approval is past its expiry, locked operation first like every other path; one another sweep or a
-- decision holds is skipped, not waited for. The operation's state is in the condition so a row decided since the
-- snapshot is rechecked on its new version and left out; its approval moves with it. The clock is the statement's, as
-- the decision's is.
SELECT o.id FROM operations.operations o JOIN operations.approvals a ON a.operation_id = o.id
WHERE o.state = 'pending_approval' AND a.state = 'pending' AND a.expires_at <= clock_timestamp()
ORDER BY o.id
LIMIT @batch
FOR UPDATE OF o SKIP LOCKED;

-- name: SetOperationState :one
UPDATE operations.operations SET state = @state, result = @result, error_code = @error_code, updated_at = now() WHERE id = @id
RETURNING updated_at;

-- name: InsertApproval :one
-- It waits @ttl_seconds at most, and never past the end of the UTC day it was asked on.
INSERT INTO operations.approvals (id, operation_id, tier, state, summary, expires_at)
VALUES (@id, @operation_id, 'human', 'pending', @summary,
    least(now() + make_interval(secs => @ttl_seconds::float8), date_trunc('day', now(), 'UTC') + interval '1 day'))
RETURNING *;

-- name: ApprovalByID :many
SELECT * FROM operations.approvals WHERE id = @id;

-- name: ApprovalsOf :many
SELECT * FROM operations.approvals WHERE operation_id = ANY (@operations::uuid[]);

-- name: LockApprovalOf :many
-- The approval of an operation its caller has locked, and whether it is past its expiry by the database's clock as it
-- is now: now() is the transaction's start, and a decision that waited for the lock past the expiry must see it.
SELECT sqlc.embed(a), (a.expires_at <= clock_timestamp())::bool AS due FROM operations.approvals a WHERE a.operation_id = @operation_id FOR UPDATE;

-- name: Approvals :many
-- Every approval newest first, a page after the cursor's ID, in one state or all: the owner's queue.
SELECT * FROM operations.approvals
WHERE (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text)
  AND (@after::uuid = '00000000-0000-0000-0000-000000000000' OR id < @after::uuid)
ORDER BY id DESC
LIMIT @page_size;

-- name: DecideApproval :one
UPDATE operations.approvals SET state = @state, decided_at = now(), decided_by = @decided_by, decision_reason = @decision_reason
WHERE id = @id
RETURNING decided_at;
