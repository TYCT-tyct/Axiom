-- +goose Up
-- Pre-launch only: the partitioned parent cannot be indexed CONCURRENTLY. For populated
-- deployments, use the per-partition concurrent build/attach procedure in README.md.
-- Keep this ordering distinct from the account indexes used by console queries.
CREATE INDEX call_log_admin_time ON observability.call_log ((at AT TIME ZONE 'UTC') DESC,id DESC);
