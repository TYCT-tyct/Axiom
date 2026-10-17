-- +goose Up

-- What a person decides about a supplier from the admin (internal/suppliers/README.md「管理接口」).

-- When a model name was first listed: the queue of names waiting to be mapped is worked oldest first. Rows that exist
-- now are taken to be first seen now.
ALTER TABLE suppliers.supplier_models ADD COLUMN first_seen_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX supplier_models_unmapped ON suppliers.supplier_models (first_seen_at, supplier_group_id, upstream_name)
    WHERE model_id IS NULL AND present;
