-- +goose NO TRANSACTION
-- +goose Up

-- A workspace's name is what a user picks a key's workspace by, so two live workspaces of an account may not share one,
-- ignoring case (docs/domains/accounts.md「工作区」). Every account had only its default workspace until workspaces
-- could be made, so no existing rows collide. A failed concurrent build leaves an invalid index of this name, which
-- must be dropped before rerunning.
CREATE UNIQUE INDEX CONCURRENTLY workspace_names ON accounts.workspaces (account_id, lower(name)) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY accounts.workspace_names;
