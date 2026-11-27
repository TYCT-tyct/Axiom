-- +goose Up

-- A tier the operator retires keeps its accounts and the pricing rules for it, but no account, sign-up or default
-- moves onto it again (docs/domains/accounts.md「等级」). The site's default tier is never a disabled one: disabling
-- and choosing the default both lock the site row first, and each refuses what the other would break.
ALTER TABLE accounts.tiers ADD COLUMN disabled boolean NOT NULL DEFAULT false;
GRANT UPDATE(disabled) ON TABLE accounts.tiers TO axiom_app;

-- The admin names a tier to people, so two tiers of a site cannot share a name, whatever its case.
CREATE UNIQUE INDEX tiers_site_name ON accounts.tiers (site_id, lower(name));
