-- +goose Up

-- The reconciliation of the cap counters (docs/domains/operations.md「失败与对账」). Every minute the counters of the
-- current UTC day and ISO week are added up again from money_actions; a scope and category whose counters differ is
-- paused: every proposal there waits for the owner until the owner has the counters recomputed. Nullable, so a node
-- that predates it reads and writes the policies as before.
ALTER TABLE operations.cap_policies ADD COLUMN paused_at timestamptz;
GRANT UPDATE (paused_at) ON operations.cap_policies TO axiom_app;

-- A node that predates the pause does not read paused_at and would reserve a proposal within the caps at once, in a
-- scope whose counters are known to be wrong; so would this one, rolled back to. The database is the one check every
-- version runs: in a paused scope only a proposal that waits for the owner is reserved. A proposal holds its policy
-- FOR SHARE and a pause takes it FOR UPDATE, so a pause lands wholly before a proposal or after it.
-- +goose StatementBegin
CREATE FUNCTION operations.refuse_autonomous_money_when_paused() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM operations.cap_policies p
        WHERE p.category = NEW.category AND p.site_id IS NOT DISTINCT FROM NEW.site_id AND p.paused_at IS NOT NULL
    ) AND NOT EXISTS (
        SELECT 1 FROM operations.operations o WHERE o.id = NEW.operation_id AND o.state = 'pending_approval'
    ) THEN
        RAISE EXCEPTION 'the caps of this scope are paused: a proposal waits for the owner' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER refuse_autonomous_money_when_paused
    BEFORE INSERT ON operations.money_actions
    FOR EACH ROW EXECUTE FUNCTION operations.refuse_autonomous_money_when_paused();

-- The reconciliation reads the money actions of the current week only.
CREATE INDEX money_actions_week ON operations.money_actions (week_start);

-- +goose Down
DROP INDEX operations.money_actions_week;
DROP TRIGGER refuse_autonomous_money_when_paused ON operations.money_actions;
DROP FUNCTION operations.refuse_autonomous_money_when_paused();
REVOKE UPDATE (paused_at) ON operations.cap_policies FROM axiom_app;
ALTER TABLE operations.cap_policies DROP COLUMN paused_at;
