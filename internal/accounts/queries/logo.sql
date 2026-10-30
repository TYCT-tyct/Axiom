-- name: ReadSiteLogo :many
-- A logo the site has, current or an earlier one whose content-hash URL a page still holds.
SELECT content_type, width, height, bytes
FROM accounts.site_logos
WHERE site_id = @site_id AND hash = @hash
LIMIT 1;

-- name: CurrentSiteLogo :many
-- The logo sites.logo_hash names, read in the admin's transaction so an operator sees what a page will.
SELECT l.hash, l.content_type, l.width, l.height, l.bytes
FROM accounts.sites s
JOIN accounts.site_logos l ON l.site_id = s.id AND l.hash = s.logo_hash
WHERE s.id = @site_id
LIMIT 1;

-- name: WriteSiteLogo :exec
-- The same bytes uploaded twice are one row; a changed logo is a new hash, so history needs no update.
INSERT INTO accounts.site_logos (site_id, hash, content_type, width, height, bytes)
VALUES (@site_id, @hash, @content_type, @width, @height, @bytes)
ON CONFLICT (site_id, hash) DO NOTHING;

-- name: SetSiteLogoHash :exec
UPDATE accounts.sites SET logo_hash = @hash WHERE id = @site_id;
