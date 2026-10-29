-- +goose Up

-- Old nodes reject unknown names in permissions; keep their projection unchanged.
ALTER TABLE accounts.roles ADD COLUMN additional_permissions text[] NOT NULL DEFAULT '{}';
ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY;
UPDATE accounts.roles SET additional_permissions = array_append(additional_permissions, 'balance.top_up')
WHERE builtin AND name IN ('owner', 'admin', 'finance');
UPDATE accounts.roles SET additional_permissions = array_append(additional_permissions, 'users.manage')
WHERE builtin AND name = 'admin';
ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY;
GRANT UPDATE (additional_permissions) ON accounts.roles TO axiom_app;

-- One bounded aggregate per site, independent of personal information and audit retention.
CREATE TABLE accounts.security_failures (
    site_id uuid PRIMARY KEY REFERENCES accounts.sites(id),
    started_at timestamptz NOT NULL DEFAULT date_trunc('minute', statement_timestamp()),
    failures bigint NOT NULL CHECK (failures >= 0),
    completed_peak bigint NOT NULL DEFAULT 0 CHECK (completed_peak >= 0),
    sampled_at timestamptz NOT NULL DEFAULT '-infinity',
    reported boolean NOT NULL DEFAULT false
);
CREATE INDEX security_failures_expiry ON accounts.security_failures(started_at, site_id) WHERE failures > 0 OR completed_peak > 0 OR reported;
ALTER TABLE accounts.security_failures ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON accounts.security_failures FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY platform_insert ON accounts.security_failures FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_update ON accounts.security_failures FOR UPDATE USING ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, UPDATE (started_at, failures, completed_peak, sampled_at, reported) ON accounts.security_failures TO axiom_app;
