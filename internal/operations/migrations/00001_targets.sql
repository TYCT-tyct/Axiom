-- +goose Up

GRANT USAGE ON SCHEMA operations TO axiom_app;

-- The business targets the owner sets in the admin and the operations dashboard compares with the actual numbers
-- (docs/domains/operations.md「运营看板」). One row is one metric's target for one UTC calendar month: the month is
-- its first day, so a target covers the whole month and the dashboard can prorate it over the selected window.
-- `amount` is a count for calls and errors and billionths of a currency unit for revenue and cost; `currency` is the
-- site's accounting currency the money target was set in, and is NULL exactly for the counts. History lives in the
-- audit log, so a change replaces the current row instead of adding a version.
CREATE TABLE operations.targets (
    site_id uuid NOT NULL,
    metric text NOT NULL CHECK (metric IN ('calls', 'errors', 'revenue', 'cost')),
    month date NOT NULL CHECK (EXTRACT(DAY FROM month) = 1),
    amount numeric(38, 0) NOT NULL CHECK (amount >= 0),
    currency text CHECK (currency ~ '^[A-Z]{3}$'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, metric, month),
    CHECK ((metric IN ('revenue', 'cost')) = (currency IS NOT NULL))
);
-- The dashboard and the editor read one site's targets over the months a window touches; the primary key starts with
-- the metric, so it cannot answer that range.
CREATE INDEX targets_by_month ON operations.targets (site_id, month);

-- Every successful set, including a clear that leaves no target rows, moves the month's revision. The editor sends the
-- revision it read and a write compares it under the same lock, so a form filled before another administrator's change
-- is refused as stale instead of restoring the old values. There is no row before the first write: revision 0.
CREATE TABLE operations.target_revisions (
    site_id uuid NOT NULL,
    month date NOT NULL CHECK (EXTRACT(DAY FROM month) = 1),
    revision bigint NOT NULL CHECK (revision >= 1),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, month)
);

-- The scope readers sit in scalar subqueries, so each is a statement InitPlan instead of a call for every row
-- (internal/platform/pg/migrations/00014_scope_once.sql, internal/platform/pg/check.go). Targets are site data: the
-- platform scope sees every site, the site scope only its own; an account scope sees none.
ALTER TABLE operations.targets ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.targets FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON operations.targets FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY updates ON operations.targets FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY deletes ON operations.targets FOR DELETE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

GRANT SELECT, INSERT, UPDATE, DELETE ON operations.targets TO axiom_app;

ALTER TABLE operations.target_revisions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.target_revisions FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON operations.target_revisions FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY updates ON operations.target_revisions FOR UPDATE USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
) WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

GRANT SELECT, INSERT, UPDATE ON operations.target_revisions TO axiom_app;

-- +goose Down
DROP TABLE operations.target_revisions;
DROP TABLE operations.targets;
