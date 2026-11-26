-- +goose Up

-- The admin's reads across every account's subscriptions (ListAllSubscriptions, ReadSubscriptionSummary): one index per
-- order of the list, one for an account's own (which SubscriptionsOfAccount also reads), the site's active ones for
-- the counts, and the paid grants by site and time for a month's revenue.
CREATE INDEX subscriptions_created ON billing.subscriptions (created_at DESC, id DESC);
CREATE INDEX subscriptions_ending ON billing.subscriptions (ends_at, id);
CREATE INDEX subscriptions_account ON billing.subscriptions (account_id, created_at DESC, id DESC);
CREATE INDEX subscriptions_site_active ON billing.subscriptions (site_id, ends_at, plan_id) WHERE state = 'active';
CREATE INDEX subscription_grants_site_sold ON billing.subscription_grants (site_id, created_at) WHERE source = 'paid';
