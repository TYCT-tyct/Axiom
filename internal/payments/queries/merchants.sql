-- name: InsertMerchant :exec
INSERT INTO payments.merchants (id, site_id, kind, name, enabled, currency, ttl_seconds, refund_enabled, user_refund, sort, rev)
VALUES (@id, @site_id, @kind, @name, @enabled, @currency, @ttl_seconds, @refund_enabled, @user_refund, @sort, 1);

-- name: LockMerchant :many
SELECT m.*, c.settings, c.ciphertext, c.nonce, c.wrapped_dek, c.key_version
FROM payments.merchants m JOIN payments.merchant_configs c ON c.merchant_id = m.id
WHERE m.id = @id FOR UPDATE OF m, c;

-- name: ReadMerchantConfig :many
SELECT m.*, c.settings, c.ciphertext, c.nonce, c.wrapped_dek, c.key_version
FROM payments.merchants m JOIN payments.merchant_configs c ON c.merchant_id = m.id
WHERE m.id = @id;

-- name: MerchantConfigs :many
SELECT m.id, m.kind, m.currency, m.rev, c.settings, c.ciphertext, c.nonce, c.wrapped_dek, c.key_version
FROM payments.merchants m JOIN payments.merchant_configs c ON c.merchant_id = m.id
WHERE m.id = ANY (@merchants::uuid[]);

-- name: MerchantRevision :many
SELECT rev FROM payments.merchants WHERE id = @id;

-- name: UpdateMerchant :exec
UPDATE payments.merchants SET name = @name, enabled = @enabled, currency = @currency, ttl_seconds = @ttl_seconds,
    refund_enabled = @refund_enabled, user_refund = @user_refund, sort = @sort, rev = rev + 1, updated_at = clock_timestamp()
WHERE id = @id;

-- name: SaveMerchantConfig :exec
INSERT INTO payments.merchant_configs AS c (merchant_id, site_id, settings, ciphertext, nonce, wrapped_dek, key_version, rev)
VALUES (@merchant_id, @site_id, @settings, @ciphertext, @nonce, @wrapped_dek, @key_version, @rev)
ON CONFLICT (merchant_id) DO UPDATE SET settings = excluded.settings, ciphertext = excluded.ciphertext,
    nonce = excluded.nonce, wrapped_dek = excluded.wrapped_dek, key_version = excluded.key_version, rev = excluded.rev;

-- name: DeleteOtherMethods :exec
DELETE FROM payments.merchant_methods WHERE merchant_id = @merchant_id AND NOT (method = ANY (@keep::text[]));

-- name: SaveMethod :exec
INSERT INTO payments.merchant_methods AS x (merchant_id, site_id, method, upstream, fee_ppm, fee_fixed, single_min, single_max, daily_limit, enabled)
VALUES (@merchant_id, @site_id, @method, @upstream, @fee_ppm, @fee_fixed, @single_min, @single_max, @daily_limit, @enabled)
ON CONFLICT (merchant_id, method) DO UPDATE SET upstream = excluded.upstream, fee_ppm = excluded.fee_ppm,
    fee_fixed = excluded.fee_fixed, single_min = excluded.single_min, single_max = excluded.single_max,
    daily_limit = excluded.daily_limit, enabled = excluded.enabled;

-- name: ListMerchants :many
SELECT * FROM payments.merchants WHERE site_id = @site_id ORDER BY sort, id;

-- name: ListMethods :many
SELECT * FROM payments.merchant_methods WHERE merchant_id = ANY (@merchants::uuid[]) ORDER BY merchant_id, method;

-- name: SiteSettings :many
SELECT * FROM payments.settings WHERE site_id = @site_id;

-- The enabled merchant methods of a site, with what quoting, choosing and ordering need of their merchant.
-- name: Offers :many
SELECT x.merchant_id, x.method, x.fee_ppm, x.fee_fixed, x.single_min, x.single_max, x.daily_limit, m.currency, m.sort, m.rev, m.ttl_seconds
FROM payments.merchant_methods x JOIN payments.merchants m ON m.id = x.merchant_id
WHERE x.site_id = @site_id AND x.enabled AND m.enabled AND (sqlc.narg(method)::text IS NULL OR x.method = sqlc.narg(method))
ORDER BY x.method, m.sort, m.id;

-- The day is the transaction's UTC date, the day an order made in it counts against.
-- name: UsageToday :many
SELECT merchant_id, used FROM payments.merchant_usage
WHERE merchant_id = ANY (@merchants::uuid[]) AND day = (transaction_timestamp() AT TIME ZONE 'UTC')::date;

-- Adds amount to the merchant's usage today if it stays within the limit, locking the row: concurrent orders wait here
-- and re-check against what the one before them added. No row means it does not fit.
-- name: AddUsage :many
INSERT INTO payments.merchant_usage AS u (merchant_id, site_id, day, used)
VALUES (@merchant_id, @site_id, (transaction_timestamp() AT TIME ZONE 'UTC')::date, @amount)
ON CONFLICT (merchant_id, day) DO UPDATE SET used = u.used + excluded.used
WHERE sqlc.narg(daily_limit)::bigint IS NULL OR u.used + excluded.used <= sqlc.narg(daily_limit)
RETURNING used;
