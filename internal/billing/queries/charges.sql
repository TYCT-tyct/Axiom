-- name: ReadCharges :many
-- Only frozen sale inputs, never supplier cost or publication basis. One primary-key batch per visible call page.
SELECT request_id, prices[entry + 1]::text AS schedule, fallbacks[entry + 1]::text AS fallback,
    coalesce(nullif(price_versions[entry + 1], '00000000-0000-0000-0000-000000000000'::uuid)::text, '')::text AS price_version,
    final_usage, pricing_tiers::smallint AS tiers, waived, estimated, price, charged
FROM billing.reservations
WHERE request_id = ANY(@ids::uuid[]) AND state = 'settled' AND entry IS NOT NULL AND pricing_tiers IS NOT NULL;
