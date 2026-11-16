-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

CREATE TABLE gateway.response_cache (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    cache_key bytea NOT NULL,
    source_id uuid NOT NULL,
    format smallint NOT NULL,
    model text NOT NULL,
    endpoint uuid NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    body_size integer NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CONSTRAINT response_cache_body_size_check CHECK (((body_size >= 1) AND (body_size <= 1048576))),
    CONSTRAINT response_cache_cache_key_check CHECK ((octet_length(cache_key) = 32)),
    CONSTRAINT response_cache_check CHECK ((expires_at > created_at)),
    CONSTRAINT response_cache_check1 CHECK ((expires_at <= (created_at + '24:00:00'::interval))),
    CONSTRAINT response_cache_format_check CHECK (((format >= 0) AND (format <= 255))),
    CONSTRAINT response_cache_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT response_cache_model_check CHECK ((octet_length(model) <= 200)),
    CONSTRAINT response_cache_nonce_check CHECK ((octet_length(nonce) = 12)),
    CONSTRAINT response_cache_wrapped_dek_check CHECK ((octet_length(wrapped_dek) = 60))
);



ALTER TABLE gateway.response_cache OWNER TO axiom_owner;

ALTER TABLE ONLY gateway.response_cache
    ADD CONSTRAINT response_cache_pkey PRIMARY KEY (id);

CREATE INDEX response_cache_account_age ON gateway.response_cache USING btree (account_id, created_at);

CREATE INDEX response_cache_expiry ON gateway.response_cache USING btree (expires_at);

CREATE UNIQUE INDEX response_cache_key ON gateway.response_cache USING btree (account_id, cache_key);

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON gateway.response_cache FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE POLICY deletes ON gateway.response_cache FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON gateway.response_cache FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON gateway.response_cache FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));


CREATE POLICY updates ON gateway.response_cache FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

GRANT USAGE ON SCHEMA gateway TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE gateway.response_cache TO axiom_app;



ALTER TABLE ONLY gateway.response_cache FORCE ROW LEVEL SECURITY;
ALTER TABLE gateway.response_cache ENABLE ROW LEVEL SECURITY;
