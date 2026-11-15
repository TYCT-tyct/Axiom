-- +goose Up
GRANT UPDATE (email, email_verified_at) ON accounts.users TO axiom_app;

CREATE TABLE accounts.email_changes (
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid PRIMARY KEY,
    email text NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
    token_hash bytea NOT NULL CHECK (octet_length(token_hash) = 32),
    factor_version bigint NOT NULL,
    language text NOT NULL CHECK (language IN ('zh', 'en')),
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id)
);
CREATE INDEX email_changes_expiry ON accounts.email_changes (expires_at);
ALTER TABLE accounts.email_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.email_changes FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.email_changes FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON accounts.email_changes FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY updates ON accounts.email_changes FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY deletes ON accounts.email_changes FOR DELETE USING (
    (SELECT platform.in_platform_scope()) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
GRANT SELECT, INSERT, DELETE ON accounts.email_changes TO axiom_app;
GRANT UPDATE (email, token_hash, factor_version, language, expires_at) ON accounts.email_changes TO axiom_app;

-- +goose Down
DROP TABLE accounts.email_changes;
REVOKE UPDATE (email, email_verified_at) ON accounts.users FROM axiom_app;
