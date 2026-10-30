-- +goose Up

-- Third-party sign-in and passkeys are site features that are off until the site's owner configures them. The login
-- domain is the one domain passkeys are bound to and OAuth providers redirect back to: one per site, chosen when the
-- site is made, so that adding or renaming a console domain cannot move the relying party out from under every
-- registered credential.
ALTER TABLE accounts.sites
    ADD COLUMN login_domain text CHECK (login_domain IS NULL OR (login_domain = lower(login_domain) AND login_domain <> ''));

-- Sites made before this migration keep their first HTTPS console domain, which is what the snapshot used to pick.
UPDATE accounts.sites s SET login_domain = d.domain
FROM (
    SELECT DISTINCT ON (site_id) site_id, domain
    FROM accounts.site_domains
    WHERE surface = 'console' AND disabled_at IS NULL
    ORDER BY site_id, (origin LIKE 'https://%') DESC, origin
) d
WHERE d.site_id = s.id;

-- The domain must belong to this site, not merely to some site: the relying party and callback depend on both.
ALTER TABLE accounts.site_domains ADD CONSTRAINT site_domains_site_domain_key UNIQUE (site_id, domain);
ALTER TABLE accounts.sites ADD CONSTRAINT sites_login_domain_fk
    FOREIGN KEY (id, login_domain) REFERENCES accounts.site_domains (site_id, domain);
GRANT UPDATE (login_domain) ON accounts.sites TO axiom_app;

-- Off unless the site turns it on. A site without a login domain cannot turn it on: the relying party ID and the
-- ceremony's origin both come from that domain.
ALTER TABLE accounts.site_settings
    ADD COLUMN passkeys_enabled boolean;
GRANT UPDATE (passkeys_enabled) ON accounts.site_settings TO axiom_app;

-- One row per provider and site: the OAuth application the site's owner registered with GitHub, Google or LinuxDO.
-- The client ID is public (it is in every authorization URL); the secret is sealed like every other credential, and
-- its location is the row's id so the master key rotation can rewrap it.
CREATE TABLE accounts.site_oauth_providers (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL REFERENCES accounts.sites,
    provider text NOT NULL CHECK (provider IN ('github', 'google', 'linuxdo')),
    client_id text NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 255),
    client_secret_ciphertext bytea NOT NULL,
    client_secret_nonce bytea NOT NULL,
    client_secret_wrapped_dek bytea NOT NULL,
    client_secret_key_version integer NOT NULL CHECK (client_secret_key_version BETWEEN 1 AND 2147483647),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, provider)
);

ALTER TABLE accounts.site_oauth_providers ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.site_oauth_providers FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.site_oauth_providers FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON accounts.site_oauth_providers FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY updates ON accounts.site_oauth_providers FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY deletes ON accounts.site_oauth_providers FOR DELETE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

GRANT SELECT, INSERT, DELETE ON accounts.site_oauth_providers TO axiom_app;
GRANT UPDATE (client_id, client_secret_ciphertext, client_secret_nonce, client_secret_wrapped_dek,
    client_secret_key_version, updated_at) ON accounts.site_oauth_providers TO axiom_app;

-- A provider identity linked to a local user while that user was signed in. The provider's subject is the only match:
-- an email address is never used to attach an identity to an account that already exists
-- (docs/domains/accounts.md「不自动合并身份」). One identity of a provider per user, so the provider is a sign-in method
-- rather than a set of aliases.
CREATE TABLE accounts.user_identities (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider IN ('github', 'google', 'linuxdo')),
    subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 255),
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, provider, subject),
    UNIQUE (user_id, provider),
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);

ALTER TABLE accounts.user_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.user_identities FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.user_identities FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY inserts ON accounts.user_identities FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
-- Unlinking deletes the row; the audit entry keeps the fact. A link that comes back is a new row.
CREATE POLICY deletes ON accounts.user_identities FOR DELETE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);

GRANT SELECT, INSERT, DELETE ON accounts.user_identities TO axiom_app;

-- A public-key credential. credential_id is the WebAuthn credential ID; sign-in begins before a user is known, so
-- looking one up needs a defined function (below). The signature counter is the authenticator's; a credential that
-- reports a counter that went backwards is refused at sign-in.
CREATE TABLE accounts.passkey_credentials (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    credential_id bytea NOT NULL UNIQUE CHECK (octet_length(credential_id) BETWEEN 1 AND 1023),
    public_key bytea NOT NULL CHECK (octet_length(public_key) BETWEEN 1 AND 1024),
    sign_count bigint NOT NULL DEFAULT 0 CHECK (sign_count BETWEEN 0 AND 4294967295),
    transports text[] NOT NULL DEFAULT '{}',
    backup_eligible boolean NOT NULL DEFAULT false,
    backup_state boolean NOT NULL DEFAULT false,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);

CREATE INDEX passkey_credentials_user ON accounts.passkey_credentials (user_id);

ALTER TABLE accounts.passkey_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.passkey_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.passkey_credentials FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY inserts ON accounts.passkey_credentials FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY updates ON accounts.passkey_credentials FOR UPDATE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY deletes ON accounts.passkey_credentials FOR DELETE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);

GRANT SELECT, INSERT, DELETE ON accounts.passkey_credentials TO axiom_app;
-- A counter only moves forward; last_used_at and the backup state are the authenticator's report, the name is the
-- user's label. The public key and the credential ID never change.
GRANT UPDATE (sign_count, last_used_at, backup_state, name) ON accounts.passkey_credentials TO axiom_app;

-- A sign-in or link round trip through a provider. The state is kept hashed and single use; the verifier is the PKCE
-- secret and never leaves the database before the code exchange. A link flow is bound to the signed-in session that
-- began it, so a state leaked into another browser cannot attach a provider identity to that user.
CREATE TABLE accounts.oauth_flows (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL REFERENCES accounts.sites,
    provider text NOT NULL CHECK (provider IN ('github', 'google', 'linuxdo')),
    intent text NOT NULL CHECK (intent IN ('sign_in', 'link')),
    account_id uuid,
    user_id uuid,
    session_id uuid,
    state_hash bytea NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    nonce_hash bytea CHECK (nonce_hash IS NULL OR octet_length(nonce_hash) = 32),
    verifier text NOT NULL CHECK (char_length(verifier) BETWEEN 32 AND 128),
    redirect_uri text NOT NULL CHECK (char_length(redirect_uri) BETWEEN 1 AND 512),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK ((intent = 'link') = (user_id IS NOT NULL)),
    CHECK ((intent = 'link') = (account_id IS NOT NULL)),
    CHECK ((intent = 'link') = (session_id IS NOT NULL)),
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);
CREATE INDEX oauth_flows_expiry ON accounts.oauth_flows (expires_at);

ALTER TABLE accounts.oauth_flows ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.oauth_flows FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.oauth_flows FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON accounts.oauth_flows FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY updates ON accounts.oauth_flows FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY deletes ON accounts.oauth_flows FOR DELETE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

GRANT SELECT, INSERT, DELETE ON accounts.oauth_flows TO axiom_app;
GRANT UPDATE (consumed_at) ON accounts.oauth_flows TO axiom_app;

-- A WebAuthn ceremony between begin and finish. It holds the challenge, the user handle and the credentials the
-- authenticator was offered; the ceremony is single use and expires in five minutes. A sign-in ceremony has no user
-- until the assertion names one.
CREATE TABLE accounts.webauthn_flows (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL REFERENCES accounts.sites,
    kind text NOT NULL CHECK (kind IN ('register', 'sign_in', 'mfa')),
    account_id uuid,
    user_id uuid,
    session_id uuid,
    challenge text NOT NULL CHECK (char_length(challenge) BETWEEN 16 AND 256),
    name text NOT NULL DEFAULT '' CHECK (char_length(name) <= 100),
    allowed_credential_ids bytea[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK ((kind = 'sign_in') = (user_id IS NULL)),
    CHECK ((kind = 'sign_in') = (account_id IS NULL)),
    CHECK ((kind = 'sign_in') = (session_id IS NULL)),
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);
CREATE INDEX webauthn_flows_expiry ON accounts.webauthn_flows (expires_at);

ALTER TABLE accounts.webauthn_flows ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.webauthn_flows FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.webauthn_flows FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON accounts.webauthn_flows FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY updates ON accounts.webauthn_flows FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY deletes ON accounts.webauthn_flows FOR DELETE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

GRANT SELECT, INSERT, DELETE ON accounts.webauthn_flows TO axiom_app;
GRANT UPDATE (consumed_at) ON accounts.webauthn_flows TO axiom_app;

-- The tenant of a credential named by a discoverable sign-in, before the caller is known. The relying party matches
-- the credential to the site of the login domain the ceremony ran on.
-- +goose StatementBegin
CREATE FUNCTION accounts.passkey_tenant(credential_id bytea)
    RETURNS TABLE (site_id uuid, account_id uuid, user_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
        SELECT c.site_id, c.account_id, c.user_id FROM accounts.passkey_credentials c
        WHERE c.credential_id = passkey_tenant.credential_id
    $$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION accounts.passkey_tenant(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.passkey_tenant(bytea) TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.passkey_tenant(bytea) OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;

GRANT SELECT (credential_id, site_id, account_id, user_id) ON accounts.passkey_credentials TO axiom_lookup;
CREATE POLICY lookup ON accounts.passkey_credentials FOR SELECT TO axiom_lookup USING (true);

-- +goose Down
DROP FUNCTION accounts.passkey_tenant(bytea);
DROP TABLE accounts.webauthn_flows;
DROP TABLE accounts.oauth_flows;
DROP TABLE accounts.passkey_credentials;
DROP TABLE accounts.user_identities;
DROP TABLE accounts.site_oauth_providers;
ALTER TABLE accounts.site_settings DROP COLUMN passkeys_enabled;
ALTER TABLE accounts.sites DROP CONSTRAINT sites_login_domain_fk;
ALTER TABLE accounts.sites DROP COLUMN login_domain;
ALTER TABLE accounts.site_domains DROP CONSTRAINT site_domains_site_domain_key;
