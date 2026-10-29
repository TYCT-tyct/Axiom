-- The advisory namespace is dedicated to FX. Six ASCII bytes encode the normalized pair without collisions.
-- name: LockExchangePair :exec
SELECT pg_advisory_xact_lock(@pair_key::bigint);

-- name: CurrentExchangeRates :many
SELECT DISTINCT ON (base, quote) * FROM billing.exchange_rates ORDER BY base, quote, version DESC;

-- name: ExchangeHistory :many
SELECT * FROM billing.exchange_rates WHERE base = @base AND quote = @quote
AND (@before_version::bigint = 0 OR version < @before_version)
ORDER BY version DESC LIMIT 100;

-- name: LatestExchangeRate :many
SELECT * FROM billing.exchange_rates WHERE base = @base AND quote = @quote ORDER BY version DESC LIMIT 1;

-- name: InsertExchangeRate :one
INSERT INTO billing.exchange_rates (id, base, quote, version, numerator, denominator, kind, reason, created_by, source, source_at)
VALUES (@id, @base, @quote, @version, @numerator, @denominator, @kind, @reason, @created_by, @source, @source_at)
RETURNING *;

-- name: ExchangeRateByID :many
SELECT * FROM billing.exchange_rates WHERE id = @id;

-- name: LockExchangeSync :one
SELECT * FROM billing.exchange_sync WHERE singleton FOR UPDATE;

-- name: ClaimExchangeSync :exec
UPDATE billing.exchange_sync SET started_at = coalesce(started_at, @at), last_attempt = @at WHERE singleton;

-- name: CompleteExchangeSync :exec
UPDATE billing.exchange_sync SET last_success = @at, source_at = @source_at WHERE singleton;

-- name: SetExchangeAlert :exec
UPDATE billing.exchange_sync SET alerted = @alerted WHERE singleton;
