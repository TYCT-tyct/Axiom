-- +goose Up

-- What a price schedule cannot price (docs/domains/billing.md「价目表缺项」, internal/billing/README.md「兜底价」).

-- A credit currency's fallbacks, as canonical JSON that billing reads and checks (fallback.go): each known per-use
-- item's default unit price, which fills a schedule that lacks one, and the markup a correction multiplies a
-- supplier's actual cost by when the usage holds an item nothing prices. They are keyed by currency because a site's
-- credits are a currency (「钱的表示」) and a site's own settings do not exist yet; Start refuses to run without a valid
-- USD row, the main site's currency, and every row must be valid.
CREATE TABLE billing.fallbacks (
    currency text PRIMARY KEY CHECK (currency ~ '^[A-Z]{3}$'),
    body text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- The initial USD row, from the price source of each item, the vendors' official pricing pages (2026-10-16):
-- web_search is the dearest per-call search price, OpenAI's web search preview for non-reasoning models at $25 per
-- 1,000 calls (Anthropic charges $10, Gemini's grounding $14); code_execution is an hour of Anthropic's container,
-- $0.05, above OpenAI's 1 GB session of 20 minutes at $0.03, as neither vendor charges per call
-- (internal/catalog/README.md「代码执行按次计」). The markup 1.2 is the developer's choice under the owner's delegation
-- until pricing rules exist; an administrator changes all three with SaveFallbacks.
INSERT INTO billing.fallbacks (currency, body)
VALUES ('USD', '{"currency":"USD","markup":"1.2","per_use":{"code_execution":"0.05","web_search":"0.025"}}');

-- Each kept route entry's fallbacks as admission locked them, by route index like prices ('' for a dropped entry),
-- and each entry's model, so settlement, cleanup and corrections price and report unknown usage without the process
-- that admitted it. attempt_entries is the route index each attempt bound, by attempt number (attempt_entries[seq],
-- -1 for a number not seen), as Settle or cleanup learnt it: a supplier record of an attempt the user is not charged
-- for still finds the model whose unknown usage it reports. unpriced is no longer written: every reservation now
-- locks its fallbacks, so all usage is priced.
ALTER TABLE billing.reservations
    ADD COLUMN fallbacks text[] NOT NULL DEFAULT '{}',
    ADD COLUMN models uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN attempt_entries integer[] NOT NULL DEFAULT '{}';
-- The held estimates in the order the fallback-hold listing pages them, which the gauges also read on every scrape.
CREATE INDEX estimated_by_correct_by ON billing.reservations (correct_by, request_id) WHERE state = 'estimated';

-- A usage field a model's upstream reported that nothing prices, from its first appearance: the model takes no new
-- requests (routing's unknown_item) until an administrator prices it and clears the row. The first call's tenant is
-- recorded so that tenant's settlement can see the row it wrote; only the platform scope sees every row and clears
-- them.
CREATE TABLE billing.unknown_items (
    model_id uuid NOT NULL,
    field text NOT NULL,
    request_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    cleared_at timestamptz,
    PRIMARY KEY (model_id, field)
);

ALTER TABLE billing.fallbacks ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.fallbacks FOR SELECT USING (platform.in_any_scope());
CREATE POLICY inserts ON billing.fallbacks FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON billing.fallbacks FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());

ALTER TABLE billing.unknown_items ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.unknown_items FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.unknown_items FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON billing.unknown_items FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());

GRANT SELECT, INSERT ON billing.fallbacks, billing.unknown_items TO axiom_app;
GRANT UPDATE (body, updated_at) ON billing.fallbacks TO axiom_app;
GRANT UPDATE (cleared_at) ON billing.unknown_items TO axiom_app;
GRANT UPDATE (attempt_entries) ON billing.reservations TO axiom_app;
