-- What the module records of a sign-in, a registration and a two-step verification (README.md「安全事件」). The row is
-- written in the platform scope, the only one the table's inserts admit, and the application never changes or deletes
-- one.

-- name: InsertSecurityEvent :exec
-- request_id is the request's, empty outside a request; the address is already a keyed hash.
INSERT INTO accounts.security_events (site_id, account_id, user_id, session_id, request_id, kind, refused, reason,
    ip_hash)
VALUES (@site_id, sqlc.narg(account_id)::uuid, sqlc.narg(user_id)::uuid, sqlc.narg(session_id)::uuid,
    nullif(@request_id::text, '')::uuid, @kind, @refused, @reason, @ip_hash);
