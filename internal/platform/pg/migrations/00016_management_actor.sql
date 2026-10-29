-- +goose Up

-- The validated old constraint implies the wider one for every existing row. Enforce new writes without rescanning
-- the audit history while holding the table lock; both changes remain atomic.
ALTER TABLE platform.audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE platform.audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'system', 'management_key')) NOT VALID;
