-- +goose Up

-- Open registration is off until a site chooses it: the mode joins the two that existed, and the anti-abuse settings
-- are site data like every other setting, so a page opened before they existed cannot erase them.
ALTER TABLE accounts.sites DROP CONSTRAINT sites_registration_check;
ALTER TABLE accounts.sites ADD CONSTRAINT sites_registration_check
    CHECK (registration IN ('invite', 'closed', 'open'));

ALTER TABLE accounts.site_settings
    ADD COLUMN registration_human_check boolean,
    ADD COLUMN registration_human_check_bits integer
        CHECK (registration_human_check_bits BETWEEN 12 AND 24),
    ADD COLUMN registration_domain_burst integer
        CHECK (registration_domain_burst BETWEEN 1 AND 10000),
    ADD COLUMN registration_domain_per_hour integer
        CHECK (registration_domain_per_hour BETWEEN 1 AND 100000),
    ADD COLUMN registration_domain_allow text[]
        CHECK (registration_domain_allow IS NULL OR cardinality(registration_domain_allow) <= 100),
    ADD COLUMN registration_domain_deny text[]
        CHECK (registration_domain_deny IS NULL OR cardinality(registration_domain_deny) <= 100);
GRANT UPDATE (registration_human_check, registration_human_check_bits, registration_domain_burst,
    registration_domain_per_hour, registration_domain_allow, registration_domain_deny)
    ON accounts.site_settings TO axiom_app;

-- The email level is data from the first row, so a later rule can require it without guessing from the flow that made
-- the user. Invitations leave it empty; only a clicked verification link sets it.
ALTER TABLE accounts.users ADD COLUMN email_verified_at timestamptz;

-- A pending open registration is site data until its link is clicked: it owns no account yet, and its row must not
-- outlive its 24-hour link. No password is kept before the address is proved: the click supplies it, so an
-- unauthenticated caller cannot choose the password of an account the mailbox owner will later activate.
CREATE TABLE accounts.registration_requests (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL REFERENCES accounts.sites,
    email_hash bytea NOT NULL CHECK (octet_length(email_hash) = 32),
    email text NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
    tier_id uuid NOT NULL,
    domain text NOT NULL,
    language text NOT NULL CHECK (language IN ('zh', 'en')),
    token_hash bytea UNIQUE CHECK (token_hash IS NULL OR octet_length(token_hash) = 32),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
    FOREIGN KEY (site_id, tier_id) REFERENCES accounts.tiers (site_id, id),
    -- One pending registration per address on a site: a second request replaces the first rather than mailing twice.
    UNIQUE (site_id, email_hash)
);
CREATE INDEX registration_requests_expiry ON accounts.registration_requests (expires_at);

ALTER TABLE accounts.registration_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.registration_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.registration_requests FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON accounts.registration_requests FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY updates ON accounts.registration_requests FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
-- A clicked link is single-use: the row goes in the transaction that makes the user, and expired rows go in batches.
CREATE POLICY deletes ON accounts.registration_requests FOR DELETE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

GRANT SELECT, INSERT, DELETE ON accounts.registration_requests TO axiom_app;
-- A second request replaces the tier, language and expiry of the first; the token is written once by the worker.
-- The address and the site are not updatable.
GRANT UPDATE (tier_id, domain, language, expires_at, token_hash)
    ON accounts.registration_requests TO axiom_app;

-- The site a console domain registers on, when open registration is allowed there. The policy and the default tier
-- come from the database on every request, not from the thirty-second snapshot.
-- +goose StatementBegin
CREATE FUNCTION accounts.registration_site(domain text)
    RETURNS TABLE (site_id uuid, default_tier_id uuid, registration text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
        SELECT s.id, s.default_tier_id, s.registration FROM accounts.sites s
        WHERE s.id = accounts.domain_site(registration_site.domain, 'console') AND s.status = 'active'
    $$;
-- +goose StatementEnd

-- The pending registration a token names on its console domain. Eligibility is checked again under the row lock.
-- +goose StatementBegin
CREATE FUNCTION accounts.registration_tenant(domain text, token_hash bytea)
    RETURNS TABLE (id uuid, site_id uuid, email text, unexpired boolean)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
        SELECT r.id, r.site_id, r.email, r.expires_at > clock_timestamp()
        FROM accounts.registration_requests r
        JOIN accounts.sites s ON s.id = r.site_id AND s.status = 'active'
        WHERE r.token_hash = registration_tenant.token_hash
            AND r.site_id = accounts.domain_site(registration_tenant.domain, 'console')
    $$;
-- +goose StatementEnd

-- The tenant of a pending registration by its id, for the worker that issues the link and the transaction that
-- consumes it. A pending registration belongs to its site; it has no account until the link is clicked.
-- +goose StatementBegin
CREATE FUNCTION accounts.registration_request_tenant(id uuid)
    RETURNS TABLE (site_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
        SELECT r.site_id, NULL::uuid FROM accounts.registration_requests r
        WHERE r.id = registration_request_tenant.id
    $$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION accounts.registration_site, accounts.registration_tenant,
    accounts.registration_request_tenant FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.registration_site, accounts.registration_tenant,
    accounts.registration_request_tenant TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.registration_site OWNER TO axiom_lookup;
ALTER FUNCTION accounts.registration_tenant OWNER TO axiom_lookup;
ALTER FUNCTION accounts.registration_request_tenant OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;

GRANT SELECT (id, site_id, email, expires_at, token_hash) ON accounts.registration_requests TO axiom_lookup;
CREATE POLICY lookup ON accounts.registration_requests FOR SELECT TO axiom_lookup USING (true);

-- +goose Down
DROP FUNCTION accounts.registration_request_tenant(uuid);
DROP FUNCTION accounts.registration_tenant(text, bytea);
DROP FUNCTION accounts.registration_site(text);
DROP TABLE accounts.registration_requests;
ALTER TABLE accounts.users DROP COLUMN email_verified_at;
ALTER TABLE accounts.site_settings
    DROP COLUMN registration_domain_deny,
    DROP COLUMN registration_domain_allow,
    DROP COLUMN registration_domain_per_hour,
    DROP COLUMN registration_domain_burst,
    DROP COLUMN registration_human_check_bits,
    DROP COLUMN registration_human_check;
-- The previous release has no open mode. Keep sites usable after a rollback by taking them back to invite rather
-- than failing the down migration on the old constraint.
UPDATE accounts.sites SET registration = 'invite' WHERE registration = 'open';
ALTER TABLE accounts.sites DROP CONSTRAINT sites_registration_check;
ALTER TABLE accounts.sites ADD CONSTRAINT sites_registration_check
    CHECK (registration IN ('invite', 'closed'));
