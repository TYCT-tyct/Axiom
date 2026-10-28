-- +goose Up

-- Keep command, role, terminal-state and write-check boundaries; only scope reads move into InitPlans.

ALTER POLICY reads ON accounts.sites
    USING (
        (SELECT platform.in_platform_scope())
        OR id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY inserts ON accounts.sites
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY updates ON accounts.sites
    USING (
        (SELECT platform.in_platform_scope())
        OR id = ANY ((SELECT platform.allowed_sites())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY reads ON accounts.site_domains
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY inserts ON accounts.site_domains
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY updates ON accounts.site_domains
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY reads ON accounts.site_settings
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY inserts ON accounts.site_settings
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY updates ON accounts.site_settings
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY reads ON accounts.tiers
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY inserts ON accounts.tiers
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY updates ON accounts.tiers
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

ALTER POLICY reads ON accounts.roles
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY inserts ON accounts.roles
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON accounts.roles
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY reads ON accounts.accounts
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.accounts
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON accounts.accounts
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON accounts.workspaces
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.workspaces
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON accounts.workspaces
    USING (
        ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))
        AND deleted_at IS NULL
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON accounts.users
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.users
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON accounts.users
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON accounts.members
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.members
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON accounts.platform_grants
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.platform_grants
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON accounts.platform_grants
    USING ((SELECT platform.in_platform_scope()) AND revoked_at IS NULL)
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY reads ON accounts.api_keys
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.api_keys
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON accounts.api_keys
    USING (
        ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))
        AND deleted_at IS NULL
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON accounts.sessions
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.sessions
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON accounts.sessions
    USING (
        ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))
        AND revoked_at IS NULL
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON accounts.recovery_codes
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON accounts.recovery_codes
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON accounts.recovery_codes
    USING (
        ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))
        AND used_at IS NULL
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

