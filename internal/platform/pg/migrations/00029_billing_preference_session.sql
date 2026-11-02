-- +goose Up
-- The funding-preference declaration belongs to one process, not one node ID: an old binary can restart under the
-- same ID, update share_session and leave the boolean behind. Matching the declared session to the live one makes that
-- stale true unsupported.
ALTER TABLE platform.nodes ADD COLUMN IF NOT EXISTS billing_preference_session uuid;
GRANT UPDATE (billing_preference_session) ON platform.nodes TO axiom_app;

-- +goose Down
ALTER TABLE platform.nodes DROP COLUMN IF EXISTS billing_preference_session;
