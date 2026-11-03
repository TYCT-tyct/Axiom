-- +goose Up

-- Conversation content storage and its controlled export (docs/domains/observability.md「存储」「受控导出」).
--
-- A blob is one message or file body, stored once per account and addressed by a keyed digest; a ref is one place a
-- call used it. Splitting them keeps the programming-tool pattern (every turn resends the whole history) from storing
-- the same bytes again, without letting one account see another's digest. The body itself is in the object store,
-- encrypted with the account's data key; no column here holds plaintext, and the application role can only read the
-- metadata in the platform scope.
CREATE TABLE observability.content_keys (
    account_id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    -- The account's 32-byte data key, sealed by the platform keyring. Every body of the account is encrypted with a
    -- subkey derived from it, so destroying this row makes all of it unreadable at once.
    ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) = 48),
    nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
    -- A distinct column name keeps the old binary's envelope inventory (which looks for wrapped_dek) able to start
    -- during a rolling upgrade; the new inventory registers this column and rotates it.
    wrapped_key bytea NOT NULL CHECK (octet_length(wrapped_key) = 60),
    key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295),
    created_at timestamptz NOT NULL
);

CREATE TABLE observability.content_blobs (
    id uuid NOT NULL PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    -- HMAC-SHA256 of the plaintext under the account's digest key: equal bodies deduplicate, and a bucket listing
    -- cannot confirm whether a guessed body is present.
    digest bytea NOT NULL CHECK (octet_length(digest) = 32),
    object_key text NOT NULL CHECK (char_length(object_key) BETWEEN 1 AND 512),
    size bigint NOT NULL CHECK (size BETWEEN 1 AND 67108864),
    created_at timestamptz NOT NULL,
    last_referenced_at timestamptz NOT NULL,
    -- Null means long-term retention. The retention job resolves the effective expiry from the platform setting and
    -- the references; the writer records the value it used.
    expires_at timestamptz,
    CHECK (last_referenced_at >= created_at),
    CHECK (expires_at IS NULL OR expires_at > created_at),
    UNIQUE (account_id, digest),
    UNIQUE (object_key),
    -- The reference's foreign key names the whole tenant, so a ref can never point at another account's blob.
    UNIQUE (id, site_id, account_id)
);
CREATE INDEX content_blobs_expiry ON observability.content_blobs (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX content_blobs_account_age ON observability.content_blobs (account_id, last_referenced_at);

CREATE TABLE observability.content_refs (
    request_id uuid NOT NULL,
    -- One message, file or other body within the request. The same request therefore has many refs for one kind,
    -- and the export path includes this component so tar entries cannot overwrite each other.
    part text NOT NULL CHECK (part ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    kind text NOT NULL CHECK (kind IN ('request', 'response', 'attempt')),
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    blob_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (request_id, kind, part),
    FOREIGN KEY (blob_id, site_id, account_id)
        REFERENCES observability.content_blobs (id, site_id, account_id)
);
CREATE INDEX content_refs_export ON observability.content_refs (created_at, account_id, request_id, kind, part);
CREATE INDEX content_refs_account_age ON observability.content_refs (account_id, created_at, request_id, kind, part);

-- One row per export attempt: the signed approval, its scope and output, and the outcome. It is the audit record the
-- command writes before it reads a byte of content, so an interrupted export still leaves evidence. The unique digest
-- makes an owner's approval single-use.
CREATE TABLE observability.content_exports (
    id uuid PRIMARY KEY,
    approval_digest bytea NOT NULL UNIQUE CHECK (octet_length(approval_digest) = 32),
    owner_key text NOT NULL CHECK (owner_key ~ '^[0-9a-f]{64}$'),
    owner2_key text NOT NULL CHECK (owner2_key ~ '^[0-9a-f]{64}$'),
    recipient_key text NOT NULL CHECK (recipient_key ~ '^[0-9a-f]{64}$'),
    since timestamptz NOT NULL,
    until timestamptz NOT NULL,
    site_id uuid,
    account_id uuid,
    destination text NOT NULL CHECK (destination ~ '^/' AND char_length(destination) <= 1024),
    format text NOT NULL CHECK (char_length(format) BETWEEN 1 AND 64),
    status text NOT NULL CHECK (status IN ('started', 'completed', 'failed')),
    content_count bigint NOT NULL DEFAULT 0 CHECK (content_count >= 0),
    content_bytes bigint NOT NULL DEFAULT 0 CHECK (content_bytes >= 0),
    cache_count bigint NOT NULL DEFAULT 0 CHECK (cache_count >= 0),
    cache_bytes bigint NOT NULL DEFAULT 0 CHECK (cache_bytes >= 0),
    output_bytes bigint NOT NULL DEFAULT 0 CHECK (output_bytes >= 0),
    output_sha256 bytea CHECK (output_sha256 IS NULL OR octet_length(output_sha256) = 32),
    error_kind text NOT NULL DEFAULT '' CHECK (char_length(error_kind) <= 64),
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    CHECK (until > since),
    CHECK ((status = 'completed') = (finished_at IS NOT NULL AND output_sha256 IS NOT NULL))
);
CREATE INDEX content_exports_started ON observability.content_exports (started_at, id);

-- Every table is platform-only: an account-scope transaction reads nothing, so no product handler can show content
-- even if it forgets a condition. The command builds the platform scope in cmd/, as every operator does.
ALTER TABLE observability.content_keys ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE observability.content_blobs ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE observability.content_refs ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE observability.content_exports ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.content_keys USING (platform.in_platform_scope());
CREATE POLICY platform_only ON observability.content_blobs USING (platform.in_platform_scope());
CREATE POLICY platform_only ON observability.content_refs USING (platform.in_platform_scope());
CREATE POLICY platform_only ON observability.content_exports USING (platform.in_platform_scope());

GRANT SELECT, INSERT, UPDATE, DELETE ON observability.content_keys TO axiom_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON observability.content_blobs TO axiom_app;
GRANT SELECT, INSERT, DELETE ON observability.content_refs TO axiom_app;
GRANT SELECT, INSERT, UPDATE ON observability.content_exports TO axiom_app;

-- A content key is an envelope like every other one in the platform: retirement must see it, and a write at a
-- retired version is refused rather than making unreadable content.
CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON observability.content_keys
FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

-- +goose Down

DROP TABLE IF EXISTS observability.content_exports;
DROP TABLE IF EXISTS observability.content_refs;
DROP TABLE IF EXISTS observability.content_blobs;
DROP TABLE IF EXISTS observability.content_keys;
