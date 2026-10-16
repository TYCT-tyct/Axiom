-- name: InsertReferencePrice :one
INSERT INTO catalog.reference_price (model_id, currency, source, schedule, schedule_sha256, supersedes)
VALUES (@model_id, @currency, @source, @schedule, @schedule_sha256, @supersedes)
RETURNING id;

-- The current price of each currency is the row nothing replaces; a withdrawn currency's row has no schedule.
-- name: ListCurrentPricesOfModel :many
SELECT r.* FROM catalog.reference_price r
WHERE r.model_id = @model_id AND NOT EXISTS (SELECT FROM catalog.reference_price n WHERE n.supersedes = r.id);

-- name: ListCurrentPricesForSnapshot :many
SELECT r.* FROM catalog.reference_price r JOIN catalog.model m ON m.id = r.model_id
WHERE m.status = 'listed' AND r.schedule IS NOT NULL
    AND NOT EXISTS (SELECT FROM catalog.reference_price n WHERE n.supersedes = r.id);
