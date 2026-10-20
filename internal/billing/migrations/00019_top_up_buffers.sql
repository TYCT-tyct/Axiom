-- +goose Up

-- A site's top-up buffer: how far above the reference rate a top-up in another currency is priced, in millionths, in
-- the platform's favour (docs/domains/payments.md「金额与汇率」). A site without a row uses the default in code
-- (topup.go). Payments reads it with the rate in the platform scope; the site or the platform writes it.
CREATE TABLE billing.top_up_buffers (
    site_id uuid PRIMARY KEY,
    ppm integer NOT NULL CHECK (ppm BETWEEN 0 AND 100000)
);
ALTER TABLE billing.top_up_buffers ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY site ON billing.top_up_buffers USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));
GRANT SELECT, INSERT, UPDATE ON billing.top_up_buffers TO axiom_app;
