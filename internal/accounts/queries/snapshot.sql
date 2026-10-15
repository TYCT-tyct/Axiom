-- name: ListSites :many
-- The snapshot's tables, read whole in the platform scope (README.md「快照」).
SELECT s.id, s.is_main, s.status, s.key_prefix, s.default_tier_id, t.new_key_daily_limit
FROM accounts.sites s LEFT JOIN accounts.site_settings t ON t.site_id = s.id;

-- name: ListSiteDomains :many
SELECT domain, site_id, surface, origin FROM accounts.site_domains WHERE disabled_at IS NULL;

-- name: ListRoles :many
SELECT name, permissions FROM accounts.roles;
