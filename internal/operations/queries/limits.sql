-- name: AgentLimits :many
SELECT * FROM operations.agent_limits;

-- name: LockAgentLimits :many
SELECT * FROM operations.agent_limits FOR UPDATE;

-- name: InsertAgentLimits :many
-- The lock above holds no row before the first set, so two first sets race here; the one that finds the row made is
-- answered as a stale revision.
INSERT INTO operations.agent_limits (alerts_per_hour, requests_per_minute, money_writes_per_minute, money_writes_per_hour, rev)
VALUES (@alerts_per_hour, @requests_per_minute, @money_writes_per_minute, @money_writes_per_hour, 1)
ON CONFLICT DO NOTHING RETURNING *;

-- name: UpdateAgentLimits :one
UPDATE operations.agent_limits SET alerts_per_hour = @alerts_per_hour, requests_per_minute = @requests_per_minute,
    money_writes_per_minute = @money_writes_per_minute, money_writes_per_hour = @money_writes_per_hour,
    rev = rev + 1, updated_at = now()
RETURNING *;
