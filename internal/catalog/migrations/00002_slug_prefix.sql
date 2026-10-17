-- +goose Up

-- The admin lists models by the start of the slug; the class lets a prefix match use the index in any collation.
CREATE INDEX model_slug_prefix ON catalog.model (slug text_pattern_ops);
