-- +goose Up

GRANT USAGE ON SCHEMA routing TO axiom_app;

-- Routing presets (docs/domains/routing.md「路由预设」): a preset is a named set of endpoint filters, sorts and price
-- limits that an API key is routed under. A preset belongs to one layer: the platform (no owner IDs), a site, an
-- account of a site, or a workspace of an account. The slug is unique among a layer's live presets and never changes;
-- the name can. revision moves on every change, current_version only when the rules change. deleted_at marks a
-- disabled preset: it stays, because keys bound to it keep routing by it, and new keys or defaults cannot choose it.
-- is_system is the zero-ID allow-all preset only, which this migration writes and nothing edits.
CREATE TABLE routing.presets (
    id uuid PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('platform', 'site', 'account', 'workspace')),
    site_id uuid,
    account_id uuid,
    workspace_id uuid,
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    current_version bigint NOT NULL CHECK (current_version >= 1),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    is_system boolean NOT NULL DEFAULT false,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CHECK ((site_id IS NULL) = (scope = 'platform')),
    CHECK ((account_id IS NULL) = (scope IN ('platform', 'site'))),
    CHECK ((workspace_id IS NULL) = (scope <> 'workspace')),
    CHECK (NOT is_system OR id = '00000000-0000-0000-0000-000000000000' AND scope = 'platform' AND deleted_at IS NULL)
);
-- The owner columns say the layer, so NULLS NOT DISTINCT makes the platform's slugs unique too.
CREATE UNIQUE INDEX presets_slug ON routing.presets (site_id, account_id, workspace_id, slug) NULLS NOT DISTINCT
    WHERE deleted_at IS NULL;

-- Each change of a preset's rules adds a version; a version is never changed or deleted, so the version a call record
-- names is the rules it was routed under. rules is the JSON of internal/routing/rules.go, at most 16 KiB as PostgreSQL
-- prints it. currency is the currency of the price limits of a platform preset that sets any; a site's, account's or
-- workspace's preset is in its site's currency and leaves it NULL.
CREATE TABLE routing.preset_versions (
    preset_id uuid NOT NULL REFERENCES routing.presets,
    version bigint NOT NULL CHECK (version >= 1),
    rules jsonb NOT NULL CHECK (jsonb_typeof(rules) = 'object' AND octet_length(rules::text) <= 16384),
    currency text CHECK (currency ~ '^[A-Z]{3}$'),
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (preset_id, version)
);
-- A preset always points at a version it has; the check waits for the end of the transaction that writes both.
ALTER TABLE routing.presets ADD FOREIGN KEY (id, current_version) REFERENCES routing.preset_versions (preset_id, version)
    DEFERRABLE INITIALLY DEFERRED;

-- The preset a new call key of a layer is bound to, at most one per layer; a key is bound to the first layer, from its
-- workspace up to the platform, that has one (routing.DefaultPreset). version guards an edit against a stale form.
CREATE TABLE routing.preset_defaults (
    scope text NOT NULL CHECK (scope IN ('platform', 'site', 'account', 'workspace')),
    site_id uuid,
    account_id uuid,
    workspace_id uuid,
    preset_id uuid NOT NULL REFERENCES routing.presets,
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((site_id IS NULL) = (scope = 'platform')),
    CHECK ((account_id IS NULL) = (scope IN ('platform', 'site'))),
    CHECK ((workspace_id IS NULL) = (scope <> 'workspace'))
);
CREATE UNIQUE INDEX preset_defaults_layer ON routing.preset_defaults (site_id, account_id, workspace_id) NULLS NOT DISTINCT;

-- Reads follow the default chain down: the platform's rows are every scope's; a site's are its own and its accounts';
-- an account's and its workspaces' are the account's (and its site's, which manages it). Writes are each layer's own.
-- The tenant boundary is the account: pg scopes have no workspace, so who in an account may change a workspace's
-- preset is the API's authorization, as for the account's keys. The scope readers sit in scalar subqueries, so each
-- is a statement InitPlan (internal/platform/pg/migrations/00014_scope_once.sql).
ALTER TABLE routing.presets ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON routing.presets FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR scope = 'platform' AND (SELECT platform.in_any_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR scope = 'site' AND site_id = (SELECT platform.home_site())
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY inserts ON routing.presets FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR NOT is_system AND scope = 'site' AND site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR NOT is_system AND scope IN ('account', 'workspace') AND account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
-- Nothing updates the system preset: a change of its rules is a migration that replaces this policy first.
CREATE POLICY updates ON routing.presets FOR UPDATE USING (
    NOT is_system AND (
        (SELECT platform.in_platform_scope())
        OR scope = 'site' AND site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR scope IN ('account', 'workspace') AND account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    )
) WITH CHECK (
    NOT is_system AND (
        (SELECT platform.in_platform_scope())
        OR scope = 'site' AND site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR scope IN ('account', 'workspace') AND account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    )
);
GRANT SELECT, INSERT, UPDATE (name, current_version, revision, updated_at, deleted_at) ON routing.presets TO axiom_app;

-- A version is read with its preset, and written by whoever may write the preset: the subqueries on routing.presets
-- take that table's own policies.
ALTER TABLE routing.preset_versions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON routing.preset_versions FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR EXISTS (SELECT 1 FROM routing.presets p WHERE p.id = preset_id)
);
CREATE POLICY inserts ON routing.preset_versions FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR EXISTS (
        SELECT 1 FROM routing.presets p WHERE p.id = preset_id AND NOT p.is_system AND (
            p.scope = 'site' AND p.site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
            OR p.scope IN ('account', 'workspace') AND p.account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
        )
    )
);
GRANT SELECT, INSERT ON routing.preset_versions TO axiom_app;

ALTER TABLE routing.preset_defaults ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON routing.preset_defaults FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR scope = 'platform' AND (SELECT platform.in_any_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR scope = 'site' AND site_id = (SELECT platform.home_site())
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY writes ON routing.preset_defaults FOR ALL USING (
    (SELECT platform.in_platform_scope())
    OR scope = 'site' AND site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR scope IN ('account', 'workspace') AND account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR scope = 'site' AND site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR scope IN ('account', 'workspace') AND account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
GRANT SELECT, INSERT, DELETE, UPDATE (preset_id, version, updated_by, updated_at) ON routing.preset_defaults TO axiom_app;

-- The system allow-all preset, with the rules the code-built zero preset had, and the platform default pointing at it,
-- so a node that reads the table routes a key with no preset exactly as the older nodes do.
INSERT INTO routing.presets (id, scope, slug, name, current_version, is_system)
    VALUES ('00000000-0000-0000-0000-000000000000', 'platform', 'allow-all', 'Allow all', 1, true);
INSERT INTO routing.preset_versions (preset_id, version, rules) VALUES ('00000000-0000-0000-0000-000000000000', 1,
    '{"default": {"classes": ["*"], "policies": ["*"]}, "advanced": {"max_attempts": 4}}');
INSERT INTO routing.preset_defaults (scope, preset_id) VALUES ('platform', '00000000-0000-0000-0000-000000000000');

-- +goose Down
DROP TABLE routing.preset_defaults;
ALTER TABLE routing.presets DROP CONSTRAINT presets_id_current_version_fkey;
DROP TABLE routing.preset_versions;
DROP TABLE routing.presets;
