-- +goose Up
-- Keep this ordering distinct from the account indexes used by console queries.
CREATE INDEX call_log_admin_time ON observability.call_log ((at AT TIME ZONE 'UTC') DESC,id DESC);
