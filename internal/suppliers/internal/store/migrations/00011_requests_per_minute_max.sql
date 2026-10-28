-- +goose Up

-- A finite per-minute ceiling, so a node's rolling-window count and the memory of unlimited traffic stay bounded
-- (internal/suppliers/README.md「并发与速率」, issue #382). A value above the cap is lowered to it rather than cleared:
-- clearing would remove the ceiling and leave the account with no bound at all.
UPDATE suppliers.credentials SET requests_per_minute = 100000 WHERE requests_per_minute > 100000;

ALTER TABLE suppliers.credentials DROP CONSTRAINT credentials_requests_per_minute_check;
ALTER TABLE suppliers.credentials ADD CONSTRAINT credentials_requests_per_minute_check
    CHECK (requests_per_minute IS NULL OR requests_per_minute BETWEEN 1 AND 100000);
