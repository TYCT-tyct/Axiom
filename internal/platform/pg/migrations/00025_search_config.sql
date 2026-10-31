-- +goose Up

-- The web plugin's backend override: one sealed row, like the SMTP one. Absent means the environment is authoritative;
-- a row with an empty URL is the admin turning web search off.
CREATE TABLE IF NOT EXISTS platform.search_config (
 id uuid PRIMARY KEY CHECK (id = '00000000-0000-0000-0000-000000000000'),
 ciphertext bytea NOT NULL,
 nonce bytea NOT NULL CHECK (octet_length(nonce)=12),
 wrapped_dek bytea NOT NULL,
 key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295)
);
ALTER TABLE platform.search_config ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS platform_only ON platform.search_config;
CREATE POLICY platform_only ON platform.search_config USING ((SELECT platform.in_platform_scope()));
GRANT SELECT,INSERT,UPDATE ON platform.search_config TO axiom_app;

CREATE OR REPLACE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.search_config
FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');
