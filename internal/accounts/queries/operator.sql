-- What the command line does to sites and users (README.md「第一个用户」): the main site, its first tier and domains,
-- users made without an invitation, platform grants, and the factors only the command line changes.

-- name: InsertMainSite :exec
INSERT INTO accounts.sites (id, name, status, key_prefix, registration, default_tier_id, currency)
VALUES (@id, @name, 'active', @key_prefix, 'invite', @default_tier_id, @currency);

-- name: InsertTier :exec
INSERT INTO accounts.tiers (id, site_id, name) VALUES (@id, @site_id, @name);

-- name: InsertDomain :exec
INSERT INTO accounts.site_domains (domain, site_id, surface, origin) VALUES (@domain, @site_id, @surface, @origin);

-- name: MainSite :many
SELECT id, default_tier_id FROM accounts.sites WHERE is_main;

-- name: RoleByName :many
SELECT id FROM accounts.roles WHERE name = @name;

-- name: UserByEmail :many
SELECT id, account_id FROM accounts.users WHERE site_id = @site_id AND email = @email;

-- name: InsertPlatformGrant :execrows
-- A user holds a role once: granting it again changes nothing. The index platform_grants_held decides, so two
-- grants at once cannot both insert.
INSERT INTO accounts.platform_grants (id, site_id, account_id, user_id, role_id)
VALUES (@id, @site_id, @account_id, @user_id, @role_id)
ON CONFLICT (user_id, role_id) WHERE revoked_at IS NULL DO NOTHING;

-- name: SetPassword :exec
-- The caller raises the factor version: a signed-in change through finishFactorChange, a reset with BumpFactorVersion.
UPDATE accounts.users SET password_hash = @password_hash WHERE id = @id;

-- name: ResetMFA :exec
UPDATE accounts.users SET totp_ciphertext = NULL, totp_nonce = NULL, totp_wrapped_dek = NULL, totp_key_version = NULL,
    totp_last_step = 0, mfa_enabled_at = NULL
WHERE id = @id;
