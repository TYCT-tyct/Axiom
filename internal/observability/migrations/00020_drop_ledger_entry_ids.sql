-- +goose Up

-- ledger_entry_ids never had a writer: billing.entries has no ID of its own, and a call is joined to its ledger
-- transactions by the request ID, which is the ledger's own key (README.md「追溯」). The code stopped reading and
-- writing it in #201. The platform's shrink rule waits one release so an old node can finish writing during a rolling
-- deploy, but Axiom has never been deployed to production: there is no node of any version that could still write this
-- column. Drop it now instead of leaving a column that claims a link that never existed (#204). PostgreSQL applies a
-- column drop on a partitioned table to every partition; TestDropLedgerEntryIDsReachesPartitions checks that on a real
-- server.
ALTER TABLE observability.call_log DROP COLUMN ledger_entry_ids;

-- +goose Down

ALTER TABLE observability.call_log ADD COLUMN ledger_entry_ids uuid[] NOT NULL DEFAULT '{}'
    CHECK (cardinality(ledger_entry_ids) <= 16);
