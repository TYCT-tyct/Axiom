-- +goose Up

-- A cached reply is conversation content: the body and the turn state are sealed together with the platform keyring,
-- and every row is scoped to one account by RLS. cache_key is the SHA-256 of the account, format, streaming mode and
-- normalized request, never a raw request or a provider's identifier (docs/domains/gateway.md「响应缓存」).
GRANT USAGE ON SCHEMA gateway TO axiom_app;

CREATE TABLE gateway.response_cache (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    cache_key bytea NOT NULL CHECK (octet_length(cache_key) = 32),
    source_id uuid NOT NULL,
    format smallint NOT NULL CHECK (format BETWEEN 0 AND 255),
    model text NOT NULL CHECK (octet_length(model) <= 200),
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
    wrapped_dek bytea NOT NULL CHECK (octet_length(wrapped_dek) = 60),
    key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295),
    body_size integer NOT NULL CHECK (body_size BETWEEN 1 AND 1048576),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (expires_at <= created_at + interval '86400 seconds')
);

CREATE UNIQUE INDEX response_cache_key ON gateway.response_cache (account_id, cache_key);
CREATE INDEX response_cache_expiry ON gateway.response_cache (expires_at);
CREATE INDEX response_cache_account_age ON gateway.response_cache (account_id, created_at);

ALTER TABLE gateway.response_cache ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
-- The SELECT sits outside the row: the planner turns it into a statement InitPlan rather than parsing the scope for
-- every candidate row (platform/pg/migrations/00014_scope_once.sql).
CREATE POLICY reads ON gateway.response_cache FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY inserts ON gateway.response_cache FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY updates ON gateway.response_cache FOR UPDATE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY deletes ON gateway.response_cache FOR DELETE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);

GRANT SELECT, INSERT, UPDATE, DELETE ON gateway.response_cache TO axiom_app;

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON gateway.response_cache
FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

-- +goose Down
DROP TABLE IF EXISTS gateway.response_cache;
