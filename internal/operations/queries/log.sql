-- name: InsertLoggedCall :exec
INSERT INTO operations.operation_log (id, credential_id, run_id, tool, reason, input, status, error_code, request_id, started_at, ended_at)
VALUES (@id, @credential_id, @run_id, @tool, @reason, @input, @status, @error_code, @request_id, @started_at, @ended_at);

-- name: LoggedCalls :many
-- A credential's calls newest first, a page after the cursor's ID.
SELECT id, run_id, tool, reason, input, status, error_code, request_id, started_at, ended_at
FROM operations.operation_log
WHERE credential_id = @credential_id AND (@after::uuid = '00000000-0000-0000-0000-000000000000' OR id < @after::uuid)
ORDER BY id DESC
LIMIT @page_size;
