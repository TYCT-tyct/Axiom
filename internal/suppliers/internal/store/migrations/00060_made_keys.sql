-- +goose Up

-- The keys the sync made at a relay (docs/domains/suppliers.md「接入流程」), by the supplier's id of each and the group it
-- was made for, written in a transaction of its own as soon as the create answers. Only a key on this record is ever
-- revoked: a key's name says whose it is to a reader, not to Axiom, since a person can give one of their own the same
-- name. A revoked key's row is deleted with the revocation.
CREATE TABLE suppliers.made_keys (
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers (id),
    upstream_key_ref text NOT NULL CONSTRAINT made_keys_upstream_key_ref_check CHECK (upstream_key_ref <> ''),
    group_ref text NOT NULL CONSTRAINT made_keys_group_ref_check CHECK (group_ref <> ''),
    made_at timestamptz NOT NULL,
    PRIMARY KEY (supplier_id, upstream_key_ref)
);

ALTER TABLE suppliers.made_keys OWNER TO axiom_owner;

CREATE POLICY platform_only ON suppliers.made_keys USING ((SELECT platform.in_platform_scope()));
ALTER TABLE suppliers.made_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.made_keys FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, DELETE ON TABLE suppliers.made_keys TO axiom_app;
