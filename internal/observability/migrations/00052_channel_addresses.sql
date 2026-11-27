-- +goose Up

-- A bot channel's address when the address itself is its credential (a WeCom group bot's key, a Feishu hook's token):
-- sealed like the channels' secrets and never part of the configuration, which keeps only a masked form and the
-- address's digest. The row is the channel ID and that digest, joined by a slash (docs/domains/observability.md「通知渠道」).
CREATE TABLE observability.channel_addresses (
    id text PRIMARY KEY,
    channel_id text NOT NULL,
    digest text NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL,
    CONSTRAINT channel_addresses_id_check CHECK (id = channel_id || '/' || digest)
);

ALTER TABLE observability.channel_addresses OWNER TO axiom_owner;
ALTER TABLE observability.channel_addresses ENABLE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.channel_addresses FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.channel_addresses USING (( SELECT platform.in_platform_scope() AS in_platform_scope));
CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON observability.channel_addresses
    FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.channel_addresses TO axiom_app;
