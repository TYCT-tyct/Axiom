-- name: ListWorkspaces :many
-- An account's workspaces that are not deleted, its default first and then in the order they were made: an ID is a
-- UUIDv7. An account holds at most its limit, never over 1,000, so one read is the whole list.
SELECT id, name, status, is_default FROM accounts.workspaces
WHERE account_id = @account_id AND deleted_at IS NULL
ORDER BY is_default DESC, id;

-- name: GetWorkspace :many
SELECT id, name, status, is_default FROM accounts.workspaces
WHERE account_id = @account_id AND id = @id AND deleted_at IS NULL;

-- name: CountWorkspaces :one
-- Read after LockAccount, so two workspaces made at once are counted one after the other.
SELECT count(*) FROM accounts.workspaces WHERE account_id = @account_id AND deleted_at IS NULL;

-- name: InsertWorkspace :exec
INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
VALUES (@id, @site_id, @account_id, @name, 'active', false);

-- name: LockWorkspace :many
-- The settings a change replaces, read in the row's turn, so the audit entry names what this change replaced and not
-- what a change made at the same moment did. NO KEY: a key being made in the workspace holds its row FOR KEY SHARE
-- through the foreign key, and need not wait for a rename.
SELECT site_id, name, status, is_default FROM accounts.workspaces
WHERE account_id = @account_id AND id = @id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- name: UpdateWorkspace :exec
UPDATE accounts.workspaces SET name = @name, status = @status WHERE id = @id;

-- name: ReadSiteWorkspaceLimit :one
-- The site's own limit on an account's workspaces (NULL is not set); no row means no such site.
SELECT t.max_workspaces_per_account FROM accounts.sites s LEFT JOIN accounts.site_settings t ON t.site_id = s.id WHERE s.id = @site_id;

-- name: WriteSiteWorkspaceLimit :exec
-- Replaces the site's own limit and leaves its other settings as they are.
INSERT INTO accounts.site_settings (site_id, max_workspaces_per_account) VALUES (@site_id, sqlc.narg(max_workspaces)::integer)
ON CONFLICT (site_id) DO UPDATE SET max_workspaces_per_account = excluded.max_workspaces_per_account;

-- name: ReadAccountWorkspaceLimit :one
-- The account's own limit and its site's, which it falls back to; no row means no such account.
SELECT a.site_id, a.max_workspaces, t.max_workspaces_per_account AS site_max_workspaces
FROM accounts.accounts a LEFT JOIN accounts.site_settings t ON t.site_id = a.site_id WHERE a.id = @account_id;

-- name: WriteAccountWorkspaceLimit :exec
UPDATE accounts.accounts SET max_workspaces = sqlc.narg(max_workspaces)::integer WHERE id = @account_id;
