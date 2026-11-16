-- +goose Up

-- A full 31-day session window can select most rows of an account. Cover the grouping, optional filters and RLS
-- so it still reads a bounded account/time index instead of the much wider call heap. Empty sessions add no index entries.
-- As with 00009, populated deployments must build partition indexes concurrently and ATTACH (README「索引」).
CREATE INDEX call_log_account_sessions_at ON observability.call_log (account_id, at DESC, id DESC)
    INCLUDE (site_id, session_id, client_app, model, currency, price_nano, workspace_id, key_id, error_kind, attempt_count)
    WHERE session_id <> '' AND NOT billing_pending;
