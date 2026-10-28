-- +goose Up

-- Keep command, role, terminal-state and write-check boundaries; only scope reads move into InitPlans.

ALTER POLICY reads ON billing.holders
    USING ((SELECT platform.in_any_scope()));

ALTER POLICY inserts ON billing.holders
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON billing.holders
    USING ((SELECT platform.in_any_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY reads ON billing.balances
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON billing.balances
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON billing.balances
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

ALTER POLICY reads ON billing.spend
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON billing.spend
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON billing.spend
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

ALTER POLICY reads ON billing.reservations
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON billing.reservations
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON billing.reservations
    USING (
        ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])) AND state NOT IN ('released', 'settled')
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON billing.transactions
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON billing.transactions
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON billing.entries
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON billing.entries
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON billing.credit_batches
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON billing.credit_batches
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY reads ON billing.fallbacks
    USING ((SELECT platform.in_any_scope()));

ALTER POLICY inserts ON billing.fallbacks
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON billing.fallbacks
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY reads ON billing.unknown_items
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY inserts ON billing.unknown_items
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );

ALTER POLICY updates ON billing.unknown_items
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY reads ON billing.pricing_rules
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY inserts ON billing.pricing_rules
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON billing.pricing_rules
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY reads ON billing.pricing_rule_versions
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY inserts ON billing.pricing_rule_versions
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON billing.credit_batches
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

ALTER POLICY reads ON billing.expiry_cursor
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY inserts ON billing.expiry_cursor
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY updates ON billing.expiry_cursor
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY reads ON billing.exchange_rates
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY inserts ON billing.exchange_rates
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY site ON billing.top_up_buffers
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );

