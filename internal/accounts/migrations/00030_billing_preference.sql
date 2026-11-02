-- +goose Up
-- A user's funding order for calls (docs/domains/billing.md「资金来源」): the account's own choice, or the site's
-- default when the account has none. Both are optional so a row an old node wrote still reads, and the code default is
-- subscription_first.
--
-- The site default lives in its own table because accounts read it in their own scope when a key is admitted, and
-- site_settings holds administrator-only values that an account must not see. site_billing_preferences has no such
-- values, so its read policy can include the account's home site.
ALTER TABLE accounts.accounts ADD COLUMN billing_preference text
    CONSTRAINT accounts_billing_preference_check CHECK (billing_preference IN
        ('subscription_first', 'wallet_first', 'subscription_only', 'wallet_only'));
GRANT UPDATE (billing_preference) ON accounts.accounts TO axiom_app;

CREATE TABLE accounts.site_billing_preferences (
    site_id uuid PRIMARY KEY REFERENCES accounts.sites,
    preference text NOT NULL DEFAULT 'subscription_first'
        CONSTRAINT site_billing_preferences_check CHECK (preference IN
            ('subscription_first', 'wallet_first', 'subscription_only', 'wallet_only'))
);
ALTER TABLE accounts.site_billing_preferences ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.site_billing_preferences FOR SELECT
    USING ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR site_id IN (SELECT a.site_id FROM accounts.accounts a
            WHERE a.id = ANY ((SELECT platform.allowed_accounts())::uuid[])));
CREATE POLICY inserts ON accounts.site_billing_preferences FOR INSERT
    WITH CHECK ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]));
CREATE POLICY updates ON accounts.site_billing_preferences FOR UPDATE
    USING ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]))
    WITH CHECK ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]));
CREATE POLICY deletes ON accounts.site_billing_preferences FOR DELETE
    USING ((SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]));
GRANT SELECT, INSERT, DELETE ON accounts.site_billing_preferences TO axiom_app;
GRANT UPDATE (preference) ON accounts.site_billing_preferences TO axiom_app;
