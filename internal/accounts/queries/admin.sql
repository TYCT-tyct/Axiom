-- What the admin API reads and writes of sites, users and accounts (README.md「管理接口」). They run in the platform
-- scope, which sees every tenant.

-- name: LockSite :many
-- Takes the site's turn, so two changes to its settings are applied one after the other and each audit entry names
-- the values it really replaced.
SELECT id FROM accounts.sites WHERE id = @id FOR NO KEY UPDATE;

-- name: ReadSiteSettings :many
-- A site with no settings row has none of its own; a site that does not exist has no row at all. A length of 0 is unset:
-- the table's bounds keep a set one at 5 minutes or more.
SELECT coalesce(extract(epoch FROM t.console_idle_timeout)::bigint, 0)::bigint AS console_idle_seconds,
    coalesce(extract(epoch FROM t.console_max_age)::bigint, 0)::bigint AS console_max_age_seconds,
    coalesce(extract(epoch FROM t.admin_idle_timeout)::bigint, 0)::bigint AS admin_idle_seconds,
    coalesce(extract(epoch FROM t.admin_max_age)::bigint, 0)::bigint AS admin_max_age_seconds,
    t.new_key_daily_limit
FROM accounts.sites s LEFT JOIN accounts.site_settings t ON t.site_id = s.id WHERE s.id = @site_id;

-- name: WriteSiteSettings :exec
-- Replaces the site's own values; a NULL is a value left out, which falls back to the default in code. The attempt
-- limits belong to their own admin resource and are left as they are.
INSERT INTO accounts.site_settings (site_id, console_idle_timeout, console_max_age, admin_idle_timeout, admin_max_age,
    new_key_daily_limit)
VALUES (@site_id, make_interval(secs => sqlc.narg(console_idle_seconds)::bigint),
    make_interval(secs => sqlc.narg(console_max_age_seconds)::bigint),
    make_interval(secs => sqlc.narg(admin_idle_seconds)::bigint),
    make_interval(secs => sqlc.narg(admin_max_age_seconds)::bigint), sqlc.narg(new_key_daily_limit)::bigint)
ON CONFLICT (site_id) DO UPDATE SET console_idle_timeout = excluded.console_idle_timeout,
    console_max_age = excluded.console_max_age, admin_idle_timeout = excluded.admin_idle_timeout,
    admin_max_age = excluded.admin_max_age, new_key_daily_limit = excluded.new_key_daily_limit;

-- name: CutSessions :exec
-- A site that shortens its sessions writes the shorter lifetimes into the ones already made, so that loosening it again
-- later does not bring back a session the shorter ones ended, even one that made no request in between. least() only
-- shortens: loosening leaves every session as it was.
UPDATE accounts.sessions
SET expires_at = least(expires_at, created_at + make_interval(secs => @max_age_seconds::bigint)),
    idle_timeout = least(idle_timeout, make_interval(secs => @idle_seconds::bigint))
WHERE site_id = @site_id AND surface = @surface AND revoked_at IS NULL AND expires_at > now()
    AND (created_at + make_interval(secs => @max_age_seconds::bigint) < expires_at
        OR make_interval(secs => @idle_seconds::bigint) < idle_timeout);

-- name: ListTiers :many
SELECT id, name FROM accounts.tiers WHERE site_id = @site_id ORDER BY name, id;

-- name: ListUsers :many
-- Newest first: an ID is a UUIDv7, so its order is the order of making, and a page starts after the last ID of the one
-- before. The prefix is matched literally (its % and _ are escaped by the caller) against the lower-cased email.
SELECT u.id, u.account_id, u.site_id, u.email, u.status, (u.mfa_enabled_at IS NOT NULL)::boolean AS mfa,
    a.tier_id, t.name AS tier_name, uuid_extract_timestamp(u.id)::timestamptz AS created_at
FROM accounts.users u
JOIN accounts.accounts a ON a.site_id = u.site_id AND a.id = u.account_id
JOIN accounts.tiers t ON t.site_id = a.site_id AND t.id = a.tier_id
WHERE (sqlc.narg(site_id)::uuid IS NULL OR u.site_id = sqlc.narg(site_id))
    AND (sqlc.narg(email_prefix)::text IS NULL OR u.email LIKE sqlc.narg(email_prefix) || '%')
    AND (sqlc.narg(after)::uuid IS NULL OR u.id < sqlc.narg(after))
ORDER BY u.id DESC
LIMIT @page_size::int;

-- name: ReadAccount :many
-- The account with its tier and its owner: in M1 an account is a person's own, so the owner is its one member.
SELECT a.id, a.site_id, a.name, a.kind, a.status, a.tier_id, t.name AS tier_name,
    uuid_extract_timestamp(a.id)::timestamptz AS created_at, o.id AS owner_id, o.email AS owner_email
FROM accounts.accounts a
JOIN accounts.tiers t ON t.site_id = a.site_id AND t.id = a.tier_id
JOIN LATERAL (
    SELECT u.id, u.email FROM accounts.members m
    JOIN accounts.users u ON u.site_id = m.site_id AND u.account_id = m.user_account_id AND u.id = m.user_id
    JOIN accounts.roles r ON r.id = m.role_id
    WHERE m.account_id = a.id
    ORDER BY (r.name = 'owner') DESC, m.user_id
    LIMIT 1) o ON true
WHERE a.id = @id;

-- name: ReadSiteKeyLimits :one
-- The site's own limits on the call keys of an account (a NULL is not set); no row means no such site.
SELECT t.max_keys_per_account, t.key_creations_per_hour
FROM accounts.sites s LEFT JOIN accounts.site_settings t ON t.site_id = s.id WHERE s.id = @site_id;

-- name: WriteSiteKeyLimits :exec
-- Replaces the site's own limits on keys and leaves its other settings as they are, so the settings the admin API
-- replaces whole cannot take these along.
INSERT INTO accounts.site_settings (site_id, max_keys_per_account, key_creations_per_hour)
VALUES (@site_id, sqlc.narg(max_keys)::integer, sqlc.narg(creations_per_hour)::integer)
ON CONFLICT (site_id) DO UPDATE SET max_keys_per_account = excluded.max_keys_per_account,
    key_creations_per_hour = excluded.key_creations_per_hour;

-- name: ReadAccountKeyLimits :one
-- The account's own limits and its site's, which it falls back to; no row means no such account.
SELECT a.site_id, a.max_keys, a.key_creations_per_hour, t.max_keys_per_account AS site_max_keys,
    t.key_creations_per_hour AS site_creations_per_hour
FROM accounts.accounts a LEFT JOIN accounts.site_settings t ON t.site_id = a.site_id WHERE a.id = @account_id;

-- name: WriteAccountKeyLimits :exec
UPDATE accounts.accounts SET max_keys = sqlc.narg(max_keys)::integer, key_creations_per_hour = sqlc.narg(creations_per_hour)::integer
WHERE id = @account_id;

-- name: ReadLoginLimits :one
SELECT t.ip_attempt_burst, t.ip_attempts_per_minute, t.email_sign_in_failures, t.user_code_failures, t.challenge_code_attempts
FROM accounts.sites s LEFT JOIN accounts.site_settings t ON t.site_id=s.id WHERE s.id = @site_id;

-- name: WriteLoginLimits :exec
INSERT INTO accounts.site_settings(site_id,ip_attempt_burst,ip_attempts_per_minute,email_sign_in_failures,user_code_failures,challenge_code_attempts)
VALUES(@site_id,sqlc.narg(ip_attempt_burst),sqlc.narg(ip_attempts_per_minute),sqlc.narg(email_sign_in_failures),sqlc.narg(user_code_failures),sqlc.narg(challenge_code_attempts))
ON CONFLICT(site_id) DO UPDATE SET ip_attempt_burst=excluded.ip_attempt_burst,ip_attempts_per_minute=excluded.ip_attempts_per_minute,
 email_sign_in_failures=excluded.email_sign_in_failures,user_code_failures=excluded.user_code_failures,challenge_code_attempts=excluded.challenge_code_attempts;
