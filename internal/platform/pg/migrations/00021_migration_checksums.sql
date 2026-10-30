-- +goose Up

-- What each applied migration's file was: the version tables say which migrations ran, this says what they contained,
-- so a merged migration that changes later stops `axiom migrate` and every node's start instead of leaving a schema its
-- recorded history no longer explains (internal/platform/README.md「迁移」). A table of its own rather than a column of
-- goose's version tables: goose creates a module's version table when that module first has a migration, after this one.
-- Repeatable like the later platform migrations, for the run that finds the objects of an interrupted one.
CREATE TABLE IF NOT EXISTS platform.migration_checksums (
    module text NOT NULL,
    version_id bigint NOT NULL CHECK (version_id > 0),
    filename text NOT NULL,
    checksum text NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (module, version_id)
);

-- The record names no tenant and every node reads all of it in its startup check, which runs without a scope; only the
-- migration administrator writes, in the platform scope migrations run in. Forced like every table, so a node of an
-- older release, whose audit has no exception for it, starts against this schema too.
ALTER TABLE platform.migration_checksums ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON platform.migration_checksums;
CREATE POLICY reads ON platform.migration_checksums FOR SELECT USING (true);
DROP POLICY IF EXISTS records ON platform.migration_checksums;
CREATE POLICY records ON platform.migration_checksums FOR INSERT WITH CHECK (platform.in_platform_scope());
GRANT SELECT ON platform.migration_checksums TO axiom_app;
