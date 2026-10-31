-- +goose Up

-- P18 adds the direct formats (embeddings and rerank) to the call formats a supplier model and an endpoint can serve
-- (docs/domains/gateway.md「路径与格式」). The CHECK lists the names store.FormatNames writes, so it changes with them.
ALTER TABLE suppliers.supplier_models DROP CONSTRAINT supplier_models_formats_check;
ALTER TABLE suppliers.supplier_models ADD CONSTRAINT supplier_models_formats_check
    CHECK (cardinality(formats) >= 1
        AND formats <@ ARRAY['messages', 'responses', 'chat', 'embeddings', 'rerank']);

ALTER TABLE suppliers.endpoints DROP CONSTRAINT endpoints_formats_check;
ALTER TABLE suppliers.endpoints ADD CONSTRAINT endpoints_formats_check
    CHECK (cardinality(formats) >= 1
        AND formats <@ ARRAY['messages', 'responses', 'chat', 'embeddings', 'rerank']);

-- +goose Down

-- +goose StatementBegin
-- Restoring the old CHECK while rows still name a P18 format would fail with a bare constraint error. Say what to do
-- instead: a rollback must first delete the direct-format endpoints and supplier models, or rewrite their formats to
-- the three a pre-P18 node knows.
DO $migration$
BEGIN
    IF EXISTS (SELECT 1 FROM suppliers.supplier_models
               WHERE formats && ARRAY['embeddings', 'rerank']::text[])
       OR EXISTS (SELECT 1 FROM suppliers.endpoints
                  WHERE formats && ARRAY['embeddings', 'rerank']::text[]) THEN
        RAISE EXCEPTION 'cannot roll back P18: supplier_models or endpoints still use embeddings or rerank; remove those rows or rewrite their formats first';
    END IF;
END
$migration$;
-- +goose StatementEnd

ALTER TABLE suppliers.supplier_models DROP CONSTRAINT supplier_models_formats_check;
ALTER TABLE suppliers.supplier_models ADD CONSTRAINT supplier_models_formats_check
    CHECK (cardinality(formats) >= 1 AND formats <@ ARRAY['messages', 'responses', 'chat']);

ALTER TABLE suppliers.endpoints DROP CONSTRAINT endpoints_formats_check;
ALTER TABLE suppliers.endpoints ADD CONSTRAINT endpoints_formats_check
    CHECK (cardinality(formats) >= 1 AND formats <@ ARRAY['messages', 'responses', 'chat']);
