-- +goose Up

-- The hash chain over the audit log (docs/domains/operations.md「审计」): one link per committed audit entry, in the
-- entries' (at, id) order, each hash covering the previous one and the entry's fields, so an entry changed, removed or
-- slipped in behind the head after it was chained no longer matches. A job links the entries after they commit, never
-- inside the transaction that writes them (internal/platform/README.md「审计日志」). No foreign key to the audit log:
-- a link must outlive an entry that was deleted, which is what the verification then reports.
CREATE TABLE operations.audit_chain (
    seq bigint PRIMARY KEY CHECK (seq >= 1),
    entry_id uuid NOT NULL UNIQUE,
    at timestamptz NOT NULL,
    prev_hash bytea NOT NULL CHECK (octet_length(prev_hash) = 32),
    hash bytea NOT NULL CHECK (octet_length(hash) = 32),
    chained_at timestamptz NOT NULL DEFAULT now()
);

-- Every link follows the one before it: the first has 32 zero bytes before it, and each later one the hash of seq - 1.
-- With seq the primary key the chain can neither fork nor skip, whichever version of the job appends.
-- +goose StatementBegin
CREATE FUNCTION operations.audit_chain_follows() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF NOT (
        (NEW.seq = 1 AND NEW.prev_hash = '\x0000000000000000000000000000000000000000000000000000000000000000'::bytea)
        OR EXISTS (SELECT 1 FROM operations.audit_chain c WHERE c.seq = NEW.seq - 1 AND c.hash = NEW.prev_hash)
    ) THEN
        RAISE EXCEPTION 'link % does not follow the chain', NEW.seq USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_chain_follows BEFORE INSERT ON operations.audit_chain
    FOR EACH ROW EXECUTE FUNCTION operations.audit_chain_follows();

-- What the chaining found wrong: an entry behind the head that was never chained (late), a link whose entry is gone
-- (missing) or no longer hashes to it (mismatch), or a link that does not follow the one before it (broken). While a
-- row is here the chain is not extended; the application can neither change nor remove one.
CREATE TABLE operations.audit_chain_faults (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    found_at timestamptz NOT NULL DEFAULT now(),
    kind text NOT NULL CHECK (kind IN ('late', 'missing', 'mismatch', 'broken')),
    seq bigint NOT NULL,
    entry_id uuid NOT NULL
);

ALTER TABLE operations.audit_chain ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.audit_chain FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.audit_chain FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT (seq, entry_id, at, prev_hash, hash) ON operations.audit_chain TO axiom_app;

ALTER TABLE operations.audit_chain_faults ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.audit_chain_faults FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.audit_chain_faults FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT (kind, seq, entry_id) ON operations.audit_chain_faults TO axiom_app;

-- +goose Down
DROP TABLE operations.audit_chain_faults;
DROP TABLE operations.audit_chain;
DROP FUNCTION operations.audit_chain_follows();
