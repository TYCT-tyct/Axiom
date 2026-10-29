-- +goose Up
CREATE TABLE billing.price_policies (
 site_id uuid PRIMARY KEY,
 rev bigint NOT NULL DEFAULT 0 CHECK (rev >= 0),
 floor_multiplier text NOT NULL DEFAULT '1',
 change_ratio text NOT NULL DEFAULT '0.1',
 max_increase_ratio text NOT NULL DEFAULT '1'
);
CREATE TABLE billing.price_versions (
 id uuid PRIMARY KEY,
 site_id uuid NOT NULL,
 model_id uuid NOT NULL,
 candidate_id uuid NOT NULL,
 schedule jsonb NOT NULL,
 basis jsonb NOT NULL,
 overridden boolean NOT NULL,
 actor text NOT NULL,
 reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1000),
 at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE billing.price_publications (
 site_id uuid NOT NULL,
 model_id uuid NOT NULL,
 candidate_id uuid NOT NULL,
 candidate jsonb NOT NULL,
 current_id uuid REFERENCES billing.price_versions,
 status text NOT NULL CHECK (status IN ('published','pending','blocked','unpriced')),
 PRIMARY KEY(site_id,model_id)
);
ALTER TABLE billing.price_policies ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE billing.price_versions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE billing.price_publications ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.price_policies FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.price_policies FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON billing.price_policies FOR UPDATE USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());
CREATE POLICY reads ON billing.price_versions FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.price_versions FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY reads ON billing.price_publications FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.price_publications FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON billing.price_publications FOR UPDATE USING (platform.in_platform_scope()) WITH CHECK (platform.in_platform_scope());
GRANT SELECT,INSERT,UPDATE ON billing.price_policies,billing.price_publications TO axiom_app;
GRANT SELECT,INSERT ON billing.price_versions TO axiom_app;
ALTER TABLE billing.reservations ADD COLUMN price_versions uuid[] NOT NULL DEFAULT '{}';
