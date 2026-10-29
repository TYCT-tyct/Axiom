-- +goose NO TRANSACTION
-- +goose Up

-- Keep the call-only index for old nodes; new nodes count both kinds. Follow billing's concurrent-index migrations:
-- a failed build leaves an invalid index of this name, which must be dropped before rerunning the migration.
CREATE INDEX CONCURRENTLY api_keys_created_by_account ON accounts.api_keys (account_id, created_at);
