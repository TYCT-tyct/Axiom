-- +goose Up

-- The sign-in side's lookups before a tenant is known (README.md「登录前的查找」), in the shape of 00002's: owned by
-- axiom_lookup, which reads only the columns they need, run only by axiom_app. Each answers only for the site of the
-- request's domain, when the domain and the site are enabled.

GRANT SELECT (registration, default_tier_id) ON accounts.sites TO axiom_lookup;
GRANT SELECT (id, site_id, account_id, email, invite_code, status) ON accounts.users TO axiom_lookup;
GRANT SELECT (id, site_id, account_id, user_id, surface, token_hash) ON accounts.sessions TO axiom_lookup;
CREATE POLICY lookup ON accounts.users FOR SELECT TO axiom_lookup USING (true);
CREATE POLICY lookup ON accounts.sessions FOR SELECT TO axiom_lookup USING (true);

-- The session whose token hashes to token_hash, issued for surface on the domain's site.
-- +goose StatementBegin
CREATE FUNCTION accounts.session_tenant(domain text, surface text, token_hash bytea)
    RETURNS TABLE (session_id uuid, user_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
    AS $$
        SELECT s.id, s.user_id, s.account_id FROM accounts.sessions s
        WHERE s.token_hash = session_tenant.token_hash AND s.surface = session_tenant.surface
            AND s.site_id = accounts.domain_site(session_tenant.domain, session_tenant.surface)
    $$;
-- +goose StatementEnd

-- The user with email on the site of a console or admin domain.
-- +goose StatementBegin
CREATE FUNCTION accounts.login_tenant(domain text, email text)
    RETURNS TABLE (user_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
    AS $$
        SELECT u.id, u.account_id FROM accounts.users u
        WHERE u.email = login_tenant.email AND u.site_id IN (
            accounts.domain_site(login_tenant.domain, 'console'), accounts.domain_site(login_tenant.domain, 'admin'))
    $$;
-- +goose StatementEnd

-- The enabled user whose invite code is code on the site of a console domain, with the site's default tier for the
-- new account, when the site registers by invitation.
-- +goose StatementBegin
CREATE FUNCTION accounts.invite_tenant(domain text, code text)
    RETURNS TABLE (site_id uuid, inviter_user_id uuid, default_tier_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
    AS $$
        SELECT s.id, u.id, s.default_tier_id FROM accounts.sites s
        JOIN accounts.users u ON u.site_id = s.id
        WHERE s.id = accounts.domain_site(invite_tenant.domain, 'console') AND s.registration = 'invite'
            AND u.invite_code = invite_tenant.code AND u.status = 'active'
    $$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION accounts.session_tenant, accounts.login_tenant, accounts.invite_tenant FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.session_tenant, accounts.login_tenant, accounts.invite_tenant TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.session_tenant OWNER TO axiom_lookup;
ALTER FUNCTION accounts.login_tenant OWNER TO axiom_lookup;
ALTER FUNCTION accounts.invite_tenant OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;

-- The built-in roles' permissions, as the code declares them (authorize.go). The program refuses to start on a role
-- holding a permission it does not declare. Forced row-level security binds the owner too, and a migration sets no
-- scope, so the table's owner writes the rows with FORCE lifted inside this migration's transaction.
ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY;
UPDATE accounts.roles SET permissions = CASE name
    WHEN 'owner' THEN '{api_keys.view,api_keys.manage,workspaces.manage}'
    WHEN 'admin' THEN '{api_keys.view,api_keys.manage,workspaces.manage,sites.manage,platform_grants.manage}'
    WHEN 'member' THEN '{api_keys.view,api_keys.manage}'
    ELSE '{api_keys.view}'
END::text[]
WHERE builtin;
ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY;
