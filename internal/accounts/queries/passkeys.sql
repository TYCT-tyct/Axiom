-- Passkeys as a sign-in factor and as a second factor (README.md「通行密钥」).

-- name: ListPasskeys :many
SELECT id, name, created_at, last_used_at, backup_eligible FROM accounts.passkey_credentials
WHERE user_id = @user_id ORDER BY created_at, id;

-- name: CountPasskeys :one
SELECT count(*)::bigint FROM accounts.passkey_credentials WHERE user_id = @user_id;

-- name: PasskeyTenant :many
-- The account of a credential a discoverable assertion named. The assertion's user handle must still equal the
-- user this returns; the lookup happens before the caller is known.
SELECT k.site_id::uuid AS site_id, k.account_id::uuid AS account_id, k.user_id::uuid AS user_id
FROM accounts.passkey_tenant(@credential_id::bytea) AS k (site_id, account_id, user_id);

-- name: ReadPasskeySite :one
-- The current switch and login domain, read in the transaction that stores a credential: a stale page or snapshot
-- cannot add one after an operator turned passkeys off or moved the relying party.
SELECT coalesce(t.passkeys_enabled, false)::boolean AS passkeys,
    coalesce(s.login_domain, '')::text AS login_domain,
    coalesce(d.origin, '')::text AS login_origin
FROM accounts.sites s
LEFT JOIN accounts.site_settings t ON t.site_id = s.id
LEFT JOIN accounts.site_domains d ON d.site_id = s.id AND d.domain = s.login_domain AND d.surface = 'console'
WHERE s.id = @site_id;

-- name: UserForWebAuthn :one
-- The relying party's user entity: the handle is the user's UUID, the name the email the person recognizes.
SELECT site_id, account_id, email, (status = 'active')::boolean AS active, factor_version
FROM accounts.users WHERE id = @id;

-- name: ListUserCredentials :many
SELECT id, credential_id, public_key, sign_count, transports, backup_eligible, backup_state, name
FROM accounts.passkey_credentials WHERE user_id = @user_id ORDER BY created_at, id;

-- name: InsertPasskeyCredential :exec
INSERT INTO accounts.passkey_credentials (id, site_id, account_id, user_id, credential_id, public_key, sign_count,
    transports, backup_eligible, backup_state, name)
VALUES (@id, @site_id, @account_id, @user_id, @credential_id, @public_key, @sign_count, @transports, @backup_eligible,
    @backup_state, @name);

-- name: UsePasskey :execrows
-- The counter only moves forward. A concurrent second assertion carrying the same or an older count changes no row;
-- zero is what a synced passkey reports, so it stays accepted.
UPDATE accounts.passkey_credentials SET sign_count = @sign_count, last_used_at = now(),
    backup_state = @backup_state
WHERE id = @id AND (sign_count < @sign_count OR (sign_count = 0 AND @sign_count = 0));

-- name: DeletePasskey :execrows
DELETE FROM accounts.passkey_credentials WHERE id = @id AND user_id = @user_id;

-- name: InsertWebAuthnFlow :exec
INSERT INTO accounts.webauthn_flows (id, site_id, kind, account_id, user_id, session_id, challenge,
    relying_party_id, origin, name, allowed_credential_ids, expires_at)
VALUES (@id, @site_id, @kind, @account_id, @user_id, @session_id, @challenge, @relying_party_id, @origin, @name,
    @allowed_credential_ids, now() + interval '5 minutes');

-- name: FindWebAuthnFlow :many
SELECT id, kind, account_id, user_id, session_id, challenge, relying_party_id, origin, name, allowed_credential_ids,
    expires_at, expires_at > now() AS unexpired
FROM accounts.webauthn_flows WHERE id = @id AND site_id = @site_id AND consumed_at IS NULL;

-- name: ConsumeWebAuthnFlow :execrows
UPDATE accounts.webauthn_flows SET consumed_at = now()
WHERE id = @id AND consumed_at IS NULL AND expires_at > now();
