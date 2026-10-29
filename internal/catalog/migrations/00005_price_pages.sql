-- +goose Up
CREATE TABLE catalog.page_site (
    origin text PRIMARY KEY CHECK (origin ~ '^https://'),
    delay_seconds bigint NOT NULL CHECK (delay_seconds BETWEEN 1 AND 86400),
    last_request_at timestamptz,
    next_request_at timestamptz NOT NULL DEFAULT '-infinity'
);
CREATE TABLE catalog.watched_page (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    author text NOT NULL REFERENCES catalog.author(slug),
    url text NOT NULL CHECK (url ~ '^https://'),
    origin text NOT NULL REFERENCES catalog.page_site(origin),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    section_start text NOT NULL CHECK (section_start <> ''),
    section_end text NOT NULL CHECK (section_end <> ''),
    interval_seconds bigint NOT NULL CHECK (interval_seconds BETWEEN 3600 AND 604800),
    enabled bool NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    next_check_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_gen bigint NOT NULL DEFAULT 0,
    lease_until timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_ok_at timestamptz,
    failures bigint NOT NULL DEFAULT 0 CHECK (failures >= 0),
    current_snapshot_id uuid
);
CREATE TABLE catalog.page_snapshot (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    page_id uuid NOT NULL REFERENCES catalog.watched_page(id),
    first_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    extractor_version integer NOT NULL,
    text text NOT NULL CHECK (text <> ''),
    text_sha256 bytea NOT NULL CHECK (octet_length(text_sha256) = 32),
    UNIQUE (page_id, extractor_version, text_sha256),
    UNIQUE (page_id, id)
);
ALTER TABLE catalog.watched_page ADD FOREIGN KEY (id, current_snapshot_id)
    REFERENCES catalog.page_snapshot(page_id, id);
CREATE TABLE catalog.page_observation (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    page_id uuid NOT NULL,
    observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    snapshot_id uuid NOT NULL,
    FOREIGN KEY (page_id, snapshot_id) REFERENCES catalog.page_snapshot(page_id, id)
);
CREATE TABLE catalog.review (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    page_id uuid NOT NULL REFERENCES catalog.watched_page(id),
    kind text NOT NULL DEFAULT 'page_changed' CHECK (kind = 'page_changed'),
    snapshot_id uuid NOT NULL,
    previous_snapshot_id uuid,
    removed text NOT NULL,
    added text NOT NULL,
    content_seq bigint NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'dismissed')),
    resolution text,
    FOREIGN KEY (page_id, snapshot_id) REFERENCES catalog.page_snapshot(page_id, id),
    FOREIGN KEY (page_id, previous_snapshot_id) REFERENCES catalog.page_snapshot(page_id, id)
);
CREATE UNIQUE INDEX review_open_page ON catalog.review(page_id) WHERE status = 'open';
CREATE INDEX review_dismissed_page ON catalog.review(page_id, snapshot_id) WHERE status = 'dismissed';
CREATE UNIQUE INDEX watched_page_active_url ON catalog.watched_page(url) WHERE enabled;
CREATE INDEX watched_page_due ON catalog.watched_page(next_check_at) WHERE enabled;

ALTER TABLE catalog.page_site ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE catalog.watched_page ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE catalog.page_snapshot ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE catalog.page_observation ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE catalog.review ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_read ON catalog.page_site FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_insert ON catalog.page_site FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_update ON catalog.page_site FOR UPDATE USING ((SELECT platform.in_platform_scope())) WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_read ON catalog.watched_page FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_insert ON catalog.watched_page FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_update ON catalog.watched_page FOR UPDATE USING ((SELECT platform.in_platform_scope())) WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_read ON catalog.page_snapshot FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_insert ON catalog.page_snapshot FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_read ON catalog.page_observation FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_insert ON catalog.page_observation FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_read ON catalog.review FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_insert ON catalog.review FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform_update ON catalog.review FOR UPDATE USING ((SELECT platform.in_platform_scope())) WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON catalog.page_site, catalog.watched_page, catalog.page_snapshot, catalog.page_observation, catalog.review TO axiom_app;
GRANT UPDATE (delay_seconds, last_request_at, next_request_at) ON catalog.page_site TO axiom_app;
GRANT UPDATE (enabled, lease_gen, lease_until, next_check_at, last_ok_at, failures, current_snapshot_id) ON catalog.watched_page TO axiom_app;
GRANT UPDATE (snapshot_id, previous_snapshot_id, removed, added, content_seq, status, resolution) ON catalog.review TO axiom_app;
