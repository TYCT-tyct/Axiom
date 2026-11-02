-- Sign-in, sessions, two-step verification, registration and authorization (README.md「注册、登录与会话」). sqlc
-- cannot see a function's result columns, so the lookups name and type them.

-- name: LoginTenant :many
SELECT k.user_id::uuid AS user_id, k.account_id::uuid AS account_id
FROM accounts.login_tenant(@domain::text, @email::text) AS k (user_id, account_id);

-- name: InviteTenant :many
SELECT k.site_id::uuid AS site_id, k.inviter_user_id::uuid AS inviter_user_id, k.default_tier_id::uuid AS default_tier_id
FROM accounts.invite_tenant(@domain::text, @code::text) AS k (site_id, inviter_user_id, default_tier_id);

-- name: AccountTier :one
-- The tier whose sale prices the account pays; the account row is the only row the console scope can read.
SELECT tier_id FROM accounts.accounts WHERE id = @account_id;

-- name: SessionTenant :many
SELECT k.session_id::uuid AS session_id, k.user_id::uuid AS user_id, k.account_id::uuid AS account_id
FROM accounts.session_tenant(@domain::text, @surface::text, @token_hash::bytea) AS k (session_id, user_id, account_id);

-- name: UserPassword :many
-- Read before the issuing transaction, so the slow hash runs outside it.
SELECT password_hash, factor_version FROM accounts.users WHERE id = @id;

-- name: LockUser :many
-- The first statement of every transaction that issues a session, spends a one-time factor or changes one: the lock
-- keeps a factor change from slipping in between the check and the write.
SELECT site_id, factor_version, status = 'active' AS active, totp_ciphertext, totp_nonce, totp_wrapped_dek,
    totp_key_version, totp_last_step, (mfa_enabled_at IS NOT NULL)::boolean AS mfa,
    EXISTS (SELECT 1 FROM accounts.passkey_credentials p WHERE p.user_id = u.id)::boolean AS passkeys
FROM accounts.users u WHERE u.id = @id FOR UPDATE;

-- name: InsertSession :exec
INSERT INTO accounts.sessions (id, site_id, account_id, user_id, surface, token_hash, csrf_token, level, factor_version,
    expires_at, idle_timeout, ip, user_agent)
VALUES (@id, @site_id, @account_id, @user_id, @surface, @token_hash, @csrf_token, @level, @factor_version,
    now() + make_interval(secs => @max_age_seconds::bigint), make_interval(secs => @idle_seconds::bigint),
    nullif(@ip::text, '')::inet, nullif(@user_agent::text, ''));

-- name: ValidSession :many
-- A session holds while it is not revoked, expired or idle too long, its user is enabled, and its factor version is
-- the user's. The site's lifetimes now (the caller's, in seconds) cut what the session was issued with, never extend it:
-- a site that shortens its sessions writes that into the ones already made (CutSessions), and this cut also holds a
-- session another node issued from its snapshot before reloading the change.
SELECT s.site_id, s.surface, s.level, s.csrf_token, s.factor_version,
    EXISTS (
        SELECT 1
        FROM accounts.legal_documents d
        WHERE d.site_id = s.site_id
            AND d.version = (SELECT max(x.version) FROM accounts.legal_documents x
                WHERE x.site_id = d.site_id AND x.kind = d.kind)
            AND NOT EXISTS (
                SELECT 1 FROM accounts.legal_acceptances a
                WHERE a.site_id = s.site_id AND a.user_id = s.user_id
                    AND a.kind = d.kind AND a.version >= d.version
            )
    ) AS consent_pending
FROM accounts.sessions s
JOIN accounts.users u ON u.site_id = s.site_id AND u.account_id = s.account_id AND u.id = s.user_id
WHERE s.id = @id AND s.revoked_at IS NULL AND s.expires_at > now()
    AND s.created_at + make_interval(secs => @max_age_seconds::bigint) > now()
    AND s.last_seen_at + least(s.idle_timeout, make_interval(secs => @idle_seconds::bigint)) > now()
    AND u.status = 'active' AND u.factor_version = s.factor_version;

-- name: TouchSession :exec
-- Idle time counts from the last request, written at most once a minute.
UPDATE accounts.sessions SET last_seen_at = now() WHERE id = @id AND last_seen_at < now() - interval '1 minute';

-- name: RevokeSession :execrows
UPDATE accounts.sessions SET revoked_at = now() WHERE id = @id;

-- name: ListSessions :many
-- The user's live console sessions, newest first, as the device list shows them. A pending sign-in has not passed the
-- second factor and is not a signed-in device; the same validity cuts as ValidSession keep a session the site has
-- shortened, an idle one and one from before a factor change out of the list.
SELECT s.id, s.created_at, s.last_seen_at, coalesce(host(s.ip), '')::text AS ip,
    coalesce(s.user_agent, '') AS user_agent
FROM accounts.sessions s
JOIN accounts.users u ON u.site_id = s.site_id AND u.account_id = s.account_id AND u.id = s.user_id
WHERE s.user_id = @user_id AND s.site_id = @site_id AND s.surface = 'console'
    AND s.level <> 'pending_mfa'
    AND s.revoked_at IS NULL AND s.expires_at > now()
    AND s.created_at + make_interval(secs => @max_age_seconds::bigint) > now()
    AND s.last_seen_at + least(s.idle_timeout, make_interval(secs => @idle_seconds::bigint)) > now()
    AND u.status = 'active' AND u.factor_version = s.factor_version
ORDER BY s.created_at DESC, s.id DESC
LIMIT 100;

-- name: RevokeDeviceSession :execrows
-- Only the user's own live console sessions other than the one asking: the account scope and this predicate both keep
-- one member of a future shared account from ending another's sign-in, and another surface's session is out of reach.
-- The cuts are ListSessions' and ValidSession's, so the endpoint finds exactly a session the list showed.
UPDATE accounts.sessions SET revoked_at = now()
WHERE id = @id AND site_id = @site_id AND user_id = @user_id AND surface = 'console'
    AND level <> 'pending_mfa' AND id <> @current_id AND factor_version = @factor_version
    AND revoked_at IS NULL AND expires_at > now()
    AND created_at + make_interval(secs => @max_age_seconds::bigint) > now()
    AND last_seen_at + least(idle_timeout, make_interval(secs => @idle_seconds::bigint)) > now();

-- name: RevokeOtherSessions :execrows
-- Every other live console session of the user, pending ones included: a sign-in that has the password but not the
-- second factor must not outlive the user's choice to end the others. Factor-stale rows are already invalid and are
-- not counted as devices the user ended.
UPDATE accounts.sessions SET revoked_at = now()
WHERE site_id = @site_id AND user_id = @user_id AND surface = 'console'
    AND id <> @current_id AND factor_version = @factor_version
    AND revoked_at IS NULL AND expires_at > now()
    AND created_at + make_interval(secs => @max_age_seconds::bigint) > now()
    AND last_seen_at + least(idle_timeout, make_interval(secs => @idle_seconds::bigint)) > now();

-- name: PendingSession :many
SELECT id, csrf_token, factor_version, mfa_attempts FROM accounts.sessions
WHERE token_hash = @token_hash AND level = 'pending_mfa' AND revoked_at IS NULL AND expires_at > now();

-- name: CountMFAAttempt :exec
UPDATE accounts.sessions SET mfa_attempts = mfa_attempts + 1 WHERE id = @id;

-- name: AdvanceTOTP :execrows
-- A code counts once: the step must be later than the last one used.
UPDATE accounts.users SET totp_last_step = @step WHERE id = @id AND totp_last_step < @step;

-- name: UseRecoveryCode :execrows
-- A code of the current factor version not used yet; the table's policy updates only unused codes.
UPDATE accounts.recovery_codes SET used_at = now()
WHERE user_id = @user_id AND code_hash = @code_hash AND factor_version = @factor_version;

-- name: EnableTOTP :exec
-- The factor version is raised by finishFactorChange, after the change and before the new recovery codes.
UPDATE accounts.users SET totp_ciphertext = @ciphertext, totp_nonce = @nonce, totp_wrapped_dek = @wrapped_dek,
    totp_key_version = @key_version, totp_last_step = @step, mfa_enabled_at = now()
WHERE id = @id;

-- name: ReplaceTOTP :exec
-- The user keeps the date two-step verification was enabled; only the secret and its code step change.
UPDATE accounts.users SET totp_ciphertext = @ciphertext, totp_nonce = @nonce, totp_wrapped_dek = @wrapped_dek,
    totp_key_version = @key_version, totp_last_step = @step
WHERE id = @id;

-- name: BumpFactorVersion :exec
UPDATE accounts.users SET factor_version = factor_version + 1 WHERE id = @id;

-- name: SetSessionRecoveryCodes :exec
-- Codes for the console to read once, encrypted under the session token, when the answer that made them is a redirect
-- (a provider link).
UPDATE accounts.sessions
SET recovery_codes_ciphertext = @ciphertext, recovery_codes_nonce = @nonce
WHERE id = @id;

-- name: LockSessionRecoveryCodes :one
-- The row lock is held until the clearing update in the same transaction, so two calls cannot both read the codes.
SELECT recovery_codes_ciphertext, recovery_codes_nonce
FROM accounts.sessions WHERE id = @id AND recovery_codes_ciphertext IS NOT NULL FOR UPDATE;

-- name: ClearSessionRecoveryCodes :exec
UPDATE accounts.sessions
SET recovery_codes_ciphertext = NULL, recovery_codes_nonce = NULL
WHERE id = @id;

-- name: InsertRecoveryCode :exec
INSERT INTO accounts.recovery_codes (id, site_id, account_id, user_id, factor_version, code_hash)
VALUES (@id, @site_id, @account_id, @user_id, @factor_version, @code_hash);

-- name: InsertAccount :exec
INSERT INTO accounts.accounts (id, site_id, kind, name, status, tier_id)
VALUES (@id, @site_id, 'personal', @name, 'active', @tier_id);

-- name: InsertDefaultWorkspace :exec
INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
VALUES (@id, @site_id, @account_id, 'Default', 'active', true);

-- name: InsertUser :exec
INSERT INTO accounts.users (id, site_id, account_id, email, password_hash, factor_version, status, invite_code,
    inviter_user_id, email_verified_at)
VALUES (@id, @site_id, @account_id, @email, @password_hash, 1, 'active', @invite_code, @inviter_user_id,
    @email_verified_at);

-- name: InsertOwner :exec
INSERT INTO accounts.members (site_id, account_id, account_kind, user_id, user_account_id, role_id)
VALUES (@site_id, @account_id, 'personal', @user_id, @account_id, @role_id);

-- name: MemberRoles :many
SELECT role_id FROM accounts.members WHERE account_id = @account_id AND user_id = @user_id;

-- name: PlatformRoles :many
SELECT role_id FROM accounts.platform_grants WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: SessionInfo :many
-- What a page shows of the session it is in; the session ends at its absolute end or after the idle time from now,
-- whichever comes first, both cut by the site's lifetimes now (ValidSession).
SELECT u.email, u.invite_code, (u.mfa_enabled_at IS NOT NULL)::boolean AS mfa,
    (SELECT count(*) FROM accounts.passkey_credentials p WHERE p.user_id = u.id)::bigint AS passkeys, s.csrf_token,
    least(s.expires_at, s.created_at + make_interval(secs => @max_age_seconds::bigint),
    now() + least(s.idle_timeout, make_interval(secs => @idle_seconds::bigint)))::timestamptz AS expires_at
FROM accounts.sessions s
JOIN accounts.users u ON u.site_id = s.site_id AND u.account_id = s.account_id AND u.id = s.user_id
WHERE s.id = @id;

-- name: BillingPreference :one
-- The funding order calls of this account use: its own choice, or the site's default, or subscription_first. While a
-- node of the previous release is live, the effective order is the safe default; own only says whether the account
-- chose a value that is currently in force.
SELECT CASE WHEN coalesce((SELECT enabled FROM platform.billing_preference_readiness WHERE id), true)
        THEN coalesce(a.billing_preference, p.preference, 'subscription_first')
        ELSE 'subscription_first' END::text AS preference,
    (coalesce((SELECT enabled FROM platform.billing_preference_readiness WHERE id), true)
        AND a.billing_preference IS NOT NULL)::boolean AS own,
    coalesce(p.preference, 'subscription_first')::text AS site_default
FROM accounts.accounts a LEFT JOIN accounts.site_billing_preferences p ON p.site_id = a.site_id
WHERE a.id = @account_id;

-- name: BillingPreferenceReady :one
SELECT coalesce((SELECT enabled FROM platform.billing_preference_readiness WHERE id), true)::boolean AS ready;

-- name: SetBillingPreference :exec
-- A NULL clears the account's choice, so the site's default applies again.
UPDATE accounts.accounts SET billing_preference = sqlc.narg('preference')::text WHERE id = @account_id;
