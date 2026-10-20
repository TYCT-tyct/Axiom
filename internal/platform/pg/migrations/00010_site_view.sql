-- +goose Up

-- sees_site is 「范围的站点等于该行的站点」 of site public configuration and shared counters (docs/security.md「租户隔离」):
-- the platform, a scope listing the site, or an account scope whose home site it is. pg sets the home site only from
-- the authorization that found the account, so no row an account can write, such as its own registration in
-- platform.tenants, can widen it. Like in_scope, no SET clause, so policies can inline it; a malformed setting fails
-- the query, and no home site is false, not NULL.
CREATE FUNCTION platform.sees_site(site uuid) RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN coalesce(platform.in_scope(site, NULL)
        OR site = nullif(current_setting('axiom.scope_home', true), '')::uuid, false);
