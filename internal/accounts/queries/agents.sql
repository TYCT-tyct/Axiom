-- The operations credentials (README.md「运营凭证」), all in the platform scope.

-- name: InsertAgentCredential :exec
INSERT INTO accounts.agent_credentials (id, name, permissions, sites, platform, proposer_vendors, created_by, created_at, expires_at)
VALUES (@id, @name, @permissions, @sites, @platform, @proposer_vendors, @created_by, @created_at, @expires_at);

-- name: InsertAgentToken :exec
INSERT INTO accounts.agent_credential_tokens (secret_hash, secret_version, credential_id, last4, created_at)
VALUES (@secret_hash, @secret_version, @credential_id, @last4, @created_at);

-- name: AgentByToken :many
-- The live credential a token belongs to: the token is current or within its overlap, the credential is neither
-- revoked nor expired. Read on every request, so a revocation holds from the next one.
SELECT c.id, c.name, c.permissions, c.sites, c.platform, c.proposer_vendors, c.expires_at, c.last_used_at
FROM accounts.agent_credential_tokens t
JOIN accounts.agent_credentials c ON c.id = t.credential_id
WHERE t.secret_hash = ANY (@hashes::bytea[]) AND (t.retires_at IS NULL OR t.retires_at > @now)
    AND c.revoked_at IS NULL AND c.expires_at > @now
LIMIT 1;

-- name: TouchAgent :exec
UPDATE accounts.agent_credentials SET last_used_at = @now WHERE id = @id;

-- name: LockAgentCredential :many
SELECT id, revoked_at, expires_at FROM accounts.agent_credentials WHERE id = @id FOR UPDATE;

-- name: EndAgentOverlap :exec
-- A rotation ends now the overlap an earlier rotation gave the token before the current one.
UPDATE accounts.agent_credential_tokens SET retires_at = @now WHERE credential_id = @credential_id AND retires_at > @now;

-- name: RetireAgentToken :exec
-- The current token works until @retires_at, when the rotation's new token has replaced it.
UPDATE accounts.agent_credential_tokens SET retires_at = @retires_at WHERE credential_id = @credential_id AND retires_at IS NULL;

-- name: RevokeAgentCredential :many
UPDATE accounts.agent_credentials SET revoked_at = @now WHERE id = @id AND revoked_at IS NULL RETURNING id;

-- name: RevokeAllAgentCredentials :many
UPDATE accounts.agent_credentials SET revoked_at = @now WHERE revoked_at IS NULL RETURNING id;

-- name: ListAgentCredentials :many
-- Newest first, a page after the cursor's ID (IDs are UUIDv7). The tokens are their last four characters only.
SELECT c.id, c.name, c.permissions, c.sites, c.platform, c.proposer_vendors, c.created_by, c.created_at, c.expires_at,
    c.revoked_at, c.last_used_at,
    (SELECT t.last4 FROM accounts.agent_credential_tokens t WHERE t.credential_id = c.id AND t.retires_at IS NULL) AS last4,
    (SELECT t.retires_at FROM accounts.agent_credential_tokens t WHERE t.credential_id = c.id AND t.retires_at IS NOT NULL
        ORDER BY t.retires_at DESC LIMIT 1) AS previous_retires_at
FROM accounts.agent_credentials c
WHERE @after::uuid = '00000000-0000-0000-0000-000000000000' OR c.id < @after::uuid
ORDER BY c.id DESC
LIMIT @page_size;
