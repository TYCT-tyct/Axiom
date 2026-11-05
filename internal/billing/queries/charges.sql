-- name: ReadCharges :many
-- Only frozen sale inputs, never supplier cost or publication basis. One primary-key batch per visible call page.
SELECT request_id, prices[entry + 1]::text AS schedule, fallbacks[entry + 1]::text AS fallback,
    coalesce(nullif(price_versions[entry + 1], '00000000-0000-0000-0000-000000000000'::uuid)::text, '')::text AS price_version,
    final_usage, pricing_tiers::smallint AS tiers, waived, estimated, price, charged
FROM billing.reservations
WHERE request_id = ANY(@ids::uuid[]) AND state = 'settled' AND entry IS NOT NULL AND pricing_tiers IS NOT NULL;

-- name: ChargedCalls :many
-- The current charge of settled calls, after any correction. Activity reads this instead of its immutable call-log
-- snapshot: a corrected reservation's charged amount moved, and the ledger is the authority.
SELECT request_id, charged FROM billing.reservations
WHERE request_id = ANY(@ids::uuid[]) AND state IN ('settled', 'cleanup_settled');

-- name: SettledFinals :many
-- What a settlement stored of the named calls, for the call log to complete a record written before its settlement
-- committed. Corrections move charged and final_usage on the same row, so this is the charge as it stands now.
SELECT request_id, price_versions, entry, charged, waived, final_usage FROM billing.reservations
WHERE request_id = ANY(@ids::uuid[]) AND state = 'settled';
