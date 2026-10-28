-- name: AllFallbacks :many
SELECT currency, body FROM billing.fallbacks ORDER BY currency;

-- name: ReadFallbacks :many
-- Admission's: the rows of the currencies its route's schedules are in.
SELECT currency, body FROM billing.fallbacks WHERE currency = ANY (@currencies::text[]);

-- name: SaveFallbacks :exec
INSERT INTO billing.fallbacks AS f (currency, body) VALUES (@currency, @body)
ON CONFLICT (currency) DO UPDATE SET body = excluded.body, updated_at = clock_timestamp();

-- name: NoteUnknownItem :execrows
-- 1 row: the field's first appearance on the model, which takes the model out of routing; 0: it was seen before.
INSERT INTO billing.unknown_items (model_id, field, request_id, site_id, account_id)
VALUES (@model_id, @field, @request_id, @site_id, @account_id)
ON CONFLICT (model_id, field) DO NOTHING;

-- name: UnknownItems :many
SELECT model_id, field, request_id, seen_at FROM billing.unknown_items WHERE cleared_at IS NULL ORDER BY model_id, field;

-- name: ClearUnknownItem :execrows
UPDATE billing.unknown_items SET cleared_at = clock_timestamp()
WHERE model_id = @model_id AND field = @field AND cleared_at IS NULL;
