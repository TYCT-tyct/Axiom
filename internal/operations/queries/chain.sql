-- name: LockAuditChain :exec
-- One chaining at a time: a run that overlaps another (a rescued job, a second node) waits for it and then extends the
-- head it left.
SELECT pg_advisory_xact_lock(hashtextextended('operations.audit_chain', 0));

-- name: AuditChainHalted :one
SELECT EXISTS (SELECT 1 FROM operations.audit_chain_faults) AS halted;

-- name: AuditChainHead :many
SELECT seq, entry_id, at, hash FROM operations.audit_chain ORDER BY seq DESC LIMIT 1;

-- name: AuditChainLinks :many
-- The links from seq on, oldest first.
SELECT seq, entry_id, at, prev_hash, hash FROM operations.audit_chain WHERE seq >= @seq ORDER BY seq LIMIT @n;

-- name: AuditChainFrontier :one
-- Before what moment every audit entry has committed, so that one chained later cannot come out ahead of it. An entry's
-- at is its transaction's start, which pg_stat_activity shows as xact_start until it ends: an entry older than every
-- open transaction of this database cannot still be on its way. The application's sessions are all one role, and
-- PostgreSQL shows a role its own sessions' xact_start; an hour-long read holds the frontier back an hour, which the lag
-- alert reports, rather than chaining an entry ahead of one still to commit. settle more seconds cover a session that
-- has taken its start time and not yet published it, or one of another role. Read in a statement of its own, before
-- the entries: a transaction that commits between the two is then in the entries' snapshot.
SELECT now()::timestamptz AS now, least(now() - make_interval(secs => @settle::float8), (
    SELECT min(xact_start) FROM pg_catalog.pg_stat_activity
    WHERE datname = current_database() AND backend_type = 'client backend' AND pid <> pg_backend_pid()
))::timestamptz AS frontier;

-- name: AppendAuditChain :exec
INSERT INTO operations.audit_chain (seq, entry_id, at, prev_hash, hash) VALUES (@seq, @entry_id, @at, @prev_hash, @hash);

-- name: InsertAuditChainFault :exec
INSERT INTO operations.audit_chain_faults (kind, seq, entry_id) VALUES (@kind, @seq, @entry_id);
