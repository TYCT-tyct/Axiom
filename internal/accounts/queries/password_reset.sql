-- name: RequestPasswordReset :exec
INSERT INTO accounts.password_reset_requests (site_id, email_hash, id, email, domain, language)
VALUES (@site_id, @email_hash, @id, @email, @domain, @language)
ON CONFLICT (site_id, email_hash) DO UPDATE
SET id = excluded.id, email = excluded.email, domain = excluded.domain, language = excluded.language, expires_at = excluded.expires_at;

-- name: LockPasswordResetRequest :many
SELECT site_id, id, email, domain, language, expires_at > clock_timestamp() AS unexpired
FROM accounts.password_reset_requests WHERE id = @id FOR UPDATE;

-- name: FinishPasswordResetRequest :exec
UPDATE accounts.password_reset_requests SET email = NULL WHERE id = @id;

-- name: PasswordResetSite :many
-- Match the console order return URL: use an enabled HTTPS console domain owned by the site.
SELECT s.name, d.domain FROM accounts.sites s
JOIN accounts.site_domains d ON d.site_id = s.id
WHERE s.id = @site_id AND s.status = 'active' AND d.surface = 'console' AND d.disabled_at IS NULL
    AND d.origin = 'https://' || d.domain
ORDER BY (d.domain = @domain::text) DESC, d.domain LIMIT 1;

-- name: ResetUser :many
SELECT id, account_id
FROM accounts.users WHERE site_id = @site_id AND email = @email;

-- name: InsertPasswordResetToken :exec
INSERT INTO accounts.password_reset_tokens (id, site_id, account_id, user_id, token_hash, factor_version, expires_at)
SELECT r.id, r.site_id, @account_id, @user_id, @token_hash, @factor_version, r.expires_at
FROM accounts.password_reset_requests r WHERE r.id = @id;

-- name: PasswordResetTenant :many
SELECT t.id::uuid AS id, t.user_id::uuid AS user_id, t.account_id::uuid AS account_id
FROM accounts.password_reset_tenant(@domain::text, @token_hash::bytea) AS t (id, user_id, account_id);

-- name: ConsumePasswordResetToken :execrows
UPDATE accounts.password_reset_tokens SET used_at = clock_timestamp()
WHERE id = @id AND user_id = @user_id AND token_hash = @token_hash
    AND factor_version = @factor_version AND expires_at > clock_timestamp() AND used_at IS NULL;

-- name: PasswordRecipient :one
SELECT email FROM accounts.users WHERE id = @id;

-- name: PurgePasswordResetTokens :exec
DELETE FROM accounts.password_reset_tokens WHERE id IN (
    SELECT id FROM accounts.password_reset_tokens WHERE expires_at <= clock_timestamp() ORDER BY expires_at LIMIT 1000
);

-- name: PurgePasswordResetRequests :exec
DELETE FROM accounts.password_reset_requests WHERE id IN (
    SELECT id FROM accounts.password_reset_requests WHERE expires_at <= clock_timestamp() ORDER BY expires_at LIMIT 1000
);
