-- name: PriceEnvelopes :many
SELECT * FROM operations.price_envelopes ORDER BY site_id;

-- name: LockPriceEnvelope :many
-- FOR SHARE by a publication, so a change of the envelope waits for the publications weighed against it.
SELECT * FROM operations.price_envelopes WHERE site_id = @site_id FOR SHARE;

-- name: LockPriceEnvelopeForUpdate :many
SELECT * FROM operations.price_envelopes WHERE site_id = @site_id FOR UPDATE;

-- name: InsertPriceEnvelope :one
INSERT INTO operations.price_envelopes (site_id, min_margin_bps, model_daily_change_bps, daily_changes, daily_models, rev)
VALUES (@site_id, @min_margin_bps, @model_daily_change_bps, @daily_changes, @daily_models, 1)
RETURNING *;

-- name: UpdatePriceEnvelope :one
UPDATE operations.price_envelopes SET min_margin_bps = @min_margin_bps, model_daily_change_bps = @model_daily_change_bps,
    daily_changes = @daily_changes, daily_models = @daily_models, rev = rev + 1, updated_at = now()
WHERE site_id = @site_id
RETURNING *;

-- name: EnsurePriceCounters :exec
-- The site's row and the models' rows of a UTC day, made at zero if they are not there yet.
INSERT INTO operations.price_counters (site_id, day_start, model_id)
SELECT @site_id, @day_start, m FROM unnest(array_prepend(NULL::uuid, @model_ids::uuid[])) AS m
ON CONFLICT DO NOTHING;

-- name: LockPriceCounters :many
-- The site's row, then the models' in ascending order: every path locks them in this order.
SELECT * FROM operations.price_counters
WHERE site_id = @site_id AND day_start = @day_start AND (model_id IS NULL OR model_id = ANY(@model_ids::uuid[]))
ORDER BY model_id NULLS FIRST
FOR UPDATE;

-- name: CountPriceChange :exec
-- One publication of the model: its move added to the model's row, and the publication, and the model when it is its
-- first move of the day, counted in the site's.
UPDATE operations.price_counters SET
    change_bps = change_bps + CASE WHEN model_id IS NULL THEN 0 ELSE @change_bps::bigint END,
    count = count + 1,
    models = models + CASE WHEN model_id IS NULL AND @first_move::bool THEN 1 ELSE 0 END
WHERE site_id = @site_id AND day_start = @day_start AND (model_id IS NULL OR model_id = @model_id::uuid);

-- name: PriceUsage :many
-- Every envelope with its site's and its models' rows of the current UTC day; a site without one today has none.
SELECT e.site_id, e.min_margin_bps, e.model_daily_change_bps, e.daily_changes, e.daily_models, e.rev, e.updated_at,
    w.day_start, c.model_id, coalesce(c.change_bps, 0)::bigint AS change_bps, coalesce(c.count, 0)::integer AS count,
    coalesce(c.models, 0)::integer AS models
FROM operations.price_envelopes e
CROSS JOIN (SELECT date_trunc('day', clock_timestamp(), 'UTC')::timestamptz AS day_start) w
LEFT JOIN operations.price_counters c ON c.site_id = e.site_id AND c.day_start = w.day_start
ORDER BY e.site_id, c.model_id NULLS FIRST;
