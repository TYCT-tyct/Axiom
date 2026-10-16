-- +goose Up

-- The list of an account's calls filtered by model or by client application (internal/observability/README.md「读」).
-- The account index (account_id, at DESC, id DESC) cannot serve a filter on a column it does not hold: a sparse match
-- or none made the planner read the whole month's partition (3,530 buffers at 120,000 rows, growing with the table).
-- Each filter gets its own index with the filter column after the account, so a page is read in order from the
-- filter's own range and the reads are bounded by the matches, not by the account's volume.
--
-- Compatible with the program before it: a new index is only used by the query, and the older program's query is
-- correct without it. Run migrate before the new program starts, as always.
--
-- Locking: CREATE INDEX on a partitioned table builds one index per partition and takes a SHARE lock on each for its
-- build, which blocks the call-record writer's inserts into that partition for as long as the build takes. CONCURRENTLY
-- is not available on a partitioned parent. This migration is only fit for a table that is still small, as call_log is
-- when it is first deployed (a build of 360,000 rows takes about a second); the README (「索引」) gives the way to add
-- an index to a populated call log: CREATE INDEX ... ON ONLY the parent, then CREATE INDEX CONCURRENTLY on each
-- partition and ATTACH it, in a migration marked NO TRANSACTION.
CREATE INDEX call_log_account_model_at ON observability.call_log (account_id, model, at DESC, id DESC);
CREATE INDEX call_log_account_app_at ON observability.call_log (account_id, client_app, at DESC, id DESC);
