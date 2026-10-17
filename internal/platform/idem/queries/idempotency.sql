-- name: ClaimKey :execrows
-- Claims the key for the request, or reports that it was claimed before: a concurrent claim waits here until the first
-- transaction commits or rolls back.
INSERT INTO platform.idempotency (key, actor, operation, fingerprint)
VALUES (@key, @actor, @operation, @fingerprint)
ON CONFLICT (key) DO NOTHING;

-- name: FinishKey :exec
UPDATE platform.idempotency SET response = @response WHERE key = @key AND response IS NULL;

-- name: ClaimedKey :many
SELECT actor, operation, fingerprint, response FROM platform.idempotency WHERE key = @key;
