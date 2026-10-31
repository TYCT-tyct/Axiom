-- name: KeyTenant :many
-- The key whose hash is one of hashes on site, found before any scope is known (README.md「登录前的查找」). sqlc
-- cannot see a function's result columns, so the query names and types them.
SELECT k.key_id::uuid AS key_id, k.account_id::uuid AS account_id, k.workspace_id::uuid AS workspace_id
FROM accounts.key_tenant(@site_id::uuid, @hashes::bytea[]) AS k (key_id, account_id, workspace_id);

-- name: AdmitKey :many
-- What a call with the key may do, read in the key's account scope in the admission transaction: no row unless the
-- request came to an enabled API domain of the key's site and the key and workspace are usable. The account state
-- comes with the same row so suspension is distinguishable without another query.
SELECT k.preset_id, k.models, k.spend_limit, k.limit_reset, w.budget, w.budget_reset, (uuid_send(a.tier_id) || uuid_send(coalesce(owner.id,'00000000-0000-0000-0000-000000000000'::uuid)) || int4send(coalesce(a.rate_requests, t.rate_requests, -1)) || int4send(coalesce(k.rate_requests, a.rate_requests, t.rate_requests, -1)))::bytea AS admission_ids, a.status = 'active' AS account_active
FROM accounts.api_keys k
JOIN accounts.workspaces w ON w.site_id = k.site_id AND w.account_id = k.account_id AND w.id = k.workspace_id
JOIN accounts.accounts a ON a.site_id = k.site_id AND a.id = k.account_id
JOIN accounts.tiers t ON t.site_id = a.site_id AND t.id = a.tier_id
LEFT JOIN accounts.users owner ON owner.account_id=a.id
WHERE k.id = @key_id
    AND accounts.domain_site(@domain::text, 'api') = k.site_id
    AND k.kind = 'call' AND k.status = 'active' AND k.deleted_at IS NULL
    AND (k.expires_at IS NULL OR k.expires_at > now())
    AND (k.ip_allow IS NULL OR @client_ip::inet <<= ANY (k.ip_allow))
    AND w.status = 'active' AND w.deleted_at IS NULL;

-- name: LockAccount :one
-- Takes the account's turn before a key is made for it, so two keys made at once are made one after the other and the
-- second one's count sees the first: a count read before the insert without this lock would let both through. The
-- account's own limits come with it.
SELECT max_keys, key_creations_per_hour FROM accounts.accounts WHERE id = @id FOR NO KEY UPDATE;

-- name: CountKeys :one
-- The keys the account holds that are not deleted: what the limit of keys held bounds. Read in the account's turn
-- (LockAccount), so it counts every key an insert before this one made.
SELECT count(*) FROM accounts.api_keys
WHERE account_id = @account_id AND deleted_at IS NULL;

-- name: RecentKeys :one
-- How many keys the account made in the last window, deleted ones too, and how long until a place is free under a
-- limit of max_made: until the key that must age out for the count to fall below it is a window old (0 while the
-- count is below the limit, when it is not asked). The time is the database's, taken once per statement, after the
-- account's turn was won, and so is the time a key is stamped with (InsertKey): one clock for every node.
WITH recent AS (
    SELECT created_at FROM accounts.api_keys
    WHERE account_id = @account_id
        AND created_at > statement_timestamp() - make_interval(secs => @window_seconds::bigint)
)
SELECT (SELECT count(*) FROM recent)::bigint AS made,
    coalesce((SELECT extract(epoch FROM created_at + make_interval(secs => @window_seconds::bigint) - statement_timestamp())::float8
        FROM recent ORDER BY created_at OFFSET greatest((SELECT count(*) FROM recent) - @max_made::bigint, 0) LIMIT 1), 0)::float8 AS wait_seconds;

-- name: InsertKey :exec
INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
    secret_version, created_by, created_at, expires_at, status, spend_limit, limit_reset, models, ip_allow)
VALUES (@id, @site_id, @account_id, @workspace_id, @kind, @name, @prefix, @last4, @secret_hash, @secret_version,
    @created_by, statement_timestamp(), @expires_at, 'active', @spend_limit, @limit_reset, @models, @ip_allow);

-- name: DefaultWorkspace :many
SELECT id FROM accounts.workspaces WHERE account_id = @account_id AND is_default;

-- name: ListKeys :many
-- An account's keys that are not deleted, newest first: an ID is a UUIDv7, so its order is the order of making. The
-- optional filters are applied while the keys are read in index order, so a later page starts after the last key
-- read. With no filter, or a dense one, a page costs only the rows it reads; a sparse filter reads on through its
-- range, and no filter changes the order.
SELECT id, workspace_id, kind, created_by, name, prefix, last4, created_at, updated_at, expires_at, status, spend_limit, limit_reset, models, ip_allow
FROM accounts.api_keys
WHERE account_id = @account_id AND deleted_at IS NULL AND id < @before
  AND (sqlc.narg('name')::text IS NULL OR strpos(lower(name), lower(sqlc.narg('name')::text)) > 0)
  AND (sqlc.narg('prefix')::text IS NULL OR starts_with(lower(prefix), lower(sqlc.narg('prefix')::text)))
  AND (sqlc.narg('status')::text IS NULL
    OR (sqlc.narg('status')::text = 'expired' AND expires_at IS NOT NULL AND expires_at <= statement_timestamp())
    OR (sqlc.narg('status')::text = 'active' AND status = 'active'
      AND (expires_at IS NULL OR expires_at > statement_timestamp()))
    OR (sqlc.narg('status')::text = 'disabled' AND status = 'disabled'
      AND (expires_at IS NULL OR expires_at > statement_timestamp())))
  AND (sqlc.narg('created_after')::timestamptz IS NULL OR created_at >= sqlc.narg('created_after')::timestamptz)
  AND (sqlc.narg('created_before')::timestamptz IS NULL OR created_at < sqlc.narg('created_before')::timestamptz)
ORDER BY id DESC
LIMIT @page_size;

-- name: GetKey :many
SELECT id, workspace_id, kind, created_by, name, prefix, last4, created_at, updated_at, expires_at, status, spend_limit, limit_reset, models, ip_allow
FROM accounts.api_keys WHERE id = @id AND deleted_at IS NULL;

-- name: LockKey :one
-- The settings a change leaves behind, read in the key's row's turn in the transaction that writes: the audit entry
-- names what this change replaced, and not what a change made at the same moment did.
SELECT site_id, account_id, name, status, spend_limit, limit_reset, models, ip_allow
FROM accounts.api_keys WHERE id = @id AND deleted_at IS NULL
FOR UPDATE;

-- name: UpdateKey :exec
-- Every setting a user may change, replaced whole; the expiry is not among them. The caller holds the key's turn
-- (LockKey) and has read the settings this replaces.
UPDATE accounts.api_keys SET updated_at = statement_timestamp(), name = @name, status = @status, spend_limit = @spend_limit, limit_reset = @limit_reset,
    models = @models, ip_allow = @ip_allow
WHERE id = @id;

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
