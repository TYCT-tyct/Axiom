-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

CREATE TABLE routing.preset_budget (
    id boolean DEFAULT true NOT NULL,
    presets bigint NOT NULL,
    bytes bigint NOT NULL,
    CONSTRAINT preset_budget_bytes_check CHECK ((bytes >= 0)),
    CONSTRAINT preset_budget_id_check CHECK (id),
    CONSTRAINT preset_budget_presets_check CHECK ((presets >= 0))
);



ALTER TABLE routing.preset_budget OWNER TO axiom_owner;

CREATE SEQUENCE routing.preset_default_versions
    START WITH 2
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE routing.preset_default_versions OWNER TO axiom_owner;

CREATE TABLE routing.preset_defaults (
    scope text NOT NULL,
    site_id uuid,
    account_id uuid,
    workspace_id uuid,
    preset_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_by uuid,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT preset_defaults_check CHECK (((site_id IS NULL) = (scope = 'platform'::text))),
    CONSTRAINT preset_defaults_check1 CHECK (((account_id IS NULL) = (scope = ANY (ARRAY['platform'::text, 'site'::text])))),
    CONSTRAINT preset_defaults_check2 CHECK (((workspace_id IS NULL) = (scope <> 'workspace'::text))),
    CONSTRAINT preset_defaults_scope_check CHECK ((scope = ANY (ARRAY['platform'::text, 'site'::text, 'account'::text, 'workspace'::text]))),
    CONSTRAINT preset_defaults_version_check CHECK ((version >= 1))
);



ALTER TABLE routing.preset_defaults OWNER TO axiom_owner;

CREATE TABLE routing.preset_limits (
    singleton boolean DEFAULT true NOT NULL,
    rev bigint NOT NULL,
    platform_active bigint,
    platform_total bigint,
    site_active bigint,
    site_total bigint,
    account_active bigint,
    account_total bigint,
    workspace_active bigint,
    workspace_total bigint,
    kept_versions bigint,
    max_presets bigint,
    max_bytes bigint,
    CONSTRAINT preset_limits_account_active_check CHECK (((account_active >= 1) AND (account_active <= 500))),
    CONSTRAINT preset_limits_account_total_check CHECK (((account_total >= 1) AND (account_total <= 5000))),
    CONSTRAINT preset_limits_kept_versions_check CHECK (((kept_versions >= 50) AND (kept_versions <= 500))),
    CONSTRAINT preset_limits_max_bytes_check CHECK (((max_bytes >= 1048576) AND (max_bytes <= '2147483648'::bigint))),
    CONSTRAINT preset_limits_max_presets_check CHECK (((max_presets >= 1) AND (max_presets <= 1000000))),
    CONSTRAINT preset_limits_platform_active_check CHECK (((platform_active >= 1) AND (platform_active <= 500))),
    CONSTRAINT preset_limits_platform_total_check CHECK (((platform_total >= 1) AND (platform_total <= 5000))),
    CONSTRAINT preset_limits_rev_check CHECK ((rev >= 1)),
    CONSTRAINT preset_limits_singleton_check CHECK (singleton),
    CONSTRAINT preset_limits_site_active_check CHECK (((site_active >= 1) AND (site_active <= 500))),
    CONSTRAINT preset_limits_site_total_check CHECK (((site_total >= 1) AND (site_total <= 5000))),
    CONSTRAINT preset_limits_workspace_active_check CHECK (((workspace_active >= 1) AND (workspace_active <= 500))),
    CONSTRAINT preset_limits_workspace_total_check CHECK (((workspace_total >= 1) AND (workspace_total <= 5000)))
);



ALTER TABLE routing.preset_limits OWNER TO axiom_owner;

CREATE TABLE routing.preset_suspension (
    singleton boolean DEFAULT true NOT NULL,
    since timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT preset_suspension_singleton_check CHECK (singleton)
);



ALTER TABLE routing.preset_suspension OWNER TO axiom_owner;

CREATE TABLE routing.preset_versions (
    preset_id uuid NOT NULL,
    version bigint NOT NULL,
    rules jsonb NOT NULL,
    currency text,
    created_by uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT preset_versions_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT preset_versions_rules_check CHECK (((jsonb_typeof(rules) = 'object'::text) AND (octet_length((rules)::text) <= 16384))),
    CONSTRAINT preset_versions_version_check CHECK ((version >= 1))
);



ALTER TABLE routing.preset_versions OWNER TO axiom_owner;

CREATE TABLE routing.presets (
    id uuid NOT NULL,
    scope text NOT NULL,
    site_id uuid,
    account_id uuid,
    workspace_id uuid,
    slug text NOT NULL,
    name text NOT NULL,
    current_version bigint NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    is_system boolean DEFAULT false NOT NULL,
    created_by uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    CONSTRAINT presets_check CHECK (((site_id IS NULL) = (scope = 'platform'::text))),
    CONSTRAINT presets_check1 CHECK (((account_id IS NULL) = (scope = ANY (ARRAY['platform'::text, 'site'::text])))),
    CONSTRAINT presets_check2 CHECK (((workspace_id IS NULL) = (scope <> 'workspace'::text))),
    CONSTRAINT presets_check3 CHECK (((NOT is_system) OR ((id = '00000000-0000-0000-0000-000000000000'::uuid) AND (scope = 'platform'::text) AND (deleted_at IS NULL)))),
    CONSTRAINT presets_current_version_check CHECK ((current_version >= 1)),
    CONSTRAINT presets_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT presets_revision_check CHECK ((revision >= 1)),
    CONSTRAINT presets_scope_check CHECK ((scope = ANY (ARRAY['platform'::text, 'site'::text, 'account'::text, 'workspace'::text]))),
    CONSTRAINT presets_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,63}$'::text))
);



ALTER TABLE routing.presets OWNER TO axiom_owner;

INSERT INTO routing.presets (id, scope, site_id, account_id, workspace_id, slug, name, current_version, revision, is_system, created_by, created_at, updated_at, deleted_at) VALUES ('00000000-0000-0000-0000-000000000000', 'platform', NULL, NULL, NULL, 'allow-all', 'Allow all', 1, 1, true, NULL, now(), now(), NULL);
INSERT INTO routing.preset_versions (preset_id, version, rules, currency, created_by, created_at) VALUES ('00000000-0000-0000-0000-000000000000', 1, '{"default": {"classes": ["*"], "policies": ["*"]}, "advanced": {"max_attempts": 4}}', NULL, NULL, now());
INSERT INTO routing.preset_defaults (scope, site_id, account_id, workspace_id, preset_id, version, updated_by, updated_at) VALUES ('platform', NULL, NULL, NULL, '00000000-0000-0000-0000-000000000000', 1, NULL, now());
INSERT INTO routing.preset_budget (id, presets, bytes) VALUES (true, 1, 83);

ALTER TABLE ONLY routing.preset_budget
    ADD CONSTRAINT preset_budget_pkey PRIMARY KEY (id);

ALTER TABLE ONLY routing.preset_limits
    ADD CONSTRAINT preset_limits_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY routing.preset_suspension
    ADD CONSTRAINT preset_suspension_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY routing.preset_versions
    ADD CONSTRAINT preset_versions_pkey PRIMARY KEY (preset_id, version);

ALTER TABLE ONLY routing.presets
    ADD CONSTRAINT presets_pkey PRIMARY KEY (id);

CREATE UNIQUE INDEX preset_defaults_layer ON routing.preset_defaults USING btree (site_id, account_id, workspace_id) NULLS NOT DISTINCT;

CREATE INDEX presets_owner ON routing.presets USING btree (site_id, account_id, workspace_id);

CREATE UNIQUE INDEX presets_slug ON routing.presets USING btree (site_id, account_id, workspace_id, slug) NULLS NOT DISTINCT WHERE (deleted_at IS NULL);

ALTER TABLE ONLY routing.preset_defaults
    ADD CONSTRAINT preset_defaults_preset_id_fkey FOREIGN KEY (preset_id) REFERENCES routing.presets(id);

ALTER TABLE ONLY routing.preset_versions
    ADD CONSTRAINT preset_versions_preset_id_fkey FOREIGN KEY (preset_id) REFERENCES routing.presets(id);

ALTER TABLE ONLY routing.presets
    ADD CONSTRAINT presets_id_current_version_fkey FOREIGN KEY (id, current_version) REFERENCES routing.preset_versions(preset_id, version) DEFERRABLE INITIALLY DEFERRED;

CREATE POLICY inserts ON routing.preset_versions FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (EXISTS ( SELECT 1
   FROM routing.presets p
  WHERE ((p.id = preset_versions.preset_id) AND (NOT p.is_system) AND (((p.scope = 'site'::text) AND (p.site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) OR ((p.scope = ANY (ARRAY['account'::text, 'workspace'::text])) AND (p.account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))))))));

CREATE POLICY inserts ON routing.presets FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((NOT is_system) AND (scope = 'site'::text) AND (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) OR ((NOT is_system) AND (scope = ANY (ARRAY['account'::text, 'workspace'::text])) AND (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))));

CREATE POLICY platform ON routing.preset_limits USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform ON routing.preset_suspension USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));







CREATE POLICY prunes ON routing.preset_versions FOR DELETE USING ((EXISTS ( SELECT 1
   FROM routing.presets p
  WHERE ((p.id = preset_versions.preset_id) AND (preset_versions.version <= (p.current_version - 50)) AND (( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((NOT p.is_system) AND (p.scope = 'site'::text) AND (p.site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) OR ((NOT p.is_system) AND (p.scope = ANY (ARRAY['account'::text, 'workspace'::text])) AND (p.account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))))))));

CREATE POLICY reads ON routing.preset_defaults FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((scope = 'platform'::text) AND ( SELECT platform.in_any_scope() AS in_any_scope)) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR ((scope = 'site'::text) AND (site_id = ( SELECT platform.home_site() AS home_site))) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON routing.preset_limits FOR SELECT USING (true);

CREATE POLICY reads ON routing.preset_suspension FOR SELECT USING (true);

CREATE POLICY reads ON routing.preset_versions FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (EXISTS ( SELECT 1
   FROM routing.presets p
  WHERE (p.id = preset_versions.preset_id)))));

CREATE POLICY reads ON routing.presets FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((scope = 'platform'::text) AND ( SELECT platform.in_any_scope() AS in_any_scope)) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR ((scope = 'site'::text) AND (site_id = ( SELECT platform.home_site() AS home_site))) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY totals ON routing.preset_budget USING (true) WITH CHECK (true);

CREATE POLICY updates ON routing.presets FOR UPDATE USING (((NOT is_system) AND (( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((scope = 'site'::text) AND (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) OR ((scope = ANY (ARRAY['account'::text, 'workspace'::text])) AND (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))))) WITH CHECK (((NOT is_system) AND (( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((scope = 'site'::text) AND (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) OR ((scope = ANY (ARRAY['account'::text, 'workspace'::text])) AND (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))))));

CREATE POLICY writes ON routing.preset_defaults USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((scope = 'site'::text) AND (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) OR ((scope = ANY (ARRAY['account'::text, 'workspace'::text])) AND (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR ((scope = 'site'::text) AND (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) OR ((scope = ANY (ARRAY['account'::text, 'workspace'::text])) AND (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))));

GRANT USAGE ON SCHEMA routing TO axiom_app;

GRANT SELECT ON TABLE routing.preset_budget TO axiom_app;

GRANT UPDATE(presets) ON TABLE routing.preset_budget TO axiom_app;

GRANT UPDATE(bytes) ON TABLE routing.preset_budget TO axiom_app;

GRANT USAGE ON SEQUENCE routing.preset_default_versions TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE routing.preset_defaults TO axiom_app;

GRANT UPDATE(preset_id) ON TABLE routing.preset_defaults TO axiom_app;

GRANT UPDATE(version) ON TABLE routing.preset_defaults TO axiom_app;

GRANT UPDATE(updated_by) ON TABLE routing.preset_defaults TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE routing.preset_defaults TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE routing.preset_limits TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE routing.preset_suspension TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE routing.preset_versions TO axiom_app;

GRANT SELECT,INSERT ON TABLE routing.presets TO axiom_app;

GRANT UPDATE(name) ON TABLE routing.presets TO axiom_app;

GRANT UPDATE(current_version) ON TABLE routing.presets TO axiom_app;

GRANT UPDATE(revision) ON TABLE routing.presets TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE routing.presets TO axiom_app;

GRANT UPDATE(deleted_at) ON TABLE routing.presets TO axiom_app;


ALTER TABLE ONLY routing.preset_budget FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY routing.preset_defaults FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY routing.preset_limits FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY routing.preset_suspension FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY routing.preset_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY routing.presets FORCE ROW LEVEL SECURITY;
ALTER TABLE routing.preset_budget ENABLE ROW LEVEL SECURITY;
ALTER TABLE routing.preset_defaults ENABLE ROW LEVEL SECURITY;
ALTER TABLE routing.preset_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE routing.preset_suspension ENABLE ROW LEVEL SECURITY;
ALTER TABLE routing.preset_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE routing.presets ENABLE ROW LEVEL SECURITY;
