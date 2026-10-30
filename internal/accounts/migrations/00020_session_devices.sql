-- +goose NO TRANSACTION
-- +goose Up

-- Listing and revoking a user's console sessions filters by user, surface and the live rows; without this index the
-- query walks every session on the site. A failed concurrent build leaves an invalid index of this name, which must be
-- dropped before rerunning the migration.
CREATE INDEX CONCURRENTLY sessions_live_of_user
    ON accounts.sessions (user_id, surface, created_at DESC) WHERE revoked_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS accounts.sessions_live_of_user;
