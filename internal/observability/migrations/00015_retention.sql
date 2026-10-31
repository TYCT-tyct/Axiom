-- +goose Up

-- The hot window: a monthly partition whose whole month is older than this is copied to the object store and, once that
-- copy is confirmed by its manifest, dropped from PostgreSQL. It is admin data, like the alert rules, so changing it
-- needs no deploy; 0 keeps every month, and the code default applies until the row is saved. A missing bucket also keeps
-- every month: dropping without a confirmed archive would lose the records.
CREATE TABLE observability.retention (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    body jsonb NOT NULL CHECK (jsonb_typeof(body) = 'object' AND octet_length(body::text) <= 1024)
);
ALTER TABLE observability.retention ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.retention USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE ON observability.retention TO axiom_app;

-- The retention job creates next month's partitions and drops archived ones, and PostgreSQL requires owning a table to
-- create or drop a partition of it. The job role therefore owns the two log tables and their partitions; the
-- application keeps the SELECT and INSERT it was granted, and owns nothing extra. The bootstrap creates the role and
-- grants it to axiom_owner (inherited, with admin), so later migrations still alter these tables. The job's own login
-- role is created by the deployment; the application is not a member of the job role, which is what keeps SQL run as
-- the application from owning or dropping tenant tables.
GRANT USAGE, CREATE ON SCHEMA observability TO axiom_retention_job;
GRANT SELECT ON observability.retention TO axiom_retention_job;
-- The deployment's roles.sh creates the login role before migrations run. A database restored from a set older than this
-- role still migrates: the grant is skipped, retention stays off until the role and AXIOM_RETENTION_DATABASE_URL exist.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_retention') THEN
        EXECUTE 'GRANT axiom_retention_job TO axiom_retention WITH INHERIT TRUE, SET TRUE';
    END IF;
END
$$;
-- +goose StatementEnd
-- The policies of the partitions run the scope functions, which live in platform.
GRANT USAGE ON SCHEMA platform TO axiom_retention_job;

-- +goose StatementBegin
DO $$
DECLARE
    target regclass;
BEGIN
    FOR target IN
        SELECT relid FROM pg_partition_tree('observability.call_log')
        UNION ALL SELECT relid FROM pg_partition_tree('observability.call_attempt')
    LOOP
        EXECUTE format('ALTER TABLE %s OWNER TO axiom_retention_job', target);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- ledger_entry_ids stays for one more release (#204): this release stopped writing and reading it, and dropping it in
-- the same release would break an old node writing during a rolling deploy. No migration here touches it.
