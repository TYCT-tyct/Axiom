-- +goose Up

-- A model's family is a label a person sets (docs/domains/catalog.md「决策与理由」): it groups models for the admin and
-- is never part of a name. NULL is a model in no family.
ALTER TABLE catalog.model ADD COLUMN family text
    CONSTRAINT model_family_check CHECK (family ~ '^[a-z0-9][a-z0-9.-]{0,63}$');

GRANT INSERT(family), UPDATE(family) ON TABLE catalog.model TO axiom_app;
