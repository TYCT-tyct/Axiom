-- +goose NO TRANSACTION
-- +goose Up

-- Counting and listing live keys must not scan deleted history after removing the call-only predicate.
-- A failed concurrent build leaves an invalid index of this name; drop it before rerunning the migration.
CREATE INDEX CONCURRENTLY api_keys_live_of_account ON accounts.api_keys (account_id, id DESC) WHERE deleted_at IS NULL;
