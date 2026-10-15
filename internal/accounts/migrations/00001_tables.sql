-- +goose Up

-- Every accounts table (internal/accounts/README.md「数据」): sites with their domains, settings and tiers, the
-- platform's roles, accounts, workspaces, users, members, platform grants, API keys, sessions and recovery codes.
-- Every tenant row carries its site_id, and account rows their account_id, which no grant lets the application
-- change; composite foreign keys keep them in line with the rows above, so a policy reads only its own
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

-- A user signs in on one site and owns one personal account there, which no other user has: account_id is unique
-- and account_kind, always personal, is part of its key, so two sign-ins never share an account's scope. The secret is a secret.Sealed spread over
-- four columns, all set or none, so a key rotation rewraps the data key in place. A user's invite code is theirs;
-- the inviter is another user of the same site, fixed at registration.
CREATE TABLE accounts.users (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL UNIQUE,
    account_kind text NOT NULL GENERATED ALWAYS AS ('personal') STORED,
    email text NOT NULL CHECK (email = lower(email) AND char_length(email) BETWEEN 3 AND 254 AND email LIKE '%_@_%'),
    password_hash text NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    factor_version bigint NOT NULL CHECK (factor_version >= 1),
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    invite_code text NOT NULL CHECK (invite_code ~ '^[a-z0-9]{6,32}$'),
    inviter_user_id uuid CHECK (inviter_user_id <> id),
    totp_ciphertext bytea,
    totp_nonce bytea CHECK (octet_length(totp_nonce) = 12),
    totp_wrapped_dek bytea CHECK (octet_length(totp_wrapped_dek) = 60),
    totp_key_version bigint CHECK (totp_key_version BETWEEN 1 AND 4294967295),
    totp_last_step bigint NOT NULL DEFAULT 0 CHECK (totp_last_step >= 0),
    mfa_enabled_at timestamptz,
    UNIQUE (site_id, id),
    UNIQUE (site_id, account_id, id),
    UNIQUE (site_id, email),
    UNIQUE (site_id, invite_code),
    FOREIGN KEY (site_id, account_id, account_kind) REFERENCES accounts.accounts (site_id, id, kind),
    FOREIGN KEY (site_id, inviter_user_id) REFERENCES accounts.users (site_id, id),
    CHECK (num_nulls(totp_ciphertext, totp_nonce, totp_wrapped_dek, totp_key_version) IN (0, 4))
);

-- A user's role in an account. The member is a user of the account's site, reached through the user's own account,
-- and in a personal account only its owner: personal_owner is the account when it is personal, and must then be
-- the member's own account, which belongs to that user alone. The account's kind is carried as part of the key to
-- know which applies.
CREATE TABLE accounts.members (
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    account_kind text NOT NULL,
    user_id uuid NOT NULL,
    user_account_id uuid NOT NULL,
    role_id uuid NOT NULL REFERENCES accounts.roles,
    personal_owner uuid GENERATED ALWAYS AS (CASE WHEN account_kind = 'personal' THEN account_id END) STORED,
    PRIMARY KEY (account_id, user_id),
    FOREIGN KEY (site_id, account_id, account_kind) REFERENCES accounts.accounts (site_id, id, kind),
    FOREIGN KEY (site_id, user_account_id, user_id) REFERENCES accounts.users (site_id, account_id, id),
    FOREIGN KEY (site_id, personal_owner, user_id) REFERENCES accounts.users (site_id, account_id, id)
);

-- A platform role, held only by a user of the main site: site_is_main must match the site's is_main. It is the
-- grantee's account data that only the platform scope writes. granted_by is null when the command line granted it.
CREATE TABLE accounts.platform_grants (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    site_is_main boolean NOT NULL GENERATED ALWAYS AS (true) STORED,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role_id uuid NOT NULL REFERENCES accounts.roles,
    granted_by uuid REFERENCES accounts.users,
    revoked_at timestamptz,
    FOREIGN KEY (site_id, site_is_main) REFERENCES accounts.sites (id, is_main),
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);

-- Only the key's hash is kept, with the version of the hashing key that made it; prefix is copied from the site, so
-- changing the site's prefix leaves issued keys alone. created_by is a user of the key's site. Money is in
-- billionths of a credit, and limit_reset null with a limit is a total that never resets. preset_id names a routing
-- preset, which routing owns.
CREATE TABLE accounts.api_keys (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('call', 'management')),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    prefix text NOT NULL CHECK (prefix ~ '^[a-z0-9-]{1,15}-$'),
    last4 text NOT NULL CHECK (last4 ~ '^[A-Za-z0-9]{4}$'),
    secret_hash bytea NOT NULL UNIQUE CHECK (octet_length(secret_hash) = 32),
    secret_version bigint NOT NULL CHECK (secret_version >= 1),
    created_by uuid NOT NULL,
    expires_at timestamptz,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    spend_limit bigint CHECK (spend_limit >= 0),
    limit_reset text CHECK (limit_reset IS NULL OR limit_reset IN ('daily', 'weekly', 'monthly') AND spend_limit IS NOT NULL),
    preset_id uuid,
    models text[],
    ip_allow cidr[],
    deleted_at timestamptz,
    FOREIGN KEY (site_id, account_id, workspace_id) REFERENCES accounts.workspaces (site_id, account_id, id),
    FOREIGN KEY (site_id, created_by) REFERENCES accounts.users (site_id, id),
    CHECK (kind = 'call' OR expires_at IS NOT NULL)
);

-- A sign-in on one surface; only the token's SHA-256 is kept. It holds while factor_version equals the user's.
CREATE TABLE accounts.sessions (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    surface text NOT NULL CHECK (surface IN ('console', 'admin')),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    csrf_token text NOT NULL CHECK (char_length(csrf_token) BETWEEN 32 AND 128),
    level text NOT NULL CHECK (level IN ('pending_mfa', 'password', 'mfa')),
    factor_version bigint NOT NULL,
    mfa_attempts integer NOT NULL DEFAULT 0 CHECK (mfa_attempts >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    idle_timeout interval NOT NULL,
    revoked_at timestamptz,
    ip inet,
    user_agent text CHECK (char_length(user_agent) <= 512),
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);

-- One-time codes, only their SHA-256 kept, good while factor_version equals the user's. The unique key is also how
-- a code is found when it is spent.
CREATE TABLE accounts.recovery_codes (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    factor_version bigint NOT NULL,
    code_hash bytea NOT NULL CHECK (octet_length(code_hash) = 32),
    used_at timestamptz,
    UNIQUE (user_id, code_hash),
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);

-- One policy per command, never FOR ALL: permissive policies are OR-ed, so a FOR ALL policy would let an UPDATE
-- through that a terminal-state policy refuses. Site data is in scope by its site, account data by its site or
-- account, roles only in the platform scope. A table with a terminal column (deleted_at, revoked_at, used_at) updates
-- only rows not yet in it: the first change into it goes through, and nothing changes the row after, in any scope.
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

ALTER TABLE accounts.workspaces ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.workspaces FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.workspaces FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON accounts.workspaces FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND deleted_at IS NULL)
    WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE accounts.users ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.users FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.users FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON accounts.users FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));

-- Members have nothing to update until organization accounts bring removed_at, so no UPDATE policy either.
ALTER TABLE accounts.members ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.members FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.members FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));

-- The grantee's scope reads a grant; only the platform scope, which holds every account, writes one.
ALTER TABLE accounts.platform_grants ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.platform_grants FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.platform_grants FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON accounts.platform_grants FOR UPDATE USING (platform.in_platform_scope() AND revoked_at IS NULL)
    WITH CHECK (platform.in_platform_scope());

ALTER TABLE accounts.api_keys ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.api_keys FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.api_keys FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON accounts.api_keys FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND deleted_at IS NULL)
    WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE accounts.sessions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.sessions FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.sessions FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON accounts.sessions FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND revoked_at IS NULL)
    WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE accounts.recovery_codes ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.recovery_codes FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.recovery_codes FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON accounts.recovery_codes FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND used_at IS NULL)
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
GRANT UPDATE (password_hash, factor_version, status, totp_ciphertext, totp_nonce, totp_wrapped_dek, totp_key_version,
    totp_last_step, mfa_enabled_at) ON accounts.users TO axiom_app;
GRANT UPDATE (revoked_at) ON accounts.platform_grants TO axiom_app;
GRANT UPDATE (name, status, spend_limit, limit_reset, preset_id, models, ip_allow, deleted_at) ON accounts.api_keys
    TO axiom_app;
GRANT UPDATE (last_seen_at, mfa_attempts, revoked_at) ON accounts.sessions TO axiom_app;
GRANT UPDATE (used_at) ON accounts.recovery_codes TO axiom_app;
