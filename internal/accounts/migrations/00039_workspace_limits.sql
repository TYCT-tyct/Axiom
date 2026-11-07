-- +goose Up

-- How many workspaces an account may hold that are not deleted (docs/domains/accounts.md「工作区」), set as key limits
-- are: a site sets its own in site_settings and an account may carry its own over the site's; a null is not set and
-- the next level applies, and the default, 50, is in code only. The bounds keep a typo from refusing every workspace,
-- and keep the list, which is read whole, small.
ALTER TABLE accounts.site_settings ADD COLUMN max_workspaces_per_account integer CHECK (max_workspaces_per_account BETWEEN 1 AND 1000);
ALTER TABLE accounts.accounts ADD COLUMN max_workspaces integer CHECK (max_workspaces BETWEEN 1 AND 1000);

GRANT UPDATE (max_workspaces_per_account) ON accounts.site_settings TO axiom_app;
GRANT UPDATE (max_workspaces) ON accounts.accounts TO axiom_app;

-- +goose Down
ALTER TABLE accounts.accounts DROP COLUMN max_workspaces;
ALTER TABLE accounts.site_settings DROP COLUMN max_workspaces_per_account;
