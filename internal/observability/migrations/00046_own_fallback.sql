-- +goose Up

-- Sparse and empty fallback pages must not scan every call of the account.
-- As in 00002/00009, this builds on the small, pre-launch call log. A populated
-- installation needs concurrent partition builds and ATTACH (README.md「索引」).
CREATE INDEX call_log_account_fallback_at
    ON observability.call_log (account_id, at DESC, id DESC)
    WHERE attempt_count > 1;
