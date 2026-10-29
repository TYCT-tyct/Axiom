-- +goose Up

-- One latest request per address, including unknown addresses, keeps account lookup out of the response path.
-- The email is erased after processing; its digest and request ID keep a reissued link from reviving an old token.
CREATE TABLE accounts.password_reset_requests (
    site_id uuid NOT NULL REFERENCES accounts.sites,
    email_hash bytea NOT NULL CHECK (octet_length(email_hash) = 32),
    id uuid NOT NULL UNIQUE,
    email text,
    domain text NOT NULL,
    language text NOT NULL CHECK (language IN ('zh', 'en')),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 minutes'),
    PRIMARY KEY (site_id, email_hash)
);
CREATE TABLE accounts.password_reset_tokens (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    factor_version bigint NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);
ALTER TABLE accounts.password_reset_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.password_reset_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE accounts.password_reset_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.password_reset_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY request_select ON accounts.password_reset_requests FOR SELECT
    USING (platform.in_scope(site_id, NULL));
CREATE POLICY request_insert ON accounts.password_reset_requests FOR INSERT
    WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY request_update ON accounts.password_reset_requests FOR UPDATE
    USING (platform.in_scope(site_id, NULL)) WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY token_select ON accounts.password_reset_tokens FOR SELECT
    USING (platform.in_scope(site_id, account_id));
CREATE POLICY token_insert ON accounts.password_reset_tokens FOR INSERT
    WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY token_update ON accounts.password_reset_tokens FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND used_at IS NULL)
    WITH CHECK (platform.in_scope(site_id, account_id));
GRANT SELECT, INSERT ON accounts.password_reset_requests, accounts.password_reset_tokens TO axiom_app;
GRANT UPDATE (id, email, domain, language, expires_at) ON accounts.password_reset_requests TO axiom_app;
GRANT UPDATE (used_at) ON accounts.password_reset_tokens TO axiom_app;

-- This lookup reveals only a tenant for a secret on its own console site. Eligibility is checked again under locks.
GRANT SELECT (id, site_id, account_id, user_id, token_hash) ON accounts.password_reset_tokens TO axiom_lookup;
CREATE POLICY lookup ON accounts.password_reset_tokens FOR SELECT TO axiom_lookup USING (true);
-- +goose StatementBegin
CREATE FUNCTION accounts.password_reset_tenant(domain text, token_hash bytea)
    RETURNS TABLE (id uuid, user_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
        SELECT t.id, t.user_id, t.account_id FROM accounts.password_reset_tokens t
        WHERE t.token_hash = password_reset_tenant.token_hash
            AND t.site_id = accounts.domain_site(password_reset_tenant.domain, 'console')
    $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION accounts.password_reset_tenant FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.password_reset_tenant TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.password_reset_tenant OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;

GRANT SELECT (id, site_id) ON accounts.password_reset_requests TO axiom_lookup;
CREATE POLICY lookup ON accounts.password_reset_requests FOR SELECT TO axiom_lookup USING (true);
-- +goose StatementBegin
CREATE FUNCTION accounts.password_reset_request_tenant(id uuid)
    RETURNS TABLE (site_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
        SELECT r.site_id, NULL::uuid FROM accounts.password_reset_requests r
        WHERE r.id = password_reset_request_tenant.id
    $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION accounts.password_reset_request_tenant FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.password_reset_request_tenant TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.password_reset_request_tenant OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;

-- Expired credentials carry no replay protection and no longer need to retain personal data.
CREATE INDEX password_reset_requests_expiry ON accounts.password_reset_requests (expires_at);
CREATE INDEX password_reset_tokens_expiry ON accounts.password_reset_tokens (expires_at);
CREATE POLICY request_expiry ON accounts.password_reset_requests FOR DELETE
    USING (platform.in_scope(site_id, NULL) AND expires_at <= clock_timestamp());
CREATE POLICY token_expiry ON accounts.password_reset_tokens FOR DELETE
    USING (platform.in_scope(site_id, account_id) AND expires_at <= clock_timestamp());
GRANT DELETE ON accounts.password_reset_requests, accounts.password_reset_tokens TO axiom_app;
