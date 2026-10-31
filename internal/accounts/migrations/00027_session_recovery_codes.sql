-- +goose Up
-- A provider link answers with a redirect, so the recovery codes it makes cannot come back in that response. They are
-- encrypted under a key derived from the session token the browser holds and kept here until the console takes them
-- once (#530 finding 1). No key-ring key is referenced: rotation has nothing to rewrap, retirement cannot see them, and
-- an older node's keyring check finds no envelope it does not know.
ALTER TABLE accounts.sessions
    ADD COLUMN recovery_codes_ciphertext bytea,
    ADD COLUMN recovery_codes_nonce bytea,
    ADD CONSTRAINT sessions_recovery_codes_together
        CHECK (num_nulls(recovery_codes_ciphertext, recovery_codes_nonce) IN (0, 2));
GRANT UPDATE (recovery_codes_ciphertext, recovery_codes_nonce) ON accounts.sessions TO axiom_app;

-- A revoked session must not keep its codes: revoking ends the token they are encrypted under, and the row must not
-- keep the ciphertext either. The trigger runs for every writer, including an older node that does not know these
-- columns, because the rule lives in the database.
-- +goose StatementBegin
CREATE FUNCTION accounts.clear_session_recovery_codes() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    NEW.recovery_codes_ciphertext := NULL;
    NEW.recovery_codes_nonce := NULL;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER clear_session_recovery_codes BEFORE UPDATE OF revoked_at ON accounts.sessions
FOR EACH ROW WHEN (NEW.revoked_at IS NOT NULL) EXECUTE FUNCTION accounts.clear_session_recovery_codes();

-- +goose Down
DROP TRIGGER IF EXISTS clear_session_recovery_codes ON accounts.sessions;
DROP FUNCTION IF EXISTS accounts.clear_session_recovery_codes();
ALTER TABLE accounts.sessions
    DROP CONSTRAINT sessions_recovery_codes_together,
    DROP COLUMN recovery_codes_ciphertext,
    DROP COLUMN recovery_codes_nonce;
