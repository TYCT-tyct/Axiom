-- +goose Up

-- A call, its attempts and its ledger transactions share one key: the request ID (call_log.id = call_attempt.request_id
-- = billing.transactions.request_id). This column was a second, broken link. billing.entries has no ID of its own, so no
-- value here could name an entry, and nothing ever wrote one. The ledger is read by the request ID instead, through
-- billing's LedgerOf (internal/observability/README.md「追溯」): that link cannot be lost with an asynchronous record
-- and stays current when a correction posts another transaction.
ALTER TABLE observability.call_log DROP COLUMN ledger_entry_ids;
