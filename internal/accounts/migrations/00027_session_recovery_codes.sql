-- +goose Up
-- A provider link answers with a redirect, so the recovery codes it makes cannot come back in that response. They are
-- sealed on the session the link issues, and the console reads and clears them once (#530 finding 1). An older node's
-- INSERT omits the columns, which leaves them null.
ALTER TABLE accounts.sessions
    ADD COLUMN recovery_codes_ciphertext bytea,
    ADD COLUMN recovery_codes_nonce bytea,
    ADD COLUMN recovery_codes_wrapped_dek bytea,
    ADD COLUMN recovery_codes_key_version bigint CHECK (recovery_codes_key_version BETWEEN 1 AND 4294967295),
    ADD CONSTRAINT sessions_recovery_codes_together
        CHECK (num_nulls(recovery_codes_ciphertext, recovery_codes_nonce, recovery_codes_wrapped_dek,
            recovery_codes_key_version) IN (0, 4));
GRANT UPDATE (recovery_codes_ciphertext, recovery_codes_nonce, recovery_codes_wrapped_dek, recovery_codes_key_version)
    ON accounts.sessions TO axiom_app;
-- The fourth envelope, so a version retired on another node is refused here too and the keyring check sees the store.
CREATE OR REPLACE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF recovery_codes_key_version
    ON accounts.sessions FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('recovery_codes_key_version');

-- +goose Down
DROP TRIGGER IF EXISTS fence_envelope_key_version ON accounts.sessions;
ALTER TABLE accounts.sessions
    DROP COLUMN recovery_codes_ciphertext,
    DROP COLUMN recovery_codes_nonce,
    DROP COLUMN recovery_codes_wrapped_dek,
    DROP COLUMN recovery_codes_key_version;
