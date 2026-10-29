-- +goose Up
ALTER TABLE accounts.tiers ADD COLUMN rate_requests integer CHECK (rate_requests BETWEEN 0 AND 1000000000);
ALTER TABLE accounts.accounts ADD COLUMN rate_requests integer CHECK (rate_requests BETWEEN 0 AND 1000000000);
ALTER TABLE accounts.api_keys ADD COLUMN rate_requests integer CHECK (rate_requests BETWEEN 0 AND 1000000000);
GRANT UPDATE (rate_requests) ON accounts.tiers, accounts.accounts, accounts.api_keys TO axiom_app;

-- Account-scoped admission may read only its assigned tier; it gains no tier write access.
ALTER POLICY reads ON accounts.tiers USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR id = ANY (ARRAY(SELECT a.tier_id FROM accounts.accounts a
        WHERE a.id = ANY ((SELECT platform.allowed_accounts())::uuid[])))
);

-- +goose Down
ALTER POLICY reads ON accounts.tiers USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
ALTER TABLE accounts.api_keys DROP COLUMN rate_requests;
ALTER TABLE accounts.accounts DROP COLUMN rate_requests;
ALTER TABLE accounts.tiers DROP COLUMN rate_requests;
