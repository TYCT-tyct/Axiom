-- name: RegisterPageSite :exec
INSERT INTO catalog.page_site (origin, delay_seconds) VALUES ($1, $2)
ON CONFLICT (origin) DO UPDATE SET delay_seconds = excluded.delay_seconds,
next_request_at = greatest(catalog.page_site.next_request_at, catalog.page_site.last_request_at + excluded.delay_seconds * interval '1 second');

-- name: InsertWatchedPage :one
INSERT INTO catalog.watched_page (author, url, origin, currency, section_start, section_end, interval_seconds)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id;

-- name: DisableWatchedPage :one
UPDATE catalog.watched_page SET enabled = false, lease_gen = lease_gen + 1 WHERE id = $1 RETURNING id;

-- name: ClaimWatchedPage :one
UPDATE catalog.watched_page SET lease_gen = lease_gen + 1, lease_until = clock_timestamp() + interval '5 minutes'
WHERE id = (SELECT id FROM catalog.watched_page WHERE enabled AND next_check_at <= clock_timestamp()
    AND lease_until <= clock_timestamp() ORDER BY next_check_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: LockWatchedPage :one
SELECT * FROM catalog.watched_page WHERE id = $1 FOR UPDATE;

-- name: LockPageForReview :one
SELECT p.id FROM catalog.watched_page p JOIN catalog.review r ON r.page_id = p.id
WHERE r.id = $1 FOR UPDATE OF p;

-- name: OpenPageReviewBaseline :one
SELECT previous_snapshot_id FROM catalog.review WHERE page_id = $1 AND status = 'open';

-- name: LockPageEvidence :one
SELECT p.author, p.currency, p.current_snapshot_id, s.text FROM catalog.watched_page p
JOIN catalog.page_snapshot s ON s.page_id = p.id AND s.id = p.current_snapshot_id
WHERE p.id = $1 AND p.enabled FOR SHARE OF p;

-- name: ReadPageEvidence :one
SELECT p.author, p.currency, p.current_snapshot_id, s.text FROM catalog.watched_page p
JOIN catalog.page_snapshot s ON s.page_id = p.id AND s.id = p.current_snapshot_id WHERE p.id = $1 AND p.enabled;

-- name: ReservePageRequest :one
UPDATE catalog.page_site SET last_request_at = clock_timestamp(),
    next_request_at = clock_timestamp() + greatest(delay_seconds, sqlc.arg(seconds)::bigint) * interval '1 second'
WHERE origin = sqlc.arg(origin) AND next_request_at <= clock_timestamp() RETURNING origin;

-- name: PageRequestWait :one
SELECT greatest(0, extract(epoch FROM next_request_at - clock_timestamp()))::double precision FROM catalog.page_site WHERE origin = $1;

-- name: DelayPageSite :exec
UPDATE catalog.page_site SET next_request_at = greatest(next_request_at,
    last_request_at + greatest(delay_seconds, sqlc.arg(seconds)::bigint) * interval '1 second') WHERE origin = $1;

-- name: BackoffPageSite :exec
UPDATE catalog.page_site SET next_request_at = greatest(next_request_at,
    clock_timestamp() + sqlc.arg(seconds)::bigint * interval '1 second') WHERE origin = $1;

-- name: InsertPageSnapshot :one
INSERT INTO catalog.page_snapshot (page_id, extractor_version, text, text_sha256) VALUES ($1, $2, $3, $4)
ON CONFLICT (page_id, extractor_version, text_sha256) DO NOTHING RETURNING id;

-- name: FindPageSnapshot :one
SELECT id FROM catalog.page_snapshot WHERE page_id = $1 AND extractor_version = $2 AND text_sha256 = $3;

-- name: PageSnapshotText :one
SELECT text FROM catalog.page_snapshot WHERE id = $1;

-- name: ObservePage :exec
INSERT INTO catalog.page_observation (page_id, snapshot_id) VALUES ($1, $2);

-- name: RecordPageSuccess :exec
UPDATE catalog.watched_page SET current_snapshot_id = $2, last_ok_at = clock_timestamp(), failures = 0,
    lease_until = clock_timestamp(), next_check_at = clock_timestamp() + sqlc.arg(seconds)::bigint * interval '1 second' WHERE id = $1;

-- name: RecordPageFailure :exec
UPDATE catalog.watched_page SET failures = failures + 1, lease_until = clock_timestamp(),
    next_check_at = clock_timestamp() + sqlc.arg(seconds)::bigint * interval '1 second' WHERE id = $1;

-- name: PageWasDismissed :one
SELECT EXISTS(SELECT FROM catalog.review WHERE page_id = $1 AND snapshot_id = $2 AND status = 'dismissed');

-- name: UpsertPageReview :one
INSERT INTO catalog.review (page_id, snapshot_id, previous_snapshot_id, removed, added) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (page_id) WHERE status = 'open' DO UPDATE SET snapshot_id = excluded.snapshot_id,
removed = excluded.removed, added = excluded.added,
content_seq = catalog.review.content_seq + 1 RETURNING id;

-- name: ResolvePageReview :one
UPDATE catalog.review SET status = $3, resolution = $4 WHERE id = $1 AND content_seq = $2 AND status = 'open' RETURNING page_id;

-- name: OverduePages :many
SELECT id FROM catalog.watched_page WHERE enabled AND
    coalesce(last_ok_at, created_at) < clock_timestamp() - interval_seconds * interval '2 seconds' ORDER BY id FOR UPDATE;

-- name: OfficialPriceEvidence :one
SELECT evidence, kind
FROM catalog.change WHERE after ->> 'price_id' = sqlc.arg(price_id)::text
OR after -> sqlc.arg(currency)::text ->> 'price_id' = sqlc.arg(price_id)::text LIMIT 1;

-- name: PendingPageReviews :one
SELECT count(*) FROM catalog.review WHERE page_id = $1 AND status = 'open';

-- name: CloseSupersededPageReview :exec
UPDATE catalog.review SET status = 'done', resolution = 'current content was previously dismissed'
WHERE page_id = $1 AND status = 'open';
