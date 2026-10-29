-- +goose Up
CREATE TABLE accounts.presets (
    id uuid PRIMARY KEY, site_id uuid NOT NULL, account_id uuid NOT NULL,
    slug text NOT NULL CHECK (slug ~ '^[a-zA-Z0-9_-]{1,100}$'),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    config jsonb NOT NULL CHECK (jsonb_typeof(config) = 'object' AND octet_length(config::text) <= 32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(), deleted_at timestamptz,
    FOREIGN KEY (site_id,account_id) REFERENCES accounts.accounts(site_id,id)
);
CREATE UNIQUE INDEX presets_live_slug ON accounts.presets(account_id,slug) WHERE deleted_at IS NULL;
CREATE INDEX presets_page ON accounts.presets(account_id,updated_at DESC,id DESC) WHERE deleted_at IS NULL;
ALTER TABLE accounts.presets ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.presets FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.presets FOR SELECT USING (
 (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]));
CREATE POLICY inserts ON accounts.presets FOR INSERT WITH CHECK (
 (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]));
CREATE POLICY updates ON accounts.presets FOR UPDATE USING (deleted_at IS NULL AND (
 (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))) WITH CHECK (
 (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]));
GRANT SELECT,INSERT ON accounts.presets TO axiom_app;
GRANT UPDATE(name,config,updated_at,deleted_at) ON accounts.presets TO axiom_app;
