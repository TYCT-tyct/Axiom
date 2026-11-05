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
-- committed. Corrections move charged and final_usage on the same row, so this is the charge as it stands now. FOR
-- SHARE waits for a settlement that has written the row and not yet committed, then reads the row it committed: a
-- try the gateway gave up on may still be committing. The rows are locked by ID alone and their state is the caller's
-- to check, because a lock read skips a row whose visible version fails the WHERE clause without waiting for its writer.
SELECT request_id, state, price_versions, entry, charged, waived, final_usage FROM billing.reservations
WHERE request_id = ANY(@ids::uuid[]) ORDER BY request_id FOR SHARE;
