-- +goose Up

-- Lookups before a tenant is known (internal/accounts/README.md「登录前的查找」): definer functions owned by
-- axiom_lookup turn a key's hash or a request's domain into identifiers. The role reads, through policies of its
-- own, only the columns the functions need; it cannot log in, and axiom_app may run the functions but not become it.
-- The session, sign-in and invitation lookups come with sign-in.

GRANT USAGE ON SCHEMA accounts TO axiom_lookup;
GRANT SELECT (id, status) ON accounts.sites TO axiom_lookup;
GRANT SELECT (domain, site_id, surface, disabled_at) ON accounts.site_domains TO axiom_lookup;
GRANT SELECT (id, site_id, account_id, workspace_id, secret_hash, deleted_at) ON accounts.api_keys TO axiom_lookup;
CREATE POLICY lookup ON accounts.sites FOR SELECT TO axiom_lookup USING (true);
CREATE POLICY lookup ON accounts.site_domains FOR SELECT TO axiom_lookup USING (true);
CREATE POLICY lookup ON accounts.api_keys FOR SELECT TO axiom_lookup USING (true);

-- The key with one of hashes, the key's hash under each version of the hashing key, on site. A key of another
-- site, a deleted key and no key at all give the same empty result.
-- +goose StatementBegin
CREATE FUNCTION accounts.key_tenant(site uuid, hashes bytea[])
    RETURNS TABLE (key_id uuid, account_id uuid, workspace_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
    AS $$
        SELECT k.id, k.account_id, k.workspace_id FROM accounts.api_keys k
        WHERE k.site_id = key_tenant.site AND k.secret_hash = ANY (key_tenant.hashes) AND k.deleted_at IS NULL
        LIMIT 1
    $$;
-- +goose StatementEnd

-- The site that serves domain on surface, if the domain and the site are both enabled; checked in the database on
-- every call, never from the snapshot.
CREATE FUNCTION accounts.domain_site(domain text, surface text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
    RETURN (SELECT d.site_id FROM accounts.site_domains d JOIN accounts.sites s ON s.id = d.site_id
        WHERE d.domain = domain_site.domain AND d.surface = domain_site.surface AND d.disabled_at IS NULL
            AND s.status = 'active');

-- Only the application runs them. Handing a function to axiom_lookup takes CREATE on the schema for the new owner,
-- granted for that statement alone.
REVOKE EXECUTE ON FUNCTION accounts.key_tenant, accounts.domain_site FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.key_tenant, accounts.domain_site TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.key_tenant OWNER TO axiom_lookup;
ALTER FUNCTION accounts.domain_site OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;
