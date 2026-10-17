-- +goose Up

-- The pricing rules of a site (docs/domains/billing.md「定价」): what a rule prices (target), for whom (audience), how
-- (method) and within what limits (constraints). A rule is never deleted, so what a price was can be explained: it is
-- disabled instead, and a change keeps the rule it replaced in pricing_rule_versions. Two rules of one site cannot have
-- the same target and audience, so any two can be put in order. The tables are the platform's; internal/billing/README.md
-- 「定价规则」.
CREATE TABLE billing.pricing_rules (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    -- The optimistic token: a change names the rev it was decided on and raises it.
    rev bigint NOT NULL DEFAULT 0 CHECK (rev >= 0),
    enabled boolean NOT NULL,
    target_kind text NOT NULL CHECK (target_kind IN ('endpoint', 'group', 'supplier', 'model', 'author', 'source_type', 'all')),
    target_id text NOT NULL CHECK ((target_kind = 'all') = (target_id = '') AND char_length(target_id) <= 200),
    audience_kind text NOT NULL CHECK (audience_kind IN ('account', 'tier', 'all')),
    audience_id text NOT NULL CHECK ((audience_kind = 'all') = (audience_id = '') AND char_length(audience_id) <= 64),
    method jsonb NOT NULL CHECK (jsonb_typeof(method) = 'object'),
    constraints jsonb NOT NULL CHECK (jsonb_typeof(constraints) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, target_kind, target_id, audience_kind, audience_id)
);

-- Every rev of every rule, as the rule was then, with who made it and why. Only added to.
CREATE TABLE billing.pricing_rule_versions (
    rule_id uuid NOT NULL REFERENCES billing.pricing_rules,
    rev bigint NOT NULL CHECK (rev >= 0),
    body jsonb NOT NULL CHECK (jsonb_typeof(body) = 'object'),
    at timestamptz NOT NULL DEFAULT now(),
    actor text NOT NULL CHECK (actor <> ''),
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1000),
    PRIMARY KEY (rule_id, rev)
);

ALTER TABLE billing.pricing_rules ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.pricing_rules FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.pricing_rules FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON billing.pricing_rules FOR UPDATE USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());

ALTER TABLE billing.pricing_rule_versions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.pricing_rule_versions FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.pricing_rule_versions FOR INSERT WITH CHECK (platform.in_platform_scope());

-- No DELETE, and no UPDATE of what a rule is made for: its ID and its site.
GRANT SELECT, INSERT ON billing.pricing_rules, billing.pricing_rule_versions TO axiom_app;
GRANT UPDATE (rev, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints, updated_at)
    ON billing.pricing_rules TO axiom_app;
