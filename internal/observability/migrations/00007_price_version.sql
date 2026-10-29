-- +goose Up
ALTER TABLE observability.call_log ADD COLUMN price_version uuid;
