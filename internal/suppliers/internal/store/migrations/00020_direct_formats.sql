-- +goose Up

-- P18 adds the direct formats (embeddings and rerank) and legacy completions to the call formats a supplier model and an
-- endpoint can serve (docs/domains/gateway.md「路径与格式」). The CHECK lists the names store.FormatNames writes, so it
-- changes with them.
ALTER TABLE suppliers.supplier_models DROP CONSTRAINT supplier_models_formats_check;
ALTER TABLE suppliers.supplier_models ADD CONSTRAINT supplier_models_formats_check
    CHECK (cardinality(formats) >= 1
        AND formats <@ ARRAY['messages', 'responses', 'chat', 'completions', 'embeddings', 'rerank']);

ALTER TABLE suppliers.endpoints DROP CONSTRAINT endpoints_formats_check;
ALTER TABLE suppliers.endpoints ADD CONSTRAINT endpoints_formats_check
    CHECK (cardinality(formats) >= 1
        AND formats <@ ARRAY['messages', 'responses', 'chat', 'completions', 'embeddings', 'rerank']);

-- +goose Down

ALTER TABLE suppliers.supplier_models DROP CONSTRAINT supplier_models_formats_check;
ALTER TABLE suppliers.supplier_models ADD CONSTRAINT supplier_models_formats_check
    CHECK (cardinality(formats) >= 1 AND formats <@ ARRAY['messages', 'responses', 'chat']);

ALTER TABLE suppliers.endpoints DROP CONSTRAINT endpoints_formats_check;
ALTER TABLE suppliers.endpoints ADD CONSTRAINT endpoints_formats_check
    CHECK (cardinality(formats) >= 1 AND formats <@ ARRAY['messages', 'responses', 'chat']);
