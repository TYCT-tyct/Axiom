-- +goose Up

-- How far the paged readings of a supplier got (internal/suppliers/README.md「同步」「持久化」): the offer and the keys
-- listing are read over several rounds when the supplier's limits stop them, and the next round goes on from the page
-- the one before reached. It is a member of the engine's state that a node of the version before refuses
-- (jsonx.UnmarshalKnown), so it has a column of its own: that node loads and writes the state beside it, and the two run
-- side by side during a rollout.
ALTER TABLE suppliers.sync_states ADD COLUMN listing jsonb CHECK (listing IS NULL OR jsonb_typeof(listing) = 'object');

GRANT UPDATE (listing) ON suppliers.sync_states TO axiom_app;
