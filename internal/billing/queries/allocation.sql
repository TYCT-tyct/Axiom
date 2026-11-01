-- name: AllocationState :one
-- Where the allocation of an account stands: the key of the last transaction replayed, and the debt.
SELECT allocated_seq, allocated_at, allocated_id, unfunded FROM billing.balances WHERE account_id = @account_id;

-- name: ReplayThrough :one
-- Fix the unlocked replay's upper bound before reading batches. Every transaction of an account takes its seq under that
-- account's balance row lock (PostLedger), in the order the lock is taken and released at commit, so by the
-- time this reads B every transaction of the account with a seq at or below B has committed, and the batches read after
-- it see theirs. A credit committed later gets a higher seq, and so stays out of this replay: the next one loads it
-- along with its batch (#361). Rows from before the sequence cannot arrive later, so they need no bound of their own.
-- The account's maximum is served by transactions_by_account_seq, whose NULL seqs sort after the numbered ones: an
-- account that has rows from before the release pays one pass over them here, frozen and never longer.
SELECT coalesce(max(seq), 0)::bigint FROM billing.transactions WHERE account_id = @account_id;

-- name: LiveBatches :many
-- The batches that hold something or were consumed, each with the key of the credit transaction that opened it (seq 0
-- for one from before the sequence). A fully spent batch stays here because a correction can give its credit back
-- (#277); consumption, expiry and freeze totals are the replay cache, and trail the ledger until the account is
-- replayed. Served by credit_batches_by_account.
SELECT b.id, b.site_id, b.source, b.expires_at, b.amount, b.consumed, b.expired, b.freeze_total, b.unfreeze_total, b.refunded, b.charged_back,
    coalesce(t.seq, 0)::bigint AS opened_seq, t.created_at AS opened_at, t.id AS opened_id
FROM billing.credit_batches b
JOIN billing.transactions t ON t.request_id = b.id AND t.attempt_seq = 0 AND t.kind = 'credit' AND t.revision = 0
WHERE b.account_id = @account_id
    AND (b.consumed > 0 OR b.charged_back > 0
        OR (b.consumed + b.expired < b.amount AND b.refunded < b.amount - b.consumed - b.expired));

-- name: LegacyMovements :many
-- A page of the account's transactions from before the sequence (seq NULL), in the order of their time and ID, after the
-- place given, each with its net debit on the user's balance (positive takes from the user, 0 when it did not touch it)
-- and seq 0. They come first in the replay. Bounded first, by the index transactions_legacy_by_account, and only the
-- page has its entries summed, so what a page reads grows with the page and not with the history behind it. A
-- transaction that moved nothing still takes its place in the page, so the cursor passes it.
WITH page AS (
    SELECT t.id, t.created_at, t.kind, t.request_id, t.revision
    FROM billing.transactions t
    WHERE t.account_id = @account_id AND t.seq IS NULL AND (t.created_at, t.id) > (@after_at::timestamptz, @after_id::uuid)
    ORDER BY t.created_at, t.id
    LIMIT @max
)
SELECT 0::bigint AS seq, p.created_at, p.id, p.kind, p.request_id, p.revision,
    coalesce((SELECT sum(e.amount) FROM billing.entries e WHERE e.transaction_id = p.id AND e.book = 'user_balance'), 0)::bigint AS debit
FROM page p
ORDER BY p.created_at, p.id;

-- name: Movements :many
-- A page of the account's numbered transactions after the seq given, in the order of seq, each with its net debit on
-- the user's balance as for LegacyMovements, never past through_seq. Bounded by the index transactions_by_account_seq.
WITH page AS (
    SELECT t.id, t.created_at, t.seq, t.kind, t.request_id, t.revision
    FROM billing.transactions t
    WHERE t.account_id = @account_id AND t.seq > @after_seq::bigint AND t.seq <= @through_seq::bigint
    ORDER BY t.seq
    LIMIT @max
)
SELECT p.seq::bigint AS seq, p.created_at, p.id, p.kind, p.request_id, p.revision,
    coalesce((SELECT sum(e.amount) FROM billing.entries e WHERE e.transaction_id = p.id AND e.book = 'user_balance'), 0)::bigint AS debit
FROM page p
ORDER BY p.seq;

-- name: ChargebackBatch :one
-- A chargeback can name a batch that expired before it arrived. LiveBatches leaves a fully expired batch out to keep
-- every replay small, so the chargeback loads just that batch before it posts; after the first chargeback the row's
-- charged_back keeps it in LiveBatches for the reversal.
SELECT b.id, b.site_id, b.source, b.expires_at, b.amount, b.consumed, b.expired, b.freeze_total, b.unfreeze_total, b.refunded, b.charged_back,
    coalesce(t.seq, 0)::bigint AS opened_seq, t.created_at AS opened_at, t.id AS opened_id
FROM billing.credit_batches b
JOIN billing.transactions t ON t.request_id = b.id AND t.attempt_seq = 0 AND t.kind = 'credit' AND t.revision = 0
WHERE b.id = @id AND b.account_id = @account_id;

-- name: SaveBatches :exec
UPDATE billing.credit_batches b SET consumed = v.consumed, expired = v.expired,
    freeze_total = v.freeze_total, unfreeze_total = v.unfreeze_total, refunded = v.refunded,
    charged_back = v.charged_back
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@consumed::bigint[]) AS consumed, unnest(@expired::bigint[]) AS expired,
    unnest(@freeze_total::bigint[]) AS freeze_total, unnest(@unfreeze_total::bigint[]) AS unfreeze_total,
    unnest(@refunded::bigint[]) AS refunded, unnest(@charged_back::bigint[]) AS charged_back) v
WHERE b.id = v.id;

-- name: SaveAllocation :execrows
-- Moves the allocation of an account from the key it was read at to the key it was replayed to. 0 rows: someone else
-- moved it first, so what was replayed here is stale and must be dropped.
UPDATE billing.balances SET allocated_seq = @allocated_seq, allocated_at = @allocated_at, allocated_id = @allocated_id,
    unfunded = @unfunded
WHERE account_id = @account_id AND allocated_seq = @was_seq AND allocated_at = @was_at AND allocated_id = @was_id;

-- name: DueAccounts :many
-- The accounts with a batch whose time has come at now and which still holds something, after the account given, in
-- order. now is the one time the run takes, so the range on expires_at is a bound of the index credit_batches_due.
-- Frozen credit holds something too: it is written off once the freeze resolves, and until then the run reports it as
-- deferred (docs/domains/billing.md「额度批次」), so a batch whose whole remainder is frozen is due as well.
SELECT b.account_id FROM billing.credit_batches b
WHERE b.expires_at <= @now::timestamptz AND b.consumed + b.expired < b.amount AND b.account_id > @after
GROUP BY b.account_id
ORDER BY b.account_id
LIMIT @max;

-- name: ExpiryCursor :one
SELECT after FROM billing.expiry_cursor;

-- name: SaveExpiryCursor :exec
INSERT INTO billing.expiry_cursor (one, after) VALUES (true, @after)
ON CONFLICT (one) DO UPDATE SET after = EXCLUDED.after;
