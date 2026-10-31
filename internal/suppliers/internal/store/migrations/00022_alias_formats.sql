-- +goose Up

-- A mapping may declare call formats the adapter cannot infer from the endpoint's type. new-api forwards
-- /v1/completions for an OpenAI-type endpoint but Axiom has no per-model capability for it, so an operator declares the
-- formats here; the projection unions them with what the adapter reports (docs/domains/gateway.md「决策与理由」).
-- NULL is no declaration, so every existing alias keeps only its adapter's formats.
ALTER TABLE suppliers.supplier_aliases ADD COLUMN formats text[]
    CHECK (formats IS NULL OR (cardinality(formats) >= 1
        AND formats <@ ARRAY['messages', 'responses', 'chat', 'embeddings', 'rerank', 'completions']));
GRANT UPDATE (formats) ON suppliers.supplier_aliases TO axiom_app;

-- +goose Down

-- The declarations are configuration, not data another table depends on: dropping the column loses them, and the
-- projection falls back to the adapters' formats.
ALTER TABLE suppliers.supplier_aliases DROP COLUMN formats;
