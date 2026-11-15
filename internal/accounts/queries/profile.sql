-- name: ProfileName :one
SELECT name FROM accounts.accounts WHERE id = @account_id FOR UPDATE;

-- name: SetProfileName :exec
UPDATE accounts.accounts SET name = @name WHERE id = @account_id;

-- name: PutEmailChange :exec
INSERT INTO accounts.email_changes (site_id, account_id, user_id, email, token_hash, factor_version, language, expires_at)
VALUES (@site_id, @account_id, @user_id, @email, @token_hash, @factor_version, @language, CAST(CAST(sqlc.arg(expires_at) AS text) AS timestamptz))
ON CONFLICT (user_id) DO UPDATE SET email = excluded.email, token_hash = excluded.token_hash,
    factor_version = excluded.factor_version, language = excluded.language, expires_at = excluded.expires_at;

-- name: LockEmailChange :many
SELECT email, token_hash, factor_version, language, expires_at > CAST(CAST(sqlc.arg(now) AS text) AS timestamptz) AS unexpired
FROM accounts.email_changes WHERE user_id = @user_id FOR UPDATE;

-- name: DeleteEmailChange :exec
DELETE FROM accounts.email_changes WHERE user_id = @user_id;

-- name: SetProfileEmail :exec
UPDATE accounts.users SET email = @email, email_verified_at = @verified_at WHERE id = @user_id;

-- name: InvalidateEmailResetTokens :exec
UPDATE accounts.password_reset_tokens SET used_at = @used_at
WHERE user_id = @user_id AND used_at IS NULL;

-- name: PurgeEmailChanges :exec
DELETE FROM accounts.email_changes WHERE user_id IN (
    SELECT user_id FROM accounts.email_changes WHERE expires_at <= clock_timestamp() ORDER BY expires_at LIMIT 1000
);
