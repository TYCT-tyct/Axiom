-- +goose Up

-- What a person decides about a supplier from the admin (internal/suppliers/README.md「管理接口」).

-- What one unit of the supplier's balance is worth, in its settlement currency (docs/domains/billing.md「供应商成本」):
-- the person's answer, which turns the supplier's costs into money. A later value applies from its time; the rows are
-- only added to, so what a call was costed with can be explained.
CREATE TABLE suppliers.unit_values (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    value numeric NOT NULL CHECK (value > 0 AND value <= 1000 AND value NOT IN ('NaN', 'Infinity')),
    actor text NOT NULL CHECK (actor <> ''),
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1000)
);
CREATE INDEX unit_values_of_supplier ON suppliers.unit_values (supplier_id, id DESC);

ALTER TABLE suppliers.unit_values ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.unit_values
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = unit_values.supplier_id));
GRANT SELECT, INSERT ON suppliers.unit_values TO axiom_app;

-- When a model name was first listed: the queue of names waiting to be mapped is worked oldest first. Rows that exist
-- now are taken to be first seen now.
ALTER TABLE suppliers.supplier_models ADD COLUMN first_seen_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX supplier_models_unmapped ON suppliers.supplier_models (first_seen_at, supplier_group_id, upstream_name)
    WHERE model_id IS NULL AND present;
