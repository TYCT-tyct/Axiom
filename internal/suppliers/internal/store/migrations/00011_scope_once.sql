-- +goose Up

-- Keep command, role, terminal-state and write-check boundaries; only scope reads move into InitPlans.

ALTER POLICY owner ON suppliers.suppliers
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY tenant ON suppliers.attempt_costs
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY tenant ON suppliers.lost_requests
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY tenant ON suppliers.ended_requests
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY tenant ON suppliers.lost_request_resolutions
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY platform_only ON suppliers.charge_records
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON suppliers.charge_events
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON suppliers.attempt_journal
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON suppliers.sync_states
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON suppliers.sync_events
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON suppliers.sync_plans
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_only ON suppliers.rate_buckets
    USING ((SELECT platform.in_platform_scope()));

