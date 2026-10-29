-- +goose Up

ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY;
UPDATE accounts.roles SET permissions = array_append(permissions, 'balance.top_up')
WHERE builtin AND name IN ('owner', 'admin', 'finance');
UPDATE accounts.roles SET permissions = array_append(permissions, 'users.manage')
WHERE builtin AND name = 'admin';
ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY;

-- One bounded aggregate per site, independent of personal information and audit retention.
CREATE TABLE accounts.security_failures (
    site_id uuid PRIMARY KEY REFERENCES accounts.sites(id),
    started_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    failures bigint NOT NULL CHECK (failures >= 0)
);
CREATE INDEX security_failures_expiry ON accounts.security_failures(started_at, site_id) WHERE failures > 0;
ALTER TABLE accounts.security_failures ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON accounts.security_failures FOR SELECT USING (platform.in_scope(site_id, NULL));
CREATE POLICY platform_insert ON accounts.security_failures FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY platform_update ON accounts.security_failures FOR UPDATE USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE (started_at, failures) ON accounts.security_failures TO axiom_app;
