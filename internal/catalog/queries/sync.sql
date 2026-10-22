-- name: GetSyncState :one
SELECT last_success_at, failing_since, failures, pending FROM catalog.sync_state;

-- name: RecordSyncSuccess :exec
UPDATE catalog.sync_state
SET last_success_at = sqlc.arg(last_success_at)::timestamptz, failing_since = NULL, failures = 0, pending = sqlc.arg(pending);

-- name: RecordSyncFailure :exec
UPDATE catalog.sync_state
SET failing_since = COALESCE(failing_since, sqlc.arg(failing_since)::timestamptz), failures = failures + 1;
