-- +goose Up

-- The status reason the admin contract gives at most 200 characters (api/admin.yaml「Supplier」). The engine writes only
-- short sentences of its own, but every writer must stay within the contract, including one added later; a CHECK is the
-- only place no writer can go around. Old rows are short, so the table need not be rewritten.
ALTER TABLE suppliers.suppliers DROP CONSTRAINT suppliers_status_reason_check;
ALTER TABLE suppliers.suppliers ADD CONSTRAINT suppliers_status_reason_check
    CHECK (status_reason IS NULL OR char_length(status_reason) BETWEEN 1 AND 200);

-- +goose Down
ALTER TABLE suppliers.suppliers DROP CONSTRAINT suppliers_status_reason_check;
ALTER TABLE suppliers.suppliers ADD CONSTRAINT suppliers_status_reason_check CHECK (status_reason <> '');
