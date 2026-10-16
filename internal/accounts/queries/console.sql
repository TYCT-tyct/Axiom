-- Sign-in, sessions, two-step verification, registration and authorization (README.md「注册、登录与会话」). sqlc
-- cannot see a function's result columns, so the lookups name and type them.

-- name: LoginTenant :many
SELECT k.user_id::uuid AS user_id, k.account_id::uuid AS account_id
FROM accounts.login_tenant(@domain::text, @email::text) AS k (user_id, account_id);

-- name: InviteTenant :many
SELECT k.site_id::uuid AS site_id, k.inviter_user_id::uuid AS inviter_user_id, k.default_tier_id::uuid AS default_tier_id
FROM accounts.invite_tenant(@domain::text, @code::text) AS k (site_id, inviter_user_id, default_tier_id);

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
    totp_key_version, totp_last_step, (mfa_enabled_at IS NOT NULL)::boolean AS mfa
FROM accounts.users WHERE id = @id FOR UPDATE;

-- name: InsertSession :exec
INSERT INTO accounts.sessions (id, site_id, account_id, user_id, surface, token_hash, csrf_token, level, factor_version,
    expires_at, idle_timeout, ip, user_agent)
VALUES (@id, @site_id, @account_id, @user_id, @surface, @token_hash, @csrf_token, @level, @factor_version,
    now() + make_interval(secs => @max_age_seconds::bigint), make_interval(secs => @idle_seconds::bigint),
    nullif(@ip::text, '')::inet, nullif(@user_agent::text, ''));

-- name: ValidSession :many
-- A session holds while it is not revoked, expired or idle too long, its user is enabled, and its factor version is
-- the user's.
SELECT s.site_id, s.surface, s.level, s.csrf_token, s.factor_version
FROM accounts.sessions s
JOIN accounts.users u ON u.site_id = s.site_id AND u.account_id = s.account_id AND u.id = s.user_id
WHERE s.id = @id AND s.revoked_at IS NULL AND s.expires_at > now() AND s.last_seen_at + s.idle_timeout > now()
    AND u.status = 'active' AND u.factor_version = s.factor_version;

-- name: TouchSession :exec
-- Idle time counts from the last request, written at most once a minute.
UPDATE accounts.sessions SET last_seen_at = now() WHERE id = @id AND last_seen_at < now() - interval '1 minute';

-- name: RevokeSession :execrows
UPDATE accounts.sessions SET revoked_at = now() WHERE id = @id;

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
UPDATE accounts.users SET totp_ciphertext = @ciphertext, totp_nonce = @nonce, totp_wrapped_dek = @wrapped_dek,
    totp_key_version = @key_version, totp_last_step = @step, mfa_enabled_at = now(), factor_version = factor_version + 1
WHERE id = @id;

-- name: BumpFactorVersion :exec
UPDATE accounts.users SET factor_version = factor_version + 1 WHERE id = @id;

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
    inviter_user_id)
VALUES (@id, @site_id, @account_id, @email, @password_hash, 1, 'active', @invite_code, @inviter_user_id);

-- name: InsertOwner :exec
INSERT INTO accounts.members (site_id, account_id, account_kind, user_id, user_account_id, role_id)
VALUES (@site_id, @account_id, 'personal', @user_id, @account_id, @role_id);

-- name: MemberRoles :many
SELECT role_id FROM accounts.members WHERE account_id = @account_id AND user_id = @user_id;

-- name: PlatformRoles :many
SELECT role_id FROM accounts.platform_grants WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: SessionInfo :many
-- What a page shows of the session it is in; the session ends at its absolute end or after the idle time from now,
-- whichever comes first.
SELECT u.email, u.invite_code, (u.mfa_enabled_at IS NOT NULL)::boolean AS mfa, s.csrf_token,
    least(s.expires_at, now() + s.idle_timeout)::timestamptz AS expires_at
FROM accounts.sessions s
JOIN accounts.users u ON u.site_id = s.site_id AND u.account_id = s.account_id AND u.id = s.user_id
WHERE s.id = @id;
