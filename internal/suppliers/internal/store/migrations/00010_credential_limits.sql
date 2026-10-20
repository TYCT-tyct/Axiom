-- +goose Up

-- A per-minute ceiling beside the concurrency a credential already has (internal/suppliers/README.md「并发与速率」).
-- NULL is no ceiling of Axiom's, so the keys bound before this migration keep the supplier's own limit as their only
-- one instead of being throttled by a number nobody chose. Zero is refused: a minute with no request in it would be a
-- disabled credential, which the status column already says.
ALTER TABLE suppliers.credentials ADD COLUMN requests_per_minute integer CHECK (requests_per_minute > 0);

-- The admin sets it after binding a key, on this column only: the key's text, status and identity stay unwritable.
GRANT UPDATE (requests_per_minute) ON suppliers.credentials TO axiom_app;
