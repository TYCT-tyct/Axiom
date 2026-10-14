-- +goose Up

-- Policies for data that is not a tenant's, such as the catalog's models and reference prices (public data) or its
-- audit and page snapshots (platform data): both are written only in the platform scope; public data is read in any
-- scope, platform data only in the platform scope. Like in_scope, no SET clause, so policies can inline them.

CREATE FUNCTION platform.in_platform_scope() RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN coalesce(current_setting('axiom.scope_platform', true), '') = 'on';

-- in_any_scope keeps public data from a transaction that set no scope, as in_scope does for tenant data. The casts
-- make a malformed setting fail the query instead of counting as a scope.
CREATE FUNCTION platform.in_any_scope() RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN platform.in_platform_scope()
        OR coalesce(cardinality(nullif(current_setting('axiom.scope_sites', true), '')::uuid[]), 0) > 0
        OR coalesce(cardinality(nullif(current_setting('axiom.scope_accounts', true), '')::uuid[]), 0) > 0;
