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
    -- The tenant the target belongs to: both for account data, the site alone for site data, neither for platform
    -- and public data. Nothing here yet ties an account to its site, and a policy that admits a row by either one
    -- would show an entry of (another site, this account) to that site; so no_tenant refuses tenant entries until
    -- accounts' tables exist to tie them to (internal/platform/README.md「审计日志」).
    site_id uuid,
    account_id uuid,
    CONSTRAINT no_tenant CHECK (site_id IS NULL AND account_id IS NULL),
    reason text CHECK (char_length(reason) BETWEEN 1 AND 1000),
    -- Small and structured: IDs, names and before/after values. The 8 KiB cap (as text) bounds an entry's size; it
    -- cannot tell content from other strings, so keeping content out is the caller's choice of fields.
    detail jsonb NOT NULL CHECK (jsonb_typeof(detail) = 'object' AND octet_length(detail::text) <= 8192)
);

-- One policy reads and appends: a transaction sees and writes the entries of the tenants in its scope, and only the
-- platform scope those of no tenant, which while no_tenant stands are all of them.
ALTER TABLE platform.audit_log ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON platform.audit_log USING (platform.in_scope(site_id, account_id));

GRANT SELECT, INSERT (actor_kind, actor_id, request_id, action, module, target_kind, target_id, site_id, account_id,
    reason, detail) ON platform.audit_log TO axiom_app;
