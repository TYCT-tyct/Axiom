-- +goose Up
ALTER TABLE accounts.api_keys ADD COLUMN last_used_at timestamptz;
GRANT UPDATE (last_used_at) ON accounts.api_keys TO axiom_app;

-- +goose Down
REVOKE UPDATE (last_used_at) ON accounts.api_keys FROM axiom_app;
ALTER TABLE accounts.api_keys DROP COLUMN last_used_at;
