-- +goose Up

-- The admin's reads across every account's subscriptions (ListAllSubscriptions, ReadSubscriptionSummary): one index per
-- order of the list, one for an account's own (which SubscriptionsOfAccount also reads), the site's active ones for
-- the counts, and the paid grants by site and time for a month's revenue.
CREATE INDEX subscriptions_created ON billing.subscriptions (created_at DESC, id DESC);
CREATE INDEX subscriptions_ending ON billing.subscriptions (ends_at, id);
CREATE INDEX subscriptions_account ON billing.subscriptions (account_id, created_at DESC, id DESC);
CREATE INDEX subscriptions_site_active ON billing.subscriptions (site_id, ends_at, plan_id) WHERE state = 'active';
CREATE INDEX subscription_grants_site_sold ON billing.subscription_grants (site_id, created_at) WHERE source = 'paid';

-- The dashboard's call money by UTC day (PlatformCallMoney) groups by this expression; its statistics let the planner
-- hash the window's few days rather than sort every reservation in it.
CREATE STATISTICS billing.reservations_created_day ON (date_trunc('day', created_at AT TIME ZONE 'UTC')) FROM billing.reservations;
