-- +goose NO TRANSACTION
-- +goose Up

-- The keys bound to a routing preset, counted for the pages that edit or disable it (docs/domains/routing.md「接口、
-- 权限与审计」): the admin counts a preset's keys across every account, a console its own account's, so the preset
-- leads and the account follows; site_id is included because the table's read policy names it, which keeps the count
-- an index-only scan. The zero ID is every unbound key's, the system preset's that nobody edits, so it is left out.
-- A failed concurrent build leaves an invalid index of this name, which must be dropped before rerunning.
CREATE INDEX CONCURRENTLY api_keys_by_preset ON accounts.api_keys (preset_id, account_id) INCLUDE (site_id)
    WHERE deleted_at IS NULL AND preset_id <> '00000000-0000-0000-0000-000000000000';

-- +goose Down
DROP INDEX CONCURRENTLY accounts.api_keys_by_preset;
