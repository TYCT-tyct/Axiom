-- +goose Up

-- A key version retired on another node must not be written here, even by a transaction that read the old write
-- version before the retirement committed. platform.fence_envelope_key_version is the shared fence; this attaches it
-- to this module's envelope (details in platform/pg/migrations/00020_envelope_key_fence.sql).
CREATE OR REPLACE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF totp_key_version ON accounts.users
FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('totp_key_version');

-- +goose Down
DROP TRIGGER IF EXISTS fence_envelope_key_version ON accounts.users;
