-- +goose Up

-- Like 00002, build these on the small, pre-launch call log; populated installations must use
-- concurrent partition builds and ATTACH (README.md「索引」) to avoid blocking the recorder.
CREATE INDEX call_log_account_key_at ON observability.call_log (account_id, key_id, at DESC, id DESC);
CREATE INDEX call_log_account_workspace_at ON observability.call_log (account_id, workspace_id, at DESC, id DESC);
-- Keep the false branch as NOT DistinctExpr so custom plans can match the boolean index for sparse successes.
CREATE INDEX call_log_account_status_at ON observability.call_log (account_id, (error_kind IS DISTINCT FROM ''), at DESC, id DESC);
