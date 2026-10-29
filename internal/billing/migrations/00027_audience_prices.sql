-- +goose Up
-- Keep the default publication table compatible with older nodes during rollout.
CREATE TABLE billing.audience_publications (
 site_id uuid NOT NULL,
 model_id uuid NOT NULL,
 audience_kind text NOT NULL CHECK (audience_kind IN ('account','tier')),
 audience_id text NOT NULL CHECK (audience_id <> ''),
 candidate_id uuid NOT NULL,
 candidate jsonb NOT NULL,
 current_id uuid REFERENCES billing.price_versions,
 status text NOT NULL CHECK (status IN ('published','pending','blocked','unpriced')),
 PRIMARY KEY(site_id,model_id,audience_kind,audience_id)
);
ALTER TABLE billing.audience_publications ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.audience_publications FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.audience_publications FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON billing.audience_publications FOR UPDATE USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());
GRANT SELECT,INSERT,UPDATE ON billing.audience_publications TO axiom_app;
ALTER TABLE billing.price_versions
 ADD COLUMN audience_kind text NOT NULL DEFAULT 'all' CHECK (audience_kind IN ('all','account','tier')),
 ADD COLUMN audience_id text NOT NULL DEFAULT '';
