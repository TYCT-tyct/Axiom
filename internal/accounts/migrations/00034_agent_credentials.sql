-- +goose Up

-- The operations credential, the third kind of operator beside sessions and management keys
-- (docs/domains/operations.md「运营凭证」). It is platform data: no tenant owns it, only the owner's admin session makes,
-- rotates and revokes it, and only the operations listener accepts it. `sites` NULL is every site; `platform` grants the
-- platform's own resources (suppliers, the catalog). `permissions` always holds operations.read, so a credential can
-- read its own record, and never the control plane's permissions (its own management, approving, granting platform
-- roles), which the code refuses before the row is written.
CREATE TABLE accounts.agent_credentials (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    permissions text[] NOT NULL CHECK ('operations.read' = ANY (permissions)
        AND NOT permissions && ARRAY['operations.manage', 'operations.approve', 'platform_grants.manage']),
    sites uuid[] CHECK (cardinality(sites) BETWEEN 1 AND 100),
    platform boolean NOT NULL,
    proposer_vendors text[] NOT NULL CHECK (cardinality(proposer_vendors) <= 20),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_used_at timestamptz,
    CHECK (expires_at > created_at)
);

-- A credential's tokens: the current one, and for at most a day after a rotation the one before it, so the MCP bridge
-- can swap its file. Only the keyed hash is kept (as for API keys, README.md「格式与哈希」), with the version of the
-- hash key and the last four characters to tell tokens apart. `retires_at` NULL is the current token.
CREATE TABLE accounts.agent_credential_tokens (
    secret_hash bytea PRIMARY KEY CHECK (octet_length(secret_hash) = 32),
    secret_version bigint NOT NULL CHECK (secret_version >= 1),
    credential_id uuid NOT NULL REFERENCES accounts.agent_credentials (id),
    last4 text NOT NULL CHECK (last4 ~ '^[A-Za-z0-9]{4}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    retires_at timestamptz
);
-- One current token per credential; a rotation retires the old one before it inserts the new.
CREATE UNIQUE INDEX agent_credential_tokens_current ON accounts.agent_credential_tokens (credential_id)
    WHERE retires_at IS NULL;

-- Platform data: only the platform scope sees or writes it. The operations listener looks a token up in the platform
-- scope before it knows anything else, as the admin's guard reads platform grants.
ALTER TABLE accounts.agent_credentials ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.agent_credentials FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON accounts.agent_credentials FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON accounts.agent_credentials FOR UPDATE USING ((SELECT platform.in_platform_scope()) AND revoked_at IS NULL)
    WITH CHECK ((SELECT platform.in_platform_scope()));
ALTER TABLE accounts.agent_credential_tokens ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.agent_credential_tokens FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON accounts.agent_credential_tokens FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON accounts.agent_credential_tokens FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

GRANT SELECT, INSERT ON accounts.agent_credentials, accounts.agent_credential_tokens TO axiom_app;
GRANT UPDATE (revoked_at, last_used_at) ON accounts.agent_credentials TO axiom_app;
GRANT UPDATE (retires_at) ON accounts.agent_credential_tokens TO axiom_app;

-- The owner manages the credentials from the admin with the admin role. Granting that role is itself a platform grant
-- the owner makes with two-step verification, so no other role holds the control plane.
UPDATE accounts.roles SET additional_permissions = array_append(additional_permissions, 'operations.manage')
WHERE name = 'admin' AND builtin AND NOT 'operations.manage' = ANY (additional_permissions);

-- +goose Down
UPDATE accounts.roles SET additional_permissions = array_remove(additional_permissions, 'operations.manage') WHERE builtin;
DROP TABLE accounts.agent_credential_tokens;
DROP TABLE accounts.agent_credentials;
