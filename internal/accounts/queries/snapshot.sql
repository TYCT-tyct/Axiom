-- name: ListSites :many
-- The snapshot's tables, read whole in the platform scope (README.md「快照」). A session length of 0 is unset: the
-- table's bounds keep a set one at 5 minutes or more.
SELECT s.id, s.is_main, s.name, s.status, s.key_prefix, s.registration, s.currency, s.default_tier_id, t.new_key_daily_limit,
    coalesce(extract(epoch FROM t.console_idle_timeout)::bigint, 0)::bigint AS console_idle_seconds,
    coalesce(extract(epoch FROM t.console_max_age)::bigint, 0)::bigint AS console_max_age_seconds,
    coalesce(extract(epoch FROM t.admin_idle_timeout)::bigint, 0)::bigint AS admin_idle_seconds,
    coalesce(extract(epoch FROM t.admin_max_age)::bigint, 0)::bigint AS admin_max_age_seconds,
    t.ip_attempt_burst, t.ip_attempts_per_minute, t.email_sign_in_failures, t.user_code_failures,
    t.challenge_code_attempts, t.max_keys_per_account, t.key_creations_per_hour,
    t.registration_human_check, t.registration_human_check_bits, t.registration_domain_burst,
    t.registration_domain_per_hour, t.registration_domain_allow, t.registration_domain_deny
FROM accounts.sites s LEFT JOIN accounts.site_settings t ON t.site_id = s.id;

-- name: ListSiteDomains :many
SELECT domain, site_id, surface, origin FROM accounts.site_domains WHERE disabled_at IS NULL;

-- name: ListRoles :many
SELECT id, name, (permissions || additional_permissions)::text[] AS permissions FROM accounts.roles;
