-- name: ListWorkspaces :many
-- An account's workspaces that are not deleted, its default first and then in the order they were made: an ID is a
-- UUIDv7. An account holds at most maxWorkspaces, so one read is the whole list.
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
