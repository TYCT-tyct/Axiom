-- +goose Up

ALTER TABLE suppliers.unit_values ADD COLUMN currency text, ADD COLUMN native_unit text;

-- pg.Migrate supplies platform scope, including supplier rows hidden by forced RLS from a tenant.
UPDATE suppliers.unit_values u SET currency = s.settlement_currency, native_unit = s.native_unit
FROM suppliers.suppliers s WHERE s.id = u.supplier_id;

ALTER TABLE suppliers.unit_values ALTER COLUMN currency SET NOT NULL, ALTER COLUMN native_unit SET NOT NULL;

-- Stamp both dimensions together, including INSERTs from old nodes during a rolling release. The function uses the
-- caller's privileges and scope, so it cannot read a supplier the caller cannot see.
-- +goose StatementBegin
CREATE FUNCTION suppliers.unit_value_dimensions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT s.settlement_currency, s.native_unit INTO NEW.currency, NEW.native_unit
    FROM suppliers.suppliers s WHERE s.id = NEW.supplier_id;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER unit_value_dimensions BEFORE INSERT ON suppliers.unit_values
FOR EACH ROW EXECUTE FUNCTION suppliers.unit_value_dimensions();
