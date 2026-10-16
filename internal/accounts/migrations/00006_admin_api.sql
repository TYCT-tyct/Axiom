-- +goose Up

-- The permissions the admin API names (users.view, credits.grant, suppliers.*, catalog.*, pricing.*), given to the
-- built-in roles as authorize.go declares them. Only admin holds the ones that change money, credentials, prices or
-- the catalog; the read-only roles see what they need to answer a question. Written like 00005: the program refuses
-- to start on a role holding a permission it does not declare, so the declaration and this migration ship together.
ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY;
UPDATE accounts.roles SET permissions = CASE name
    WHEN 'owner' THEN '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view}'
    WHEN 'admin' THEN '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view,sites.manage,platform_grants.manage,users.view,credits.grant,suppliers.view,suppliers.manage,catalog.view,catalog.manage,pricing.view,pricing.manage}'
    WHEN 'member' THEN '{api_keys.view,api_keys.manage,calls.view}'
    WHEN 'finance' THEN '{api_keys.view,balance.view,users.view}'
    WHEN 'viewer' THEN '{api_keys.view,calls.view,balance.view,users.view,suppliers.view,catalog.view,pricing.view}'
    ELSE '{api_keys.view,calls.view,users.view}'
END::text[]
WHERE builtin;
ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY;

-- The admin lists users by the start of the email (the column is lower case, and the pattern ops class lets a prefix
-- match use the index in any collation).
CREATE INDEX users_email_prefix ON accounts.users (email text_pattern_ops);
