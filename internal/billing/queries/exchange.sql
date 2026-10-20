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
INSERT INTO billing.exchange_rates (id, base, quote, version, numerator, denominator, kind, reason, created_by)
VALUES (@id, @base, @quote, @version, @numerator, @denominator, @kind, @reason, @created_by)
RETURNING *;
