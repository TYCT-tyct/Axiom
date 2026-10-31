-- The code rows and their redemption, and the per-account and per-IP attempt counters (README.md「兑换码」).

-- A code is written once and only its state changes afterwards. created_by is the operator's user ID.
-- name: InsertRedeemCode :exec
INSERT INTO payments.redeem_codes (id, site_id, code_hash, code_hint, amount, note, expires_at, created_by)
VALUES (@id, @site_id, @code_hash, @code_hint, @amount, @note, sqlc.narg(expires_at), @created_by);

-- The redemption locks the code row: concurrent redeems of one code wait here and the second finds it used. It does not
-- return a timestamp: transaction_timestamp() is the transaction's start, before this wait, so the decision reads the
-- clock after the lock instead (RedeemClock).
-- name: RedeemCodeByHash :one
SELECT sqlc.embed(c) FROM payments.redeem_codes c WHERE c.site_id = @site_id AND c.code_hash = @code_hash FOR UPDATE;

-- The time of the redemption decision, read after the code's row lock: the lock may have waited, and the code may have
-- expired during it. clock_timestamp() is the statement's clock, not the transaction's start.
-- name: RedeemClock :one
SELECT clock_timestamp()::timestamptz AS now;

-- A code by ID, locked: disabling and reading the answer happen under the same row lock.
-- name: LockRedeemCode :one
SELECT sqlc.embed(c), transaction_timestamp()::timestamptz AS now
FROM payments.redeem_codes c WHERE c.id = @id FOR UPDATE;

-- One site's codes, newest first. state filters by what the page shows: expired is an enabled code past its expiry.
-- Every other filter left out does not narrow; the (created_at, id) bound is the page cursor.
-- name: ListRedeemCodes :many
SELECT sqlc.embed(c), transaction_timestamp()::timestamptz AS now
FROM payments.redeem_codes c
WHERE c.site_id = @site_id
    AND (sqlc.narg(state)::text IS NULL
        OR (sqlc.narg(state)::text = 'expired' AND c.state = 'enabled'
            AND c.expires_at IS NOT NULL AND c.expires_at <= transaction_timestamp())
        OR (sqlc.narg(state)::text = 'enabled' AND c.state = 'enabled'
            AND (c.expires_at IS NULL OR c.expires_at > transaction_timestamp()))
        OR (sqlc.narg(state)::text IN ('used', 'disabled') AND c.state = sqlc.narg(state)::text))
    AND (sqlc.narg(note)::text IS NULL OR starts_with(lower(c.note), lower(sqlc.narg(note)::text)))
    AND (c.created_at, c.id) < (@after_at::timestamptz, @after_id::uuid)
ORDER BY c.created_at DESC, c.id DESC
LIMIT @max;

-- The serialization point of one code crediting once, even if the state check above ever raced, and the expiry check at
-- the clock of this statement: a lock that waited past the expiry cannot credit.
-- name: UseRedeemCode :execrows
UPDATE payments.redeem_codes SET state = 'used', redeemed_at = clock_timestamp()
WHERE id = @id AND state = 'enabled' AND (expires_at IS NULL OR expires_at > clock_timestamp());

-- name: DisableRedeemCode :execrows
UPDATE payments.redeem_codes SET state = 'disabled', disabled_at = transaction_timestamp(), disabled_by = @disabled_by
WHERE id = @id AND state = 'enabled';

-- The account's history row; its ID is the credit batch's ID, so the ledger names the same thing.
-- name: InsertRedemption :exec
INSERT INTO payments.redeem_redemptions (id, site_id, code_id, account_id, user_id, amount, code_hint, expires_at,
    idem_key)
VALUES (@id, @site_id, @code_id, @account_id, @user_id, @amount, @code_hint, sqlc.narg(expires_at), @idem_key);

-- A key the account used: the redemption it made and the hash of the code it redeemed, so a replay can tell whether
-- the request is the same one.
-- name: RedemptionByKey :one
SELECT r.id, r.site_id, r.code_id, c.code_hash, r.account_id, r.amount, r.code_hint, r.expires_at, r.created_at
FROM payments.redeem_redemptions r JOIN payments.redeem_codes c ON c.id = r.code_id
WHERE r.account_id = @account_id AND r.idem_key = @idem_key;

-- The account's redemptions, newest first; the (created_at, id) bound is the page cursor.
-- name: ListRedemptions :many
SELECT id, amount, code_hint, expires_at, created_at
FROM payments.redeem_redemptions
WHERE account_id = @account_id AND (created_at, id) < (@after_at::timestamptz, @after_id::uuid)
ORDER BY created_at DESC, id DESC
LIMIT @max;

-- Takes one place of the account's or the IP group's attempts in a window of @window_seconds that opens with the first
-- attempt after the last one ended. No row returned means the limit is used up (RedeemAttemptWindow says which, under
-- the row lock). Concurrent attempts wait here and re-check what the one before them added.
-- name: ClaimRedeemAttempt :many
INSERT INTO payments.redeem_attempts AS a (site_id, kind, subject, window_start, attempts)
VALUES (@site_id, @kind, @subject, transaction_timestamp(), 1)
ON CONFLICT (site_id, kind, subject) DO UPDATE SET
    window_start = CASE WHEN a.window_start <= transaction_timestamp() - make_interval(secs => @window_seconds::bigint)
        THEN transaction_timestamp() ELSE a.window_start END,
    attempts = CASE WHEN a.window_start <= transaction_timestamp() - make_interval(secs => @window_seconds::bigint)
        THEN 1 ELSE a.attempts + 1 END
WHERE a.window_start <= transaction_timestamp() - make_interval(secs => @window_seconds::bigint)
    OR a.attempts < @max_attempts::integer
RETURNING a.window_start;

-- Read after ClaimRedeemAttempt took nothing, in the same transaction and so under the row lock: how long until the
-- window is over.
-- name: RedeemAttemptWindow :one
SELECT greatest(1, ceil(extract(epoch FROM a.window_start + make_interval(secs => @window_seconds::bigint)
    - transaction_timestamp())))::bigint AS wait_seconds
FROM payments.redeem_attempts a WHERE a.site_id = @site_id AND a.kind = @kind AND a.subject = @subject;

-- One account's redemptions are checked, written and credited under its own transaction lock, so two requests with one
-- idempotency key cannot both make a redemption.
-- name: LockRedeemAccount :exec
SELECT pg_advisory_xact_lock(@key::bigint);
