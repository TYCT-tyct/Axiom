-- name: KeyTenant :many
-- The key whose hash is one of hashes on site, found before any scope is known (README.md「登录前的查找」). sqlc
-- cannot see a function's result columns, so the query names and types them.
SELECT k.key_id::uuid AS key_id, k.account_id::uuid AS account_id, k.workspace_id::uuid AS workspace_id
FROM accounts.key_tenant(@site_id::uuid, @hashes::bytea[]) AS k (key_id, account_id, workspace_id);

-- name: AdmitKey :many
-- What a call with the key may do, read in the key's account scope in the admission transaction: no row unless the
-- request came to an enabled API domain of the key's site and the key, its workspace and account are all usable.
SELECT k.preset_id, k.models, k.spend_limit, k.limit_reset, w.budget, w.budget_reset, a.tier_id
FROM accounts.api_keys k
JOIN accounts.workspaces w ON w.site_id = k.site_id AND w.account_id = k.account_id AND w.id = k.workspace_id
JOIN accounts.accounts a ON a.site_id = k.site_id AND a.id = k.account_id
WHERE k.id = @key_id
    AND accounts.domain_site(@domain::text, 'api') = k.site_id
    AND k.kind = 'call' AND k.status = 'active' AND k.deleted_at IS NULL
    AND (k.expires_at IS NULL OR k.expires_at > now())
    AND (k.ip_allow IS NULL OR @client_ip::inet <<= ANY (k.ip_allow))
    AND w.status = 'active' AND w.deleted_at IS NULL
    AND a.status = 'active';

-- name: InsertKey :exec
INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
    secret_version, created_by, expires_at, status, spend_limit, limit_reset, models, ip_allow)
VALUES (@id, @site_id, @account_id, @workspace_id, 'call', @name, @prefix, @last4, @secret_hash, @secret_version,
    @created_by, @expires_at, 'active', @spend_limit, @limit_reset, @models, @ip_allow);

-- name: DefaultWorkspace :many
SELECT id FROM accounts.workspaces WHERE account_id = @account_id AND is_default;

-- name: ListKeys :many
-- An account's call keys that are not deleted, newest first: an ID is a UUIDv7, so its order is the order of making.
SELECT id, workspace_id, name, prefix, last4, expires_at, status, spend_limit, limit_reset, models, ip_allow
FROM accounts.api_keys
WHERE account_id = @account_id AND kind = 'call' AND deleted_at IS NULL AND id < @before
ORDER BY id DESC
LIMIT @page_size;

-- name: GetKey :many
SELECT id, workspace_id, name, prefix, last4, expires_at, status, spend_limit, limit_reset, models, ip_allow
FROM accounts.api_keys WHERE id = @id AND kind = 'call' AND deleted_at IS NULL;

-- name: UpdateKey :many
-- Every setting a user may change, replaced whole; the expiry is not among them.
UPDATE accounts.api_keys SET name = @name, status = @status, spend_limit = @spend_limit, limit_reset = @limit_reset,
    models = @models, ip_allow = @ip_allow
WHERE id = @id AND kind = 'call'
RETURNING site_id, account_id;

-- name: DeleteKey :many
-- A deleted key is final: the table's policy updates only keys not yet deleted.
UPDATE accounts.api_keys SET deleted_at = now() WHERE id = @id RETURNING site_id, account_id;

-- name: GetKeyInfo :one
-- What a key shows of itself: its name, the text a client may see (the prefix and the last four characters), and when
-- it expires. Read in the key's account scope, so another account's key is not found.
SELECT name, prefix, last4, expires_at FROM accounts.api_keys WHERE id = @id AND deleted_at IS NULL;

-- name: ListAccountWorkspaces :many
-- Every workspace of the scope's account, deleted ones included: what billing summed once belongs to them still.
SELECT id FROM accounts.workspaces WHERE account_id = @account_id ORDER BY id;
