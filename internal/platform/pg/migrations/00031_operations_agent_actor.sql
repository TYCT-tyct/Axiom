-- +goose Up

-- An operations credential is an actor of its own (docs/domains/operations.md「审计」): what the agent proposes and
-- what runs on its behalf is recorded as operations_agent:<credential ID>. As in 00016, the validated old constraint
-- implies the wider one for every existing row, so new writes are held to it without rescanning the history.
ALTER TABLE platform.audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE platform.audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'system', 'management_key', 'operations_agent')) NOT VALID;

-- +goose Down
ALTER TABLE platform.audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE platform.audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'system', 'management_key')) NOT VALID;
