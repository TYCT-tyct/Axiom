-- Open registration (README.md「开放注册」): the mode and default tier from the database, the pending request, and
-- the token that turns it into a user once the address is proved.

-- name: RegistrationSite :one
-- The site a console domain serves when its site is enabled; no row means no such console domain. The registration
-- mode comes from the database so a disabled or closed site cannot be admitted from a stale snapshot.
SELECT t.site_id::uuid AS site_id, t.default_tier_id::uuid AS default_tier_id, t.registration::text AS registration
FROM accounts.registration_site(@domain::text) AS t (site_id, default_tier_id, registration);

-- name: InsertRegistrationRequest :one
-- A second request for the same address replaces the first pending one and invalidates its link; the older job finds
-- the same row and sends the newer link.
INSERT INTO accounts.registration_requests (id, site_id, email_hash, email, tier_id, domain, language)
VALUES (@id, @site_id, @email_hash, @email, @tier_id, @domain, @language)
ON CONFLICT (site_id, email_hash) DO UPDATE SET tier_id = excluded.tier_id, domain = excluded.domain,
    language = excluded.language, expires_at = excluded.expires_at, token_hash = NULL
RETURNING id;

-- name: LockRegistrationRequest :many
-- The worker that issues the link and the transaction that consumes it both hold this row first.
SELECT site_id, email, tier_id, domain, language, token_hash,
    expires_at > clock_timestamp() AS unexpired
FROM accounts.registration_requests WHERE id = @id FOR UPDATE;

-- name: SetRegistrationToken :execrows
-- The token is written once. A redelivered job finds the row already sent and sends nothing.
UPDATE accounts.registration_requests SET token_hash = @token_hash
WHERE id = @id AND token_hash IS NULL AND expires_at > clock_timestamp();

-- name: DeleteRegistrationRequest :execrows
DELETE FROM accounts.registration_requests WHERE id = @id;

-- name: RegistrationTenant :many
-- A token names its pending registration on the console domain that carries it; both are checked before any scope.
-- The address and expiry let the caller refuse a denied or expired request before hashing a password.
SELECT t.id::uuid AS id, t.site_id::uuid AS site_id, t.email::text AS email, t.unexpired::boolean AS unexpired
FROM accounts.registration_tenant(@domain::text, @token_hash::bytea) AS t (id, site_id, email, unexpired);

-- name: PurgeRegistrationRequests :exec
DELETE FROM accounts.registration_requests WHERE id IN (
    SELECT id FROM accounts.registration_requests WHERE expires_at <= clock_timestamp() ORDER BY expires_at LIMIT 1000
);

-- name: UserEmailTaken :one
SELECT EXISTS (SELECT 1 FROM accounts.users WHERE site_id = @site_id AND email = @email);
