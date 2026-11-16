-- name: InvitationCode :one
SELECT invite_code FROM accounts.users
WHERE site_id = @site_id AND account_id = @account_id AND id = @user_id;

-- name: ListInvitees :many
-- UUIDv7 order and timestamp are the same registration order used by ListUsers.
SELECT id, status, uuid_extract_timestamp(id)::timestamptz AS registered_at
FROM accounts.users
WHERE site_id = @site_id AND inviter_user_id = @user_id
    AND (sqlc.narg(after)::uuid IS NULL OR id < sqlc.narg(after))
ORDER BY id DESC
LIMIT @page_size::int;
