-- +goose Up

-- Cloud platforms (docs/domains/suppliers.md「云平台」). A Vertex AI or Bedrock credential serves by region, one credential
-- row a region, and a supplier serves from a region with at most one row at a time; Azure OpenAI's has no region. A
-- row's identity (upstream_key_ref) is the credential and its region, so a replacement erases the region's row and
-- stores another, or seals the same credential again in the row that had it. call_names maps an endpoint's name to the
-- name this region's calls send when the two differ (a Bedrock model the region serves only through its inference
-- profile); NULL for none. A cloud supplier's cloud_account is what is not secret about its credential, which a
-- replacement changes with it.
ALTER TABLE suppliers.credentials
    ADD COLUMN region text CONSTRAINT credentials_region_check CHECK (region ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    ADD COLUMN call_names jsonb CONSTRAINT credentials_call_names_check CHECK (jsonb_typeof(call_names) = 'object');

CREATE UNIQUE INDEX credentials_supplier_region ON suppliers.credentials (supplier_id, region)
    WHERE region IS NOT NULL AND status <> 'erased';

ALTER TABLE suppliers.suppliers
    ADD COLUMN cloud_account jsonb CONSTRAINT suppliers_cloud_account_check CHECK (jsonb_typeof(cloud_account) = 'object'),
    ADD CONSTRAINT cloud_account_is_cloud CHECK (cloud_account IS NULL OR mode = 'cloud');

GRANT UPDATE (cloud_account) ON TABLE suppliers.suppliers TO axiom_app;
GRANT UPDATE (call_names) ON TABLE suppliers.credentials TO axiom_app;
