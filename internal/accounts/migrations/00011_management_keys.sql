-- +goose Up

ALTER TABLE accounts.api_keys
    ADD COLUMN updated_at timestamptz,
    ADD COLUMN management_window timestamptz,
    ADD COLUMN management_requests integer NOT NULL DEFAULT 0;
-- Existing rows have the constant zero default; enforce future writes without scanning key history under a DDL lock.
ALTER TABLE accounts.api_keys ADD CONSTRAINT api_keys_management_requests_check
    CHECK (management_requests BETWEEN 0 AND 61) NOT VALID;
GRANT UPDATE (updated_at, management_window, management_requests) ON accounts.api_keys TO axiom_app;
