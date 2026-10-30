-- +goose Up

-- A retired version must never be written again, even by a transaction that read it before the retirement committed.
-- The tombstone and this marker commit together under an exclusive transaction lock; every envelope write takes the
-- same lock shared and checks the marker, holding it until its own commit. The write either commits before the
-- retirement, so the retirement's reference check sees it and refuses, or waits here and is refused after the tombstone.
-- The table carries version numbers only and stays outside the tenant/row-security scopes of the envelope tables.
CREATE TABLE IF NOT EXISTS platform.key_retirements (
    version bigint PRIMARY KEY CHECK (version BETWEEN 1 AND 4294967295)
);
GRANT SELECT, INSERT ON platform.key_retirements TO axiom_app;

-- A retirement committed before this fence has no marker; backfill it so the first prune cannot delete a key that a
-- stale writer could still use.
INSERT INTO platform.key_retirements (version) SELECT version FROM platform.key_versions WHERE state = 'retired'
ON CONFLICT DO NOTHING;

-- The table holds retired version numbers, which the heartbeat and audit already expose, so every scope may read and
-- record one; without a policy the trigger would see no row under a tenant scope. FORCE keeps the house rule that no
-- table in this schema is exempt from row security.
ALTER TABLE platform.key_retirements ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS fence ON platform.key_retirements;
CREATE POLICY fence ON platform.key_retirements USING (true) WITH CHECK (true);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION platform.fence_envelope_key_version() RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    key_version bigint := (to_jsonb(NEW) ->> TG_ARGV[0])::bigint;
BEGIN
    IF key_version IS NULL THEN
        RETURN NEW;
    END IF;
    -- Held to the end of this transaction. A retirement holds it exclusively until its tombstone and marker commit, so
    -- this write either commits first (and the retirement's reference check sees it) or waits here and is refused.
    PERFORM pg_advisory_xact_lock_shared(321, 9);
    IF EXISTS (SELECT 1 FROM platform.key_retirements r WHERE r.version = key_version) THEN
        RAISE EXCEPTION 'key version % is retired', key_version USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd

-- The platform's own envelope: the SMTP password. The module migrations attach the same fence to their tables.
CREATE OR REPLACE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.smtp_config
FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');
