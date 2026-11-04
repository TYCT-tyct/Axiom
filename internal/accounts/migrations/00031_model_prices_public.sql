-- +goose Up
-- Whether the site shows its model prices to visitors who are not signed in (docs/domains/gateway.md). NULL is
-- unset: the code default is public, so a row an old node wrote still reads and the site needs no backfill.
--
-- The column is nullable and has no database default on purpose: the default lives in code (accounts.DefaultSiteSettings),
-- and the admin view shows whether a value is the site's own or the default. Adding it needs no rewrite of the table.
ALTER TABLE accounts.site_settings ADD COLUMN model_prices_public boolean;
GRANT UPDATE (model_prices_public) ON accounts.site_settings TO axiom_app;

-- +goose Down
ALTER TABLE accounts.site_settings DROP COLUMN model_prices_public;
