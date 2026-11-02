-- +goose Up

-- A response-cache hit is a call that was answered without an upstream attempt: its price and cost are zero, and this
-- column is what tells it apart from a call the free-of-charge rules waived (docs/domains/gateway.md「响应缓存」).
ALTER TABLE observability.call_log ADD COLUMN cache_hit boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE observability.call_log DROP COLUMN cache_hit;
