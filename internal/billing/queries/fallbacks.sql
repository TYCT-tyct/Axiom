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

-- name: FallbackHolds :many
-- A page of the estimates that still hold part of their reservation and rest on usage only a fallback price covers,
-- after the given one in (correct_by, request_id) order, which the partial index walks and which pages stably while
-- windows open and close. An estimate a correction by cost charged past its reservation holds nothing and is left out.
-- An estimate always has its window, so correct_by is never null here.
SELECT r.request_id, r.site_id, r.account_id, (r.amount - r.charged)::bigint AS held, r.correct_by::timestamptz AS correct_by
FROM billing.reservations r
WHERE r.state = 'estimated' AND r.amount > r.charged AND jsonb_path_exists(r.final_usage, '$[*].unknown')
    AND (r.correct_by, r.request_id) > (@after_by::timestamptz, @after_id::uuid)
ORDER BY r.correct_by, r.request_id
LIMIT @page_size;

-- name: FallbackHoldTotals :one
SELECT count(*)::bigint AS holds, coalesce(sum(r.amount - r.charged), 0)::bigint AS held
FROM billing.reservations r
WHERE r.state = 'estimated' AND r.amount > r.charged AND jsonb_path_exists(r.final_usage, '$[*].unknown');

-- name: CloseHold :one
-- Run under the account's balance row lock, as every change to a reservation is: the window ends now, by the
-- database's clock, so cleanup settles the estimate and corrections leave the user's charge alone. It may hold
-- nothing, when a correction by cost charged past the reservation.
UPDATE billing.reservations SET correct_by = clock_timestamp()
WHERE request_id = @request_id AND state = 'estimated' AND correct_by > clock_timestamp()
RETURNING account_id, greatest(amount - charged, 0)::bigint AS held;
