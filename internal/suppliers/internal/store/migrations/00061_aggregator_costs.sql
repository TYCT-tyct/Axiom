-- +goose Up

-- An aggregator's line is costed at the highest price any provider of the model has (docs/domains/suppliers-direct.md
-- 「OpenRouter 的成本」): a plan of that basis is a bound, not the cost, and the attempt's cost is what the aggregator says
-- it charged. The constraints on existing tables go in NOT VALID and are checked by 00062, and the indexes are built
-- there concurrently (internal/platform/pg/README.md「在线迁移」).
ALTER TABLE suppliers.cost_schedules
    DROP CONSTRAINT cost_schedules_basis_check,
    ADD CONSTRAINT cost_schedules_basis_check CHECK (basis IN ('published', 'confirmed', 'ceiling')) NOT VALID;

-- The highest price of a name across the aggregator's providers and their price tiers, in its currency per unit, as
-- last read, and when. Both or neither: a name with none is not priced.
ALTER TABLE suppliers.supplier_models
    ADD COLUMN ceiling jsonb,
    ADD COLUMN ceiling_read_at timestamptz,
    ADD CONSTRAINT supplier_models_ceiling_check CHECK (jsonb_typeof(ceiling) = 'object') NOT VALID,
    ADD CONSTRAINT supplier_models_ceiling_whole CHECK ((ceiling IS NULL) = (ceiling_read_at IS NULL)) NOT VALID;

GRANT UPDATE (ceiling, ceiling_read_at) ON TABLE suppliers.supplier_models TO axiom_app;

-- An attempt at a ceiling plan: the aggregator's id of its generation (the key the answer and the lookup share), what
-- it reported it charged in credits (a reported 0 is a charge of 0, apart from none reported), whether that was the
-- whole charge or only the fee of a BYOK call, the plan's bound of the usage in credits, and what is still to be done
-- with the row: 'lookup' when the answer reported no charge and the generation record is to be read, 'judge' when a
-- charge is to be compared with the bound, 'done' after. estimated_native, the cost booked at the settlement, is never
-- changed: what the generation record makes the cost goes in actual_native, once. line_ratio is the line's ratio the
-- attempt ran at, what a credit cost then: the lookup and the history reckon the attempt at it, never at the line's
-- ratio of a later day. A ceiling's attempt has all of the bound, the ratio and the check state, or none.
ALTER TABLE suppliers.attempt_costs
    ADD COLUMN generation_id text,
    ADD COLUMN line_ratio numeric,
    ADD COLUMN actual_native numeric,
    ADD COLUMN reported_cost numeric,
    ADD COLUMN cost_report text,
    ADD COLUMN bound_cost numeric,
    ADD COLUMN check_state text,
    ADD CONSTRAINT attempt_costs_generation_id_check CHECK (generation_id <> '') NOT VALID,
    ADD CONSTRAINT attempt_costs_actual_native_check
        CHECK (actual_native >= 0 AND actual_native <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])) NOT VALID,
    ADD CONSTRAINT attempt_costs_reported_cost_check
        CHECK (reported_cost >= 0 AND reported_cost <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])) NOT VALID,
    ADD CONSTRAINT attempt_costs_cost_report_check CHECK (cost_report IN ('charged', 'byok_fee')) NOT VALID,
    ADD CONSTRAINT attempt_costs_bound_cost_check
        CHECK (bound_cost >= 0 AND bound_cost <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric])) NOT VALID,
    ADD CONSTRAINT attempt_costs_check_state_check CHECK (check_state IN ('lookup', 'judge', 'done')) NOT VALID,
    ADD CONSTRAINT attempt_costs_report_whole CHECK ((reported_cost IS NULL) = (cost_report IS NULL)) NOT VALID,
    ADD CONSTRAINT attempt_costs_lookup_has_id CHECK (check_state IS DISTINCT FROM 'lookup' OR generation_id IS NOT NULL) NOT VALID,
    ADD CONSTRAINT attempt_costs_line_ratio_check CHECK (line_ratio > 0) NOT VALID,
    ADD CONSTRAINT attempt_costs_ceiling_whole
        CHECK ((bound_cost IS NULL) = (line_ratio IS NULL) AND (bound_cost IS NULL) = (check_state IS NULL)) NOT VALID;

GRANT UPDATE (actual_native, reported_cost, cost_report, check_state) ON TABLE suppliers.attempt_costs TO axiom_app;
