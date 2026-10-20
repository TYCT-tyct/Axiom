-- +goose Up

-- Key material stays in protected files. Retired rows are tombstones: a version must never identify another key.
CREATE TABLE platform.key_versions (
    version bigint PRIMARY KEY CHECK (version BETWEEN 1 AND 4294967295),
    fingerprint bytea NOT NULL UNIQUE CHECK (octet_length(fingerprint) = 32),
    state text NOT NULL CHECK (state IN ('readable', 'active', 'retired'))
);
CREATE UNIQUE INDEX key_versions_one_active ON platform.key_versions (state) WHERE state = 'active';
ALTER TABLE platform.key_versions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON platform.key_versions USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE (state) ON platform.key_versions TO axiom_app;
