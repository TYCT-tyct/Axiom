-- +goose Up

-- The platform's egress proxies (docs/domains/suppliers.md「出口代理」): a credential whose egress_id names one sends its
-- calls and its checks through it; NULL is direct. The id is the text credentials.egress_id holds; 'direct' is the exit
-- without a proxy, never a row. The password is sealed like every other secret (purpose 'password', this row's id), all
-- four envelope columns or none. A proxy that is disabled hands its credentials to its fallback. A chain is one hop: a
-- fallback has no fallback of its own and is no proxy's own, which the admin's writes check under one lock, and a node
-- follows one hop only, so no chain returns to where it began.
CREATE TABLE suppliers.egress_proxies (
    id text PRIMARY KEY CONSTRAINT egress_proxies_id_check CHECK (id ~ '^[a-z0-9][a-z0-9_-]{0,31}$' AND id <> 'direct'),
    name text NOT NULL CONSTRAINT egress_proxies_name_check CHECK (char_length(name) BETWEEN 1 AND 200),
    scheme text NOT NULL CONSTRAINT egress_proxies_scheme_check CHECK (scheme IN ('http', 'https', 'socks5')),
    host text NOT NULL CONSTRAINT egress_proxies_host_check CHECK (host ~ '^[A-Za-z0-9.:-]{1,253}$'),
    port integer NOT NULL CONSTRAINT egress_proxies_port_check CHECK (port BETWEEN 1 AND 65535),
    username text CONSTRAINT egress_proxies_username_check CHECK (char_length(username) BETWEEN 1 AND 255),
    ciphertext bytea CONSTRAINT egress_proxies_ciphertext_check CHECK (octet_length(ciphertext) >= 16),
    nonce bytea CONSTRAINT egress_proxies_nonce_check CHECK (octet_length(nonce) = 12),
    wrapped_dek bytea CONSTRAINT egress_proxies_wrapped_dek_check CHECK (octet_length(wrapped_dek) > 0),
    key_version bigint CONSTRAINT egress_proxies_key_version_check CHECK (key_version BETWEEN 1 AND 4294967295),
    fallback_id text REFERENCES suppliers.egress_proxies (id),
    status text NOT NULL DEFAULT 'active' CONSTRAINT egress_proxies_status_check CHECK (status IN ('active', 'disabled')),
    CONSTRAINT egress_proxies_sealed_together CHECK (
        (ciphertext IS NULL) = (nonce IS NULL) AND (ciphertext IS NULL) = (wrapped_dek IS NULL)
        AND (ciphertext IS NULL) = (key_version IS NULL)),
    CONSTRAINT egress_proxies_password_has_user CHECK (ciphertext IS NULL OR username IS NOT NULL),
    CONSTRAINT egress_proxies_not_own_fallback CHECK (fallback_id <> id)
);

ALTER TABLE suppliers.egress_proxies OWNER TO axiom_owner;

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON suppliers.egress_proxies
    FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE POLICY platform_only ON suppliers.egress_proxies USING ((SELECT platform.in_platform_scope()));
ALTER TABLE suppliers.egress_proxies ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppliers.egress_proxies FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, DELETE ON TABLE suppliers.egress_proxies TO axiom_app;
GRANT UPDATE (name, ciphertext, nonce, wrapped_dek, key_version, username, fallback_id, status)
    ON TABLE suppliers.egress_proxies TO axiom_app;

-- The column was never written or read before this: every credential went direct, which NULL now says.
UPDATE suppliers.credentials SET egress_id = NULL WHERE egress_id IS NOT NULL;
ALTER TABLE suppliers.credentials
    DROP CONSTRAINT credentials_egress_id_check,
    ADD CONSTRAINT credentials_egress_id_fkey FOREIGN KEY (egress_id) REFERENCES suppliers.egress_proxies (id);
CREATE INDEX credentials_egress ON suppliers.credentials (egress_id) WHERE egress_id IS NOT NULL;
