-- +goose Up

-- Security events (internal/accounts/README.md「安全事件」): one row per sign-in, registration and two-step
-- verification, refused or not, naming the request, the user and the session it was about. No personal data is
-- stored: the client's address is kept only as a keyed HMAC under the key API keys are hashed with, so the same
-- address can be recognized across events without being kept, and the address itself, the email and the user agent
-- never appear. What a request carried is the request log's, with the retention policy of that store; this table
-- holds no more of it than the hash. Append only: the application may read and insert, never update or delete, and
-- retention, still undecided like the audit log's (internal/platform/README.md「审计日志」), comes with the role
-- that deletes.
CREATE TABLE accounts.security_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    at timestamptz NOT NULL DEFAULT now(),
    site_id uuid NOT NULL, -- the site of the request's domain, known before the user is
    account_id uuid,
    user_id uuid,
    session_id uuid,
    request_id uuid,
    kind text NOT NULL CHECK (kind IN ('sign_in', 'sign_up', 'mfa')),
    refused boolean NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (reason ~ '^([a-z][a-z0-9_]*)?$'),
    ip_hash text NOT NULL DEFAULT '' CHECK (ip_hash ~ '^([0-9a-f]{64})?$'),
    -- A refusal says why; an attempt that went through says nothing.
    CHECK (refused = (reason <> ''))
);

-- Only the platform scope writes an event: it is the scope that sees attempts at accounts the attempt could not
-- name, such as an email that has no user. A tenant reads its own rows, and no policy lets anyone change them.
ALTER TABLE accounts.security_events ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.security_events FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON accounts.security_events FOR INSERT WITH CHECK (platform.in_platform_scope());

-- id and at are the database's, so the application names neither.
GRANT SELECT ON accounts.security_events TO axiom_app;
GRANT INSERT (site_id, account_id, user_id, session_id, request_id, kind, refused, reason, ip_hash)
    ON accounts.security_events TO axiom_app;

-- users.manage, held by admin alone: suspending an account and disabling a login are a security decision, and the
-- roles that may read users (finance, viewer) must not make it. Written like 00005 and 00006, because the program
-- refuses to start on a role holding a permission it does not declare.
ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY;
UPDATE accounts.roles SET permissions = CASE name
    WHEN 'owner' THEN '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view}'
    WHEN 'admin' THEN '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view,sites.manage,platform_grants.manage,users.view,users.manage,credits.grant,suppliers.view,suppliers.manage,catalog.view,catalog.manage,pricing.view,pricing.manage}'
    WHEN 'member' THEN '{api_keys.view,api_keys.manage,calls.view}'
    WHEN 'finance' THEN '{api_keys.view,balance.view,users.view}'
    WHEN 'viewer' THEN '{api_keys.view,calls.view,balance.view,users.view,suppliers.view,catalog.view,pricing.view}'
    ELSE '{api_keys.view,calls.view,users.view}'
END::text[]
WHERE builtin;
ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY;
