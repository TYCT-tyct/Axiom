-- +goose NO TRANSACTION
-- +goose Up

-- The constraints 00061 added NOT VALID, checked now that it has committed; VALIDATE takes only SHARE UPDATE EXCLUSIVE,
-- so reads and writes go on. The indexes are built concurrently, and a rerun after an interruption skips those built
-- (internal/platform/pg/README.md「在线迁移」). A generation id is one attempt's: the answer's figure and the lookup
-- share it as their key.
ALTER TABLE suppliers.cost_schedules VALIDATE CONSTRAINT cost_schedules_basis_check;
ALTER TABLE suppliers.supplier_models VALIDATE CONSTRAINT supplier_models_ceiling_check;
ALTER TABLE suppliers.supplier_models VALIDATE CONSTRAINT supplier_models_ceiling_whole;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_generation_id_check;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_actual_native_check;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_reported_cost_check;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_cost_report_check;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_bound_cost_check;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_check_state_check;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_report_whole;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_lookup_has_id;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_line_ratio_check;
ALTER TABLE suppliers.attempt_costs VALIDATE CONSTRAINT attempt_costs_ceiling_whole;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS attempt_costs_generation_id ON suppliers.attempt_costs (generation_id)
    WHERE generation_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS attempt_costs_check_state ON suppliers.attempt_costs (check_state, finished_at)
    WHERE check_state IN ('lookup', 'judge');
