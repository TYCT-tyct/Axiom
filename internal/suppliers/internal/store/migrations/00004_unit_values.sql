-- +goose Up

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
