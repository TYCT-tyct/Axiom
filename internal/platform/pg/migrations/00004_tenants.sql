-- +goose Up

-- The tenants the audit log may name (internal/platform/README.md「审计日志」). A site registers as (site, site) and
-- an account as (site, account), so one key covers both: an entry's tenant_id is its account, or its site when it
-- names none. The registry is how platform checks that an entry's site and account belong together without knowing
-- the accounts module's tables; accounts registers each site and account in the transaction that creates it.
CREATE TABLE platform.tenants (
    site_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    PRIMARY KEY (site_id, tenant_id)
);

-- Only the platform scope reads it; the transaction that creates a tenant registers it, in that tenant's own scope
-- (registration runs in the new account's), and nothing changes or removes a registration.
ALTER TABLE platform.tenants ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON platform.tenants FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON platform.tenants FOR INSERT WITH CHECK (platform.in_scope(site_id, tenant_id));
GRANT SELECT, INSERT ON platform.tenants TO axiom_app;

-- Entries may now name a tenant: an account only with its site, and only a registered pair. The foreign key skips
-- entries that name none, where both columns are null.
ALTER TABLE platform.audit_log
    DROP CONSTRAINT no_tenant,
    ADD CONSTRAINT account_with_site CHECK (account_id IS NULL OR site_id IS NOT NULL),
    ADD COLUMN tenant_id uuid GENERATED ALWAYS AS (coalesce(account_id, site_id)) STORED,
    ADD CONSTRAINT registered_tenant FOREIGN KEY (site_id, tenant_id) REFERENCES platform.tenants;
