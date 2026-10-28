-- +goose Up

-- The SELECT belongs in each policy, outside any row-dependent function: it becomes an InitPlan, so parsing is
-- independent of row count and the planner estimates the platform branch as a boolean parameter, not a text equality.
CREATE FUNCTION platform.allowed_sites() RETURNS uuid[]
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN coalesce(nullif(current_setting('axiom.scope_sites', true), '')::uuid[], '{}');

CREATE FUNCTION platform.allowed_accounts() RETURNS uuid[]
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN coalesce(nullif(current_setting('axiom.scope_accounts', true), '')::uuid[], '{}');

CREATE FUNCTION platform.home_site() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN nullif(current_setting('axiom.scope_home', true), '')::uuid;

-- Keep command, role, terminal-state and write-check boundaries; only scope reads move into InitPlans.

ALTER POLICY tenant ON platform.audit_log
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON platform.tenants
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY inserts ON platform.tenants
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR tenant_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON platform.idempotency
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY inserts ON platform.idempotency
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON platform.idempotency
    USING ((SELECT platform.in_platform_scope()) AND response IS NULL)
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON platform.discarded_jobs
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON platform.nodes
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY tenant ON platform.mail_outbox
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY platform_only ON platform.key_versions
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON platform.alert_signals
    USING ((SELECT platform.in_platform_scope()));
