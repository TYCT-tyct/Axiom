-- +goose Up

-- The audit log: who changed what, written by every module through internal/platform/audit inside the transaction of
-- the change itself, so the entry commits or rolls back with it. The application appends and reads; it is granted no
-- UPDATE, DELETE or TRUNCATE, and may not choose id or at, so an entry cannot be rewritten, removed or backdated.
-- Retention and the operations module's hash chain come later (internal/platform/README.md「审计日志」).
CREATE TABLE platform.audit_log (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    at timestamptz NOT NULL DEFAULT now(),
    -- Kinds are added with their first writer: management keys (accounts) and operations agents.
    actor_kind text NOT NULL CHECK (actor_kind IN ('user', 'system')),
    actor_id text NOT NULL CHECK (actor_id ~ '^[a-z0-9][a-z0-9_.-]*$'),
    request_id uuid,
    action text NOT NULL CHECK (action ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    module text NOT NULL CHECK (module ~ '^[a-z]+$'),
    target_kind text NOT NULL CHECK (target_kind ~ '^[a-z][a-z0-9_]*$'),
    target_id text NOT NULL CHECK (target_id <> ''),
    -- The tenant the target belongs to, if any: both for account data, the site alone for site data, neither for
    -- platform and public data. Row-level security reads them as it reads any tenant table's.
    site_id uuid,
    account_id uuid CHECK (account_id IS NULL OR site_id IS NOT NULL),
    reason text CHECK (char_length(reason) BETWEEN 1 AND 1000),
    -- Small and structured: room for IDs, names and before/after values, not for content (8 KiB as text).
    detail jsonb NOT NULL CHECK (jsonb_typeof(detail) = 'object' AND octet_length(detail::text) <= 8192)
);

-- One policy reads and appends: a transaction sees and writes the entries of the tenants in its scope, and only the
-- platform scope those of no tenant, so a tenant's change cannot be recorded against another tenant or the platform.
ALTER TABLE platform.audit_log ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON platform.audit_log USING (platform.in_scope(site_id, account_id));

GRANT SELECT, INSERT (actor_kind, actor_id, request_id, action, module, target_kind, target_id, site_id, account_id,
    reason, detail) ON platform.audit_log TO axiom_app;
