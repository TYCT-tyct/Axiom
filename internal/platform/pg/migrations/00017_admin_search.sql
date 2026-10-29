-- +goose Up
CREATE INDEX IF NOT EXISTS audit_log_time ON platform.audit_log (at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_site ON platform.audit_log (site_id,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_account ON platform.audit_log (account_id,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_request ON platform.audit_log (request_id,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor ON platform.audit_log (actor_id,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor_kind ON platform.audit_log (actor_kind,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_action ON platform.audit_log (action,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_module ON platform.audit_log (module,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_target_kind ON platform.audit_log (target_kind,at DESC,id DESC);
CREATE INDEX IF NOT EXISTS audit_log_target ON platform.audit_log (target_id,at DESC,id DESC);

CREATE TABLE IF NOT EXISTS platform.smtp_config (
 id uuid PRIMARY KEY CHECK (id = '00000000-0000-0000-0000-000000000000'),
 ciphertext bytea NOT NULL,
 nonce bytea NOT NULL CHECK (octet_length(nonce)=12),
 wrapped_dek bytea NOT NULL,
 key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295)
);
ALTER TABLE platform.smtp_config ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS platform_only ON platform.smtp_config;
CREATE POLICY platform_only ON platform.smtp_config USING ((SELECT platform.in_platform_scope()));
GRANT SELECT,INSERT,UPDATE ON platform.smtp_config TO axiom_app;
