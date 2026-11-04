-- +goose Up

-- The four commercial metrics of the operations dashboard (docs/domains/operations.md「业务目标」). They live in their
-- own table because a node from before them reads operations.targets without a metric filter and refuses a metric it
-- does not know; keeping the new rows out of that table lets it run beside this release. The revision stays shared in
-- operations.target_revisions, so an editor from either release still sees one order.
CREATE TABLE IF NOT EXISTS operations.commercial_targets (
    site_id uuid NOT NULL,
    metric text NOT NULL CHECK (metric IN ('paid_users', 'monthly_revenue', 'gross_margin', 'retention')),
    month date NOT NULL CHECK (EXTRACT(DAY FROM month) = 1),
    amount numeric(38, 0) NOT NULL CHECK (amount >= 0),
    currency text CHECK (currency ~ '^[A-Z]{3}$'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, metric, month),
    -- Only the revenue target is money; the two rates are basis points and cannot be inherited from a count.
    CHECK ((metric = 'monthly_revenue') = (currency IS NOT NULL)),
    CHECK (metric NOT IN ('gross_margin', 'retention') OR amount <= 10000)
);

CREATE INDEX IF NOT EXISTS commercial_targets_by_month
    ON operations.commercial_targets (site_id, month);

ALTER TABLE operations.commercial_targets ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON operations.commercial_targets;
CREATE POLICY reads ON operations.commercial_targets FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
DROP POLICY IF EXISTS inserts ON operations.commercial_targets;
CREATE POLICY inserts ON operations.commercial_targets FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
DROP POLICY IF EXISTS updates ON operations.commercial_targets;
CREATE POLICY updates ON operations.commercial_targets FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
DROP POLICY IF EXISTS deletes ON operations.commercial_targets;
CREATE POLICY deletes ON operations.commercial_targets FOR DELETE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

GRANT SELECT, INSERT, UPDATE, DELETE ON operations.commercial_targets TO axiom_app;

-- +goose Down

DROP TABLE IF EXISTS operations.commercial_targets;
