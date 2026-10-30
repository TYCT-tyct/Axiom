-- +goose Up

-- A public name may carry OpenRouter's :free suffix, because a free version is its own catalog entry with its own
-- prices, limits and endpoints (docs/domains/catalog.md「模型与名字」). Every other ':' is still refused, so a suffix
-- never lands on the base model.
ALTER TABLE catalog.model DROP CONSTRAINT model_slug_check;
ALTER TABLE catalog.model ADD CONSTRAINT model_slug_check CHECK (
    slug ~ '^[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}(:free)?(@[a-z0-9][a-z0-9._-]{0,39})?$'
);

ALTER TABLE catalog.name DROP CONSTRAINT name_norm_check;
ALTER TABLE catalog.name ADD CONSTRAINT name_norm_check CHECK (
    norm <> '' AND norm !~ '~' AND (position(':' in norm) = 0 OR norm ~ '^[^:]+:free$')
);
ALTER TABLE catalog.name DROP CONSTRAINT public_name;
ALTER TABLE catalog.name ADD CONSTRAINT public_name CHECK (
    visibility <> 'public'
    OR (norm !~ '-latest$' AND norm !~ '@' AND (position(':' in norm) = 0 OR norm ~ '^[^:]+:free$'))
);

-- A model alias is a name a client may call that stands for one concrete model. Mapping to the model, rather than
-- deriving a family by prefix, keeps a new member from silently taking over the alias, and keeps discovery, routing
-- and billing on the same model (docs/domains/catalog.md「模型与名字」). The table is append-only: a change adds a
-- revision, and a snapshot reads the highest one.
CREATE TABLE catalog.alias (
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 200),
    rev bigint NOT NULL CHECK (rev > 0),
    model_id uuid NOT NULL REFERENCES catalog.model,
    set_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (name, rev),
    CHECK (name ~ '^~[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}-latest$')
);

ALTER TABLE catalog.alias ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY public_read ON catalog.alias FOR SELECT USING (platform.in_any_scope());
CREATE POLICY platform_insert ON catalog.alias FOR INSERT WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT (name, rev, model_id) ON catalog.alias TO axiom_app;
