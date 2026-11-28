-- +goose Up

-- An aggregator's line is costed at the highest price any provider of the model has (docs/domains/suppliers-direct.md
-- 「OpenRouter 的成本」): a plan of that basis is a bound, not the cost, and the attempt's cost is what the aggregator says
-- it charged.
ALTER TABLE suppliers.cost_schedules
    DROP CONSTRAINT cost_schedules_basis_check,
    ADD CONSTRAINT cost_schedules_basis_check CHECK (basis IN ('published', 'confirmed', 'ceiling'));

-- The highest price of a name across the aggregator's providers and their price tiers, in its currency per unit, as
-- last read, and when. Both or neither: a name with none is not priced.
ALTER TABLE suppliers.supplier_models
    ADD COLUMN ceiling jsonb CONSTRAINT supplier_models_ceiling_check CHECK (jsonb_typeof(ceiling) = 'object'),
    ADD COLUMN ceiling_read_at timestamptz,
    ADD CONSTRAINT supplier_models_ceiling_whole CHECK ((ceiling IS NULL) = (ceiling_read_at IS NULL));

GRANT UPDATE (ceiling, ceiling_read_at) ON TABLE suppliers.supplier_models TO axiom_app;

-- An attempt at a ceiling plan: the aggregator's id of its generation (the key the answer and the lookup share), what
-- it reported it charged in credits (a reported 0 is a charge of 0, apart from none reported), whether that was the
-- whole charge or only the fee of a BYOK call, the plan's bound of the usage in credits, and what is still to be done
-- with the row: 'lookup' when the answer reported no charge and the generation record is to be read, 'judge' when a
-- charge is to be compared with the bound, 'done' after. estimated_native, the cost booked at the settlement, is never
-- changed: what the generation record makes the cost goes in actual_native, once.
ALTER TABLE suppliers.attempt_costs
    ADD COLUMN generation_id text CONSTRAINT attempt_costs_generation_id_check CHECK (generation_id <> ''),
    ADD COLUMN actual_native numeric CONSTRAINT attempt_costs_actual_native_check
        CHECK (actual_native >= 0 AND actual_native <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])),
    ADD COLUMN reported_cost numeric CONSTRAINT attempt_costs_reported_cost_check
        CHECK (reported_cost >= 0 AND reported_cost <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])),
    ADD COLUMN cost_report text CONSTRAINT attempt_costs_cost_report_check CHECK (cost_report IN ('charged', 'byok_fee')),
    ADD COLUMN bound_cost numeric CONSTRAINT attempt_costs_bound_cost_check
        CHECK (bound_cost >= 0 AND bound_cost <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])),
    ADD COLUMN check_state text CONSTRAINT attempt_costs_check_state_check CHECK (check_state IN ('lookup', 'judge', 'done')),
    ADD CONSTRAINT attempt_costs_report_whole CHECK ((reported_cost IS NULL) = (cost_report IS NULL)),
    ADD CONSTRAINT attempt_costs_lookup_has_id CHECK (check_state IS DISTINCT FROM 'lookup' OR generation_id IS NOT NULL);

CREATE UNIQUE INDEX attempt_costs_generation_id ON suppliers.attempt_costs (generation_id) WHERE generation_id IS NOT NULL;
CREATE INDEX attempt_costs_check_state ON suppliers.attempt_costs (check_state, finished_at)
    WHERE check_state IN ('lookup', 'judge');

GRANT UPDATE (actual_native, reported_cost, cost_report, check_state) ON TABLE suppliers.attempt_costs TO axiom_app;
