-- +goose Up

-- The file-parser plugin's backend override: one sealed row, like the search and SMTP ones. Absent means the
-- environment is authoritative; a row with an empty URL is the admin turning file parsing off.
CREATE TABLE IF NOT EXISTS platform.file_parser_config (
 id uuid PRIMARY KEY CHECK (id = '00000000-0000-0000-0000-000000000000'),
 ciphertext bytea NOT NULL,
 nonce bytea NOT NULL CHECK (octet_length(nonce)=12),
 wrapped_dek bytea NOT NULL,
 key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295)
);
ALTER TABLE platform.file_parser_config ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS platform_only ON platform.file_parser_config;
CREATE POLICY platform_only ON platform.file_parser_config USING ((SELECT platform.in_platform_scope()));
GRANT SELECT,INSERT,UPDATE ON platform.file_parser_config TO axiom_app;

CREATE OR REPLACE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.file_parser_config
FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');
