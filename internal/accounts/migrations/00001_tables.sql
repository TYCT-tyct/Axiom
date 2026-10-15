-- +goose Up

-- Sites, their domains, settings and tiers, the platform's roles, accounts and workspaces (internal/accounts/README.md
-- 「数据」). Every tenant row carries its site_id, and account rows their account_id, which no grant lets the
-- application change; composite foreign keys keep them in line with the rows above, so a policy reads only its own
-- row. The application gets no DELETE anywhere, and UPDATE only on the columns the README lists as updatable.

-- A site is the main site, with neither parent nor owner, or a secondary site under the main site, opened by an
-- account of it. Its owner and default-tier keys are added below, once accounts and tiers exist.
CREATE TABLE accounts.sites (
    id uuid PRIMARY KEY,
    parent_site_id uuid,
    owner_account_id uuid,
    is_main boolean NOT NULL GENERATED ALWAYS AS (parent_site_id IS NULL) STORED,
    -- Sites have two levels: the parent key below matches only a main site.
    parent_is_main boolean NOT NULL GENERATED ALWAYS AS (true) STORED,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    key_prefix text NOT NULL CHECK (key_prefix ~ '^[a-z0-9-]{1,15}-$'),
    registration text NOT NULL CHECK (registration IN ('invite', 'closed')), -- open waits for a CAPTCHA (M2)
    default_tier_id uuid NOT NULL,
    UNIQUE (id, is_main),
    CHECK ((parent_site_id IS NULL) = (owner_account_id IS NULL)),
    FOREIGN KEY (parent_site_id, parent_is_main) REFERENCES accounts.sites (id, is_main)
);
CREATE UNIQUE INDEX one_main_site ON accounts.sites (is_main) WHERE is_main;

-- domain is the request's Host, so it names one site across all of them. origin is what a state-changing request's
-- Origin must equal: https on the domain itself, or plain http on localhost for development.
CREATE TABLE accounts.site_domains (
    domain text PRIMARY KEY CHECK (domain = lower(domain) AND domain <> ''),
    site_id uuid NOT NULL REFERENCES accounts.sites,
    surface text NOT NULL CHECK (surface IN ('console', 'api', 'admin')),
    origin text NOT NULL CHECK (origin = 'https://' || domain OR origin ~ '^http://localhost(:[0-9]{1,5})?$'),
    disabled_at timestamptz
);

-- A null setting means the default in code. The bounds keep a typo from opening or locking out a site; the attempt
-- limits come with sign-in.
CREATE TABLE accounts.site_settings (
    site_id uuid PRIMARY KEY REFERENCES accounts.sites,
    console_idle_timeout interval CHECK (console_idle_timeout BETWEEN '5 minutes' AND '30 days'),
    console_max_age interval CHECK (console_max_age BETWEEN '5 minutes' AND '90 days'),
    admin_idle_timeout interval CHECK (admin_idle_timeout BETWEEN '5 minutes' AND '12 hours'),
    admin_max_age interval CHECK (admin_max_age BETWEEN '5 minutes' AND '1 day'),
    -- billionths of a credit (docs/domains/billing.md「钱的表示」); at most a million credits a day
    new_key_daily_limit bigint CHECK (new_key_daily_limit BETWEEN 0 AND 1000000000000000)
);

CREATE TABLE accounts.tiers (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL REFERENCES accounts.sites,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    UNIQUE (site_id, id)
);

-- Roles are platform data for now; sites' own roles come later. Permissions are names declared in code.
CREATE TABLE accounts.roles (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 100),
    permissions text[] NOT NULL,
    builtin boolean NOT NULL
);
-- The built-in roles, empty until the code declares the permissions they hold.
INSERT INTO accounts.roles (id, name, permissions, builtin)
SELECT uuidv7(), name, '{}', true FROM unnest(ARRAY['owner', 'admin', 'member', 'finance', 'viewer', 'support']) name;

-- (site_id, id, kind) is what a membership refers to, to know whether the account is personal.
CREATE TABLE accounts.accounts (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('personal', 'organization')),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    tier_id uuid NOT NULL,
    UNIQUE (site_id, id),
    UNIQUE (site_id, id, kind),
    FOREIGN KEY (site_id, tier_id) REFERENCES accounts.tiers (site_id, id) -- also keeps site_id a site
);

-- A secondary site's owner is an account of its parent. A site and its first tier refer to each other, so the
-- default tier is checked when the transaction that creates both commits.
ALTER TABLE accounts.sites
    ADD FOREIGN KEY (parent_site_id, owner_account_id) REFERENCES accounts.accounts (site_id, id),
    ADD FOREIGN KEY (id, default_tier_id) REFERENCES accounts.tiers (site_id, id) DEFERRABLE INITIALLY DEFERRED;

-- budget is in billionths of a credit; budget_reset null with a budget is a total that never resets.
CREATE TABLE accounts.workspaces (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    is_default boolean NOT NULL,
    budget bigint CHECK (budget >= 0),
    budget_reset text CHECK (budget_reset IS NULL OR budget_reset IN ('daily', 'weekly', 'monthly') AND budget IS NOT NULL),
    deleted_at timestamptz,
    UNIQUE (site_id, account_id, id),
    FOREIGN KEY (site_id, account_id) REFERENCES accounts.accounts (site_id, id),
    CONSTRAINT default_kept CHECK (NOT (is_default AND deleted_at IS NOT NULL))
);
CREATE UNIQUE INDEX one_default_workspace ON accounts.workspaces (account_id) WHERE is_default;

-- One policy per command, never FOR ALL: permissive policies are OR-ed, so a FOR ALL policy would let an UPDATE
-- through that a terminal-state policy refuses. Site data is in scope by its site, account data by its site or
-- account, roles only in the platform scope.
ALTER TABLE accounts.sites ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.sites FOR SELECT USING (platform.in_scope(id, NULL));
CREATE POLICY inserts ON accounts.sites FOR INSERT WITH CHECK (platform.in_scope(id, NULL));
CREATE POLICY updates ON accounts.sites FOR UPDATE USING (platform.in_scope(id, NULL))
    WITH CHECK (platform.in_scope(id, NULL));

ALTER TABLE accounts.site_domains ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.site_domains FOR SELECT USING (platform.in_scope(site_id, NULL));
CREATE POLICY inserts ON accounts.site_domains FOR INSERT WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY updates ON accounts.site_domains FOR UPDATE USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));

ALTER TABLE accounts.site_settings ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.site_settings FOR SELECT USING (platform.in_scope(site_id, NULL));
CREATE POLICY inserts ON accounts.site_settings FOR INSERT WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY updates ON accounts.site_settings FOR UPDATE USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));

ALTER TABLE accounts.tiers ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.tiers FOR SELECT USING (platform.in_scope(site_id, NULL));
CREATE POLICY inserts ON accounts.tiers FOR INSERT WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY updates ON accounts.tiers FOR UPDATE USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));

ALTER TABLE accounts.roles ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.roles FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON accounts.roles FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON accounts.roles FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());

ALTER TABLE accounts.accounts ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.accounts FOR SELECT USING (platform.in_scope(site_id, id));
CREATE POLICY inserts ON accounts.accounts FOR INSERT WITH CHECK (platform.in_scope(site_id, id));
CREATE POLICY updates ON accounts.accounts FOR UPDATE USING (platform.in_scope(site_id, id))
    WITH CHECK (platform.in_scope(site_id, id));

-- A deleted workspace is final: the old row must not be deleted yet, the new one may be, so the first soft delete
-- goes through and nothing changes the row after it, in any scope.
ALTER TABLE accounts.workspaces ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.workspaces FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.workspaces FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON accounts.workspaces FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND deleted_at IS NULL)
    WITH CHECK (platform.in_scope(site_id, account_id));

GRANT USAGE ON SCHEMA accounts TO axiom_app;
GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA accounts TO axiom_app;
GRANT UPDATE (name, status, key_prefix, registration, default_tier_id) ON accounts.sites TO axiom_app;
GRANT UPDATE (disabled_at) ON accounts.site_domains TO axiom_app;
GRANT UPDATE (console_idle_timeout, console_max_age, admin_idle_timeout, admin_max_age, new_key_daily_limit)
    ON accounts.site_settings TO axiom_app;
GRANT UPDATE (name) ON accounts.tiers TO axiom_app;
GRANT UPDATE (permissions) ON accounts.roles TO axiom_app;
GRANT UPDATE (name, status, tier_id) ON accounts.accounts TO axiom_app;
GRANT UPDATE (name, status, budget, budget_reset, deleted_at) ON accounts.workspaces TO axiom_app;
