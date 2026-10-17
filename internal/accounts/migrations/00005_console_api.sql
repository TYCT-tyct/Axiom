-- +goose Up

-- The permissions the console's API names beyond the keys' (calls.view, balance.view), given to the built-in roles as
-- authorize.go declares them. The program refuses to start on a role holding a permission it does not declare, so
-- the declaration and this migration ship together. Forced row-level security binds the owner too, and a migration
-- sets no scope, so the table's owner writes the rows with FORCE lifted inside this migration's transaction, as 00003
-- does.
ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY;
UPDATE accounts.roles SET permissions = CASE name
    WHEN 'owner' THEN '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view}'
    WHEN 'admin' THEN '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view,sites.manage,platform_grants.manage}'
    WHEN 'member' THEN '{api_keys.view,api_keys.manage,calls.view}'
    WHEN 'finance' THEN '{api_keys.view,balance.view}'
    WHEN 'viewer' THEN '{api_keys.view,calls.view,balance.view}'
    ELSE '{api_keys.view,calls.view}'
END::text[]
WHERE builtin;
ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY;

-- The console lists an account's keys newest first (an ID is a UUIDv7, so its order is the order of making) and pages
-- on the ID.
CREATE INDEX api_keys_of_account ON accounts.api_keys (account_id, id DESC) WHERE kind = 'call' AND deleted_at IS NULL;
