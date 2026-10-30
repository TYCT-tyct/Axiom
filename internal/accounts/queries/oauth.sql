-- Third-party sign-in, its site-level applications and provider identities (README.md「第三方登录」).

-- name: ListSiteOAuthProviders :many
-- The snapshot's view of what each site has configured: a row means the provider is on for that site. The secret is
-- not part of the snapshot; the callback opens it in the site's transaction.
SELECT site_id, provider, client_id FROM accounts.site_oauth_providers ORDER BY site_id, provider;

-- name: ReadSiteOAuthProvider :one
SELECT id, client_id, client_secret_ciphertext, client_secret_nonce, client_secret_wrapped_dek, client_secret_key_version
FROM accounts.site_oauth_providers WHERE site_id = @site_id AND provider = @provider;

-- name: UpsertSiteOAuthProvider :exec
INSERT INTO accounts.site_oauth_providers (id, site_id, provider, client_id, client_secret_ciphertext,
    client_secret_nonce, client_secret_wrapped_dek, client_secret_key_version)
VALUES (@id, @site_id, @provider, @client_id, @ciphertext, @nonce, @wrapped_dek, @key_version)
ON CONFLICT (site_id, provider) DO UPDATE SET client_id = excluded.client_id,
    client_secret_ciphertext = excluded.client_secret_ciphertext, client_secret_nonce = excluded.client_secret_nonce,
    client_secret_wrapped_dek = excluded.client_secret_wrapped_dek,
    client_secret_key_version = excluded.client_secret_key_version, updated_at = now();

-- name: DeleteSiteOAuthProvider :execrows
DELETE FROM accounts.site_oauth_providers WHERE site_id = @site_id AND provider = @provider;

-- name: FindIdentity :many
-- The one match a sign-in may make: the subject the provider issued for this application. The email address the
-- provider reports never joins this decision (docs/domains/accounts.md「不自动合并身份」).
SELECT user_id, account_id FROM accounts.user_identities
WHERE site_id = @site_id AND provider = @provider AND subject = @subject;

-- name: InsertIdentity :exec
INSERT INTO accounts.user_identities (id, site_id, account_id, user_id, provider, subject, display_name)
VALUES (@id, @site_id, @account_id, @user_id, @provider, @subject, @display_name);

-- name: DeleteIdentity :execrows
DELETE FROM accounts.user_identities WHERE user_id = @user_id AND provider = @provider;

-- name: ListIdentities :many
SELECT provider, display_name, created_at FROM accounts.user_identities WHERE user_id = @user_id ORDER BY provider;

-- name: InsertOAuthFlow :exec
INSERT INTO accounts.oauth_flows (id, site_id, provider, intent, account_id, user_id, session_id, state_hash,
    nonce_hash, verifier, redirect_uri, expires_at)
VALUES (@id, @site_id, @provider, @intent, @account_id, @user_id, @session_id, @state_hash, @nonce_hash, @verifier,
    @redirect_uri, now() + interval '10 minutes');

-- name: FindOAuthFlow :many
SELECT id, provider, intent, account_id, user_id, session_id, verifier, redirect_uri, nonce_hash,
    expires_at > now() AS unexpired
FROM accounts.oauth_flows
WHERE site_id = @site_id AND state_hash = @state_hash AND consumed_at IS NULL;

-- name: ConsumeOAuthFlow :execrows
-- A state turns into at most one code exchange, even when two callbacks arrive together.
UPDATE accounts.oauth_flows SET consumed_at = now()
WHERE id = @id AND consumed_at IS NULL AND expires_at > now();

-- name: DeleteExpiredAuthFlows :execrows
-- Both ceremonies' abandoned rows, in one statement so the maintenance job's budget covers them together.
WITH expired_oauth AS (
    DELETE FROM accounts.oauth_flows WHERE id IN (
        SELECT id FROM accounts.oauth_flows WHERE expires_at < now() ORDER BY expires_at LIMIT 1000
    ) RETURNING 1
), expired_webauthn AS (
    DELETE FROM accounts.webauthn_flows WHERE id IN (
        SELECT id FROM accounts.webauthn_flows WHERE expires_at < now() ORDER BY expires_at LIMIT 1000
    ) RETURNING 1
)
SELECT (SELECT count(*) FROM expired_oauth) + (SELECT count(*) FROM expired_webauthn) AS deleted;

-- name: ReadSiteSignIn :one
-- A site's sign-in switches for the admin page: the login domain and whether passkeys are on.
SELECT s.login_domain, coalesce(t.passkeys_enabled, false)::boolean AS passkeys_enabled
FROM accounts.sites s LEFT JOIN accounts.site_settings t ON t.site_id = s.id WHERE s.id = @site_id;

-- name: WriteSitePasskeys :exec
INSERT INTO accounts.site_settings (site_id, passkeys_enabled) VALUES (@site_id, @enabled)
ON CONFLICT (site_id) DO UPDATE SET passkeys_enabled = excluded.passkeys_enabled;

-- name: SetLoginDomain :execrows
UPDATE accounts.sites SET login_domain = @domain WHERE id = @site_id;

-- name: CountSiteSignInAssets :one
-- What a login domain change would break: registered passkeys bind to it, OAuth applications register its redirect
-- URI, and linked identities were made under it.
SELECT (SELECT count(*) FROM accounts.passkey_credentials p WHERE p.site_id = @site_id)::bigint AS passkeys,
    (SELECT count(*) FROM accounts.user_identities i WHERE i.site_id = @site_id)::bigint AS identities,
    (SELECT count(*) FROM accounts.site_oauth_providers o WHERE o.site_id = @site_id)::bigint AS providers;

-- name: ListSiteOAuthApps :many
SELECT provider, client_id FROM accounts.site_oauth_providers WHERE site_id = @site_id ORDER BY provider;

-- name: ListSiteConsoleDomains :many
SELECT domain, origin FROM accounts.site_domains
WHERE site_id = @site_id AND surface = 'console' AND disabled_at IS NULL ORDER BY domain;
