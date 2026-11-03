-- +goose Up

-- The administrator's source classes and their fingerprint rules (docs/domains/quality.md「来源类别与指纹」). The seeds
-- have no rules: every endpoint stays relay until a rule matches, which is the behaviour before classes were data. A
-- retired class keeps its slug and row so a preset's slug never starts pointing at a different class.
CREATE TABLE quality.source_classes (
    slug text PRIMARY KEY CHECK (slug ~ '^[a-z][a-z0-9-]{0,31}$'),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    name_zh text NOT NULL DEFAULT '' CHECK (length(name_zh) <= 100),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    position integer NOT NULL CHECK (position BETWEEN 0 AND 1000000),
    fallback boolean NOT NULL DEFAULT false,
    system boolean NOT NULL DEFAULT false,
    rules jsonb NOT NULL DEFAULT '{"match":"all","conditions":[]}' CHECK (jsonb_typeof(rules) = 'object'),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CHECK (NOT fallback OR coalesce(jsonb_array_length(rules->'conditions'), 0) = 0)
);
CREATE UNIQUE INDEX source_classes_position ON quality.source_classes (position) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX source_classes_fallback ON quality.source_classes ((true)) WHERE fallback AND deleted_at IS NULL;
ALTER TABLE quality.source_classes ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform ON quality.source_classes USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, UPDATE, DELETE ON quality.source_classes TO axiom_app;

INSERT INTO quality.source_classes (slug, name, name_zh, position, fallback, system) VALUES
    ('official', 'Official', '官方直连', 0, false, true),
    ('cloud', 'Cloud', '云厂商', 10, false, true),
    ('relay', 'Relay', '中转站', 1000, true, true);

-- +goose Down
DROP TABLE quality.source_classes;
