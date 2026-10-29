-- +goose Up

-- NULL preserves the unrestricted behavior of legacy credentials. New complete listings
-- replace it with the upstream policy; an empty array denies every model.
ALTER TABLE suppliers.credentials ADD COLUMN allowed_models text[];
GRANT UPDATE (allowed_models) ON suppliers.credentials TO axiom_app;

-- The immutable alias row is revision 1. Corrections append here, so old nodes can
-- still create aliases and the original model never needs to be rewritten.
ALTER TABLE suppliers.supplier_aliases ADD UNIQUE (id, supplier_id);
CREATE TABLE suppliers.supplier_alias_revisions (
    alias_id uuid NOT NULL,
    supplier_id uuid NOT NULL,
    FOREIGN KEY (alias_id, supplier_id) REFERENCES suppliers.supplier_aliases (id, supplier_id),
    revision bigint NOT NULL CHECK (revision >= 2),
    previous_model_id uuid NOT NULL,
    model_id uuid NOT NULL CHECK (model_id <> previous_model_id),
    actor text NOT NULL CHECK (actor <> ''),
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (alias_id, revision)
);
ALTER TABLE suppliers.supplier_alias_revisions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.supplier_alias_revisions
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = supplier_alias_revisions.supplier_id));
GRANT SELECT, INSERT ON suppliers.supplier_alias_revisions TO axiom_app;
