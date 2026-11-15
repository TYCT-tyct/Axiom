-- +goose NO TRANSACTION
-- +goose Up

-- Keep registration writes available while indexing existing users. A failed concurrent build
-- leaves an invalid index of this name, which must be dropped before rerunning the migration.
CREATE INDEX CONCURRENTLY users_of_inviter ON accounts.users (site_id, inviter_user_id, id DESC)
    WHERE inviter_user_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS accounts.users_of_inviter;
