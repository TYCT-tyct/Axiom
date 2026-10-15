-- +goose Up

-- The catalog's tables (internal/catalog/README.md「表」). Public data (author, model, name, reference_price, revision)
-- is read in any non-empty scope and written only in the platform scope; draft models, and the names and prices that
-- belong to a model that is not listed, are read only in the platform scope. Platform data (change) is read and
-- written only there. "Append only" is a grant: the application has no UPDATE on those tables, and no DELETE on any.
-- Policies call platform's functions without a SET clause, so the planner can inline them.

GRANT USAGE ON SCHEMA catalog TO axiom_app;

CREATE TABLE catalog.author (
    slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    name text NOT NULL CHECK (name <> '' AND char_length(name) <= 100)
);
ALTER TABLE catalog.author ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY public_read ON catalog.author FOR SELECT USING (platform.in_any_scope());
CREATE POLICY platform_insert ON catalog.author FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY platform_update ON catalog.author FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT (slug, name), UPDATE (name) ON catalog.author TO axiom_app;

-- rev is the optimistic-concurrency token of the admin operations; the application may not choose it on insert.
CREATE TABLE catalog.model (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    author text NOT NULL REFERENCES catalog.author,
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}(@[a-z0-9][a-z0-9._-]{0,39})?$'),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'listed', 'retired')),
    version_basis text NOT NULL CHECK (version_basis IN ('dated', 'floating')),
    price_source text NOT NULL CHECK (price_source IN ('litellm', 'openrouter', 'official', 'manual')),
    spec jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(spec) = 'object'),
    rev bigint NOT NULL DEFAULT 0,
    CONSTRAINT slug_author CHECK (split_part(slug, '/', 1) = author)
);
ALTER TABLE catalog.model ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY public_read ON catalog.model FOR SELECT
    USING (platform.in_any_scope() AND (status <> 'draft' OR platform.in_platform_scope()));
CREATE POLICY platform_insert ON catalog.model FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY platform_update ON catalog.model FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT (author, slug, version_basis, price_source, spec),
    UPDATE (status, price_source, spec, rev) ON catalog.model TO axiom_app;

-- norm is Normalize(name), computed by the program; the checks are a backstop, the listing review confirms each
-- name's visibility.
CREATE TABLE catalog.name (
    name text PRIMARY KEY CHECK (octet_length(name) BETWEEN 1 AND 200),
    norm text NOT NULL UNIQUE CHECK (norm <> '' AND norm !~ '[:~]'),
    visibility text NOT NULL CHECK (visibility IN ('public', 'internal')),
    model_id uuid NOT NULL REFERENCES catalog.model,
    CONSTRAINT public_name CHECK (visibility <> 'public' OR (norm !~ '-latest$' AND norm !~ '@'))
);
CREATE INDEX name_model ON catalog.name (model_id);
ALTER TABLE catalog.name ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY public_read ON catalog.name FOR SELECT USING (platform.in_any_scope() AND (platform.in_platform_scope()
    OR EXISTS (SELECT FROM catalog.model m WHERE m.id = model_id AND m.status = 'listed')));
CREATE POLICY platform_insert ON catalog.name FOR INSERT WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT (name, norm, visibility, model_id) ON catalog.name TO axiom_app;

-- A model's reference price history per currency is a chain through supersedes: the first row has none, each later
-- row replaces exactly one row of the same model and currency, and the current price is the row nothing replaces. A
-- null schedule withdraws the currency. The constraints keep the chain from forking (UNIQUE supersedes, one first row
-- per model and currency), crossing to another timeline (the composite key), or looping.
CREATE TABLE catalog.reference_price (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    model_id uuid NOT NULL REFERENCES catalog.model,
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    source text NOT NULL CHECK (source IN ('litellm', 'openrouter', 'official', 'manual')),
    schedule jsonb,
    schedule_sha256 bytea CHECK (octet_length(schedule_sha256) = 32),
    published_at timestamptz NOT NULL DEFAULT now(),
    supersedes uuid UNIQUE CHECK (supersedes <> id),
    CONSTRAINT schedule_in_currency CHECK (jsonb_typeof(schedule) = 'object' AND schedule ->> 'currency' = currency),
    CONSTRAINT schedule_with_hash CHECK ((schedule IS NULL) = (schedule_sha256 IS NULL)),
    UNIQUE (id, model_id, currency),
    FOREIGN KEY (supersedes, model_id, currency) REFERENCES catalog.reference_price (id, model_id, currency)
);
CREATE UNIQUE INDEX reference_price_first ON catalog.reference_price (model_id, currency) WHERE supersedes IS NULL;
ALTER TABLE catalog.reference_price ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY public_read ON catalog.reference_price FOR SELECT USING (platform.in_any_scope()
    AND (platform.in_platform_scope() OR EXISTS (SELECT FROM catalog.model m WHERE m.id = model_id AND m.status = 'listed')));
CREATE POLICY platform_insert ON catalog.reference_price FOR INSERT WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT (model_id, currency, source, schedule, schedule_sha256, supersedes)
    ON catalog.reference_price TO axiom_app;

-- revision is the snapshot's version: one row, raised by every write that changes what a snapshot holds. The row is
-- inserted before row-level security is enabled, because a migration runs with no scope.
CREATE TABLE catalog.revision (
    singleton bool PRIMARY KEY DEFAULT true CHECK (singleton),
    n bigint NOT NULL
);
INSERT INTO catalog.revision (n) VALUES (0);
ALTER TABLE catalog.revision ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY public_read ON catalog.revision FOR SELECT USING (platform.in_any_scope());
CREATE POLICY platform_update ON catalog.revision FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());
GRANT SELECT, UPDATE (n) ON catalog.revision TO axiom_app;

-- The change history: what each admin operation changed, with its evidence. Platform data: the actor, reason and
-- evidence are internal. op_key makes an operation idempotent: a retry finds the first run's row.
CREATE TABLE catalog.change (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    op_key uuid NOT NULL UNIQUE CHECK (op_key <> '00000000-0000-0000-0000-000000000000'),
    at timestamptz NOT NULL DEFAULT now(),
    actor text NOT NULL CHECK (actor <> ''),
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1000),
    kind text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$'),
    subject text NOT NULL CHECK (subject <> ''),
    before jsonb,
    after jsonb,
    evidence jsonb
);
ALTER TABLE catalog.change ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_read ON catalog.change FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY platform_insert ON catalog.change FOR INSERT WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT (op_key, actor, reason, kind, subject, before, after, evidence) ON catalog.change TO axiom_app;
