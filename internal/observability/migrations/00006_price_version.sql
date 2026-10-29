-- +goose Up
ALTER TABLE observability.call_log ADD COLUMN price_version uuid;
GRANT SELECT (price_version) ON observability.call_log TO axiom_app;
