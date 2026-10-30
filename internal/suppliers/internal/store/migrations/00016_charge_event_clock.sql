-- +goose Up
-- The event cursor is ordered by created_at. now() is the transaction's start, so a producer that began first and
-- committed last could insert an event older than a cursor already advanced past it. Producers take the shared event
-- lock (LockChargeEvents) before inserting, and the insertion clock is read after that lock, not at BEGIN.
ALTER TABLE suppliers.charge_events ALTER COLUMN created_at SET DEFAULT clock_timestamp();
