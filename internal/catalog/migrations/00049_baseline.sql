-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;

-- +goose StatementBegin
CREATE FUNCTION catalog.refuse_page_extractor_downgrade() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE
    old_version integer;
    new_version integer;
BEGIN
    IF NEW.current_snapshot_id IS NOT DISTINCT FROM OLD.current_snapshot_id THEN
        RETURN NEW;
    END IF;
    IF OLD.current_snapshot_id IS NOT NULL THEN
        SELECT extractor_version INTO old_version
        FROM catalog.page_snapshot WHERE id = OLD.current_snapshot_id;
    END IF;
    SELECT extractor_version INTO new_version
    FROM catalog.page_snapshot WHERE id = NEW.current_snapshot_id;
    IF old_version IS NOT NULL AND new_version < old_version THEN
        RAISE EXCEPTION 'page snapshot extractor version cannot go backwards: % -> %', old_version, new_version
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;


ALTER FUNCTION catalog.refuse_page_extractor_downgrade() OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE catalog.alias (
    name text NOT NULL,
    rev bigint NOT NULL,
    model_id uuid NOT NULL,
    set_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT alias_name_check CHECK (((octet_length(name) >= 1) AND (octet_length(name) <= 200))),
    CONSTRAINT alias_name_check1 CHECK ((name ~ '^~[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}-latest$'::text)),
    CONSTRAINT alias_rev_check CHECK ((rev > 0))
);



ALTER TABLE catalog.alias OWNER TO axiom_owner;

CREATE TABLE catalog.author (
    slug text NOT NULL,
    name text NOT NULL,
    CONSTRAINT author_name_check CHECK (((name <> ''::text) AND (char_length(name) <= 100))),
    CONSTRAINT author_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,39}$'::text))
);



ALTER TABLE catalog.author OWNER TO axiom_owner;

CREATE TABLE catalog.change (
    id uuid DEFAULT uuidv7() NOT NULL,
    op_key uuid NOT NULL,
    at timestamptz DEFAULT now() NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    kind text NOT NULL,
    subject text NOT NULL,
    before jsonb,
    after jsonb,
    evidence jsonb,
    CONSTRAINT change_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT change_kind_check CHECK ((kind ~ '^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$'::text)),
    CONSTRAINT change_op_key_check CHECK ((op_key <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT change_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 1000))),
    CONSTRAINT change_subject_check CHECK ((subject <> ''::text))
);



ALTER TABLE catalog.change OWNER TO axiom_owner;

CREATE TABLE catalog.model (
    id uuid DEFAULT uuidv7() NOT NULL,
    author text NOT NULL,
    slug text NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    version_basis text NOT NULL,
    price_source text NOT NULL,
    spec jsonb DEFAULT '{}'::jsonb NOT NULL,
    rev bigint DEFAULT 0 NOT NULL,
    CONSTRAINT model_price_source_check CHECK ((price_source = ANY (ARRAY['litellm'::text, 'openrouter'::text, 'official'::text, 'manual'::text]))),
    CONSTRAINT model_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}(:free)?(@[a-z0-9][a-z0-9._-]{0,39})?$'::text)),
    CONSTRAINT model_spec_check CHECK ((jsonb_typeof(spec) = 'object'::text)),
    CONSTRAINT model_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'listed'::text, 'retired'::text]))),
    CONSTRAINT model_version_basis_check CHECK ((version_basis = ANY (ARRAY['dated'::text, 'floating'::text]))),
    CONSTRAINT slug_author CHECK ((split_part(slug, '/'::text, 1) = author))
);



ALTER TABLE catalog.model OWNER TO axiom_owner;

CREATE TABLE catalog.name (
    name text NOT NULL,
    norm text NOT NULL,
    visibility text NOT NULL,
    model_id uuid NOT NULL,
    CONSTRAINT name_name_check CHECK (((octet_length(name) >= 1) AND (octet_length(name) <= 200))),
    CONSTRAINT name_norm_check CHECK (((norm <> ''::text) AND (norm !~ '~'::text) AND ((POSITION((':'::text) IN (norm)) = 0) OR (norm ~ '^[^:]+:free$'::text)))),
    CONSTRAINT name_visibility_check CHECK ((visibility = ANY (ARRAY['public'::text, 'internal'::text]))),
    CONSTRAINT public_name CHECK (((visibility <> 'public'::text) OR ((norm !~ '-latest$'::text) AND (norm !~ '@'::text) AND ((POSITION((':'::text) IN (norm)) = 0) OR (norm ~ '^[^:]+:free$'::text)))))
);



ALTER TABLE catalog.name OWNER TO axiom_owner;

CREATE TABLE catalog.page_observation (
    id uuid DEFAULT uuidv7() NOT NULL,
    page_id uuid NOT NULL,
    observed_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    snapshot_id uuid NOT NULL
);



ALTER TABLE catalog.page_observation OWNER TO axiom_owner;

CREATE TABLE catalog.page_site (
    origin text NOT NULL,
    delay_seconds bigint NOT NULL,
    last_request_at timestamptz,
    next_request_at timestamptz DEFAULT '-infinity'::timestamptz NOT NULL,
    CONSTRAINT page_site_delay_seconds_check CHECK (((delay_seconds >= 1) AND (delay_seconds <= 86400))),
    CONSTRAINT page_site_origin_check CHECK ((origin ~ '^https://'::text))
);



ALTER TABLE catalog.page_site OWNER TO axiom_owner;

CREATE TABLE catalog.page_snapshot (
    id uuid DEFAULT uuidv7() NOT NULL,
    page_id uuid NOT NULL,
    first_seen_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    extractor_version integer NOT NULL,
    text text NOT NULL,
    text_sha256 bytea NOT NULL,
    CONSTRAINT page_snapshot_text_check CHECK ((text <> ''::text)),
    CONSTRAINT page_snapshot_text_sha256_check CHECK ((octet_length(text_sha256) = 32))
);



ALTER TABLE catalog.page_snapshot OWNER TO axiom_owner;

CREATE TABLE catalog.reference_price (
    id uuid DEFAULT uuidv7() NOT NULL,
    model_id uuid NOT NULL,
    currency text NOT NULL,
    source text NOT NULL,
    schedule jsonb,
    schedule_sha256 bytea,
    published_at timestamptz DEFAULT now() NOT NULL,
    supersedes uuid,
    CONSTRAINT reference_price_check CHECK ((supersedes <> id)),
    CONSTRAINT reference_price_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT reference_price_schedule_sha256_check CHECK ((octet_length(schedule_sha256) = 32)),
    CONSTRAINT reference_price_source_check CHECK ((source = ANY (ARRAY['litellm'::text, 'openrouter'::text, 'official'::text, 'manual'::text]))),
    CONSTRAINT schedule_in_currency CHECK (((jsonb_typeof(schedule) = 'object'::text) AND ((schedule ->> 'currency'::text) = currency))),
    CONSTRAINT schedule_with_hash CHECK (((schedule IS NULL) = (schedule_sha256 IS NULL)))
);



ALTER TABLE catalog.reference_price OWNER TO axiom_owner;

CREATE TABLE catalog.review (
    id uuid DEFAULT uuidv7() NOT NULL,
    page_id uuid NOT NULL,
    kind text DEFAULT 'page_changed'::text NOT NULL,
    snapshot_id uuid NOT NULL,
    previous_snapshot_id uuid,
    removed text NOT NULL,
    added text NOT NULL,
    content_seq bigint DEFAULT 1 NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    resolution text,
    CONSTRAINT review_kind_check CHECK ((kind = 'page_changed'::text)),
    CONSTRAINT review_status_check CHECK ((status = ANY (ARRAY['open'::text, 'done'::text, 'dismissed'::text])))
);



ALTER TABLE catalog.review OWNER TO axiom_owner;

CREATE TABLE catalog.revision (
    singleton boolean DEFAULT true NOT NULL,
    n bigint NOT NULL,
    CONSTRAINT revision_singleton_check CHECK (singleton)
);



ALTER TABLE catalog.revision OWNER TO axiom_owner;

CREATE TABLE catalog.sync_state (
    singleton boolean DEFAULT true NOT NULL,
    last_success_at timestamptz,
    failing_since timestamptz,
    failures bigint DEFAULT 0 NOT NULL,
    pending bigint DEFAULT 0 NOT NULL,
    CONSTRAINT sync_state_check CHECK (((failing_since IS NULL) = (failures = 0))),
    CONSTRAINT sync_state_failures_check CHECK ((failures >= 0)),
    CONSTRAINT sync_state_pending_check CHECK ((pending >= 0)),
    CONSTRAINT sync_state_singleton_check CHECK (singleton)
);



ALTER TABLE catalog.sync_state OWNER TO axiom_owner;

CREATE TABLE catalog.watched_page (
    id uuid DEFAULT uuidv7() NOT NULL,
    author text NOT NULL,
    url text NOT NULL,
    origin text NOT NULL,
    currency text NOT NULL,
    section_start text NOT NULL,
    section_end text NOT NULL,
    interval_seconds bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    next_check_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    lease_gen bigint DEFAULT 0 NOT NULL,
    lease_until timestamptz DEFAULT clock_timestamp() NOT NULL,
    last_ok_at timestamptz,
    failures bigint DEFAULT 0 NOT NULL,
    current_snapshot_id uuid,
    CONSTRAINT watched_page_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT watched_page_failures_check CHECK ((failures >= 0)),
    CONSTRAINT watched_page_interval_seconds_check CHECK (((interval_seconds >= 3600) AND (interval_seconds <= 604800))),
    CONSTRAINT watched_page_section_end_check CHECK ((section_end <> ''::text)),
    CONSTRAINT watched_page_section_start_check CHECK ((section_start <> ''::text)),
    CONSTRAINT watched_page_url_check CHECK ((url ~ '^https://'::text))
);



ALTER TABLE catalog.watched_page OWNER TO axiom_owner;

INSERT INTO catalog.revision (singleton, n) VALUES (true, 0);
INSERT INTO catalog.sync_state (singleton, last_success_at, failing_since, failures, pending) VALUES (true, NULL, NULL, 0, 0);

ALTER TABLE ONLY catalog.alias
    ADD CONSTRAINT alias_pkey PRIMARY KEY (name, rev);

ALTER TABLE ONLY catalog.author
    ADD CONSTRAINT author_pkey PRIMARY KEY (slug);

ALTER TABLE ONLY catalog.change
    ADD CONSTRAINT change_op_key_key UNIQUE (op_key);

ALTER TABLE ONLY catalog.change
    ADD CONSTRAINT change_pkey PRIMARY KEY (id);

ALTER TABLE ONLY catalog.model
    ADD CONSTRAINT model_pkey PRIMARY KEY (id);

ALTER TABLE ONLY catalog.model
    ADD CONSTRAINT model_slug_key UNIQUE (slug);

ALTER TABLE ONLY catalog.name
    ADD CONSTRAINT name_norm_key UNIQUE (norm);

ALTER TABLE ONLY catalog.name
    ADD CONSTRAINT name_pkey PRIMARY KEY (name);

ALTER TABLE ONLY catalog.page_observation
    ADD CONSTRAINT page_observation_pkey PRIMARY KEY (id);

ALTER TABLE ONLY catalog.page_site
    ADD CONSTRAINT page_site_pkey PRIMARY KEY (origin);

ALTER TABLE ONLY catalog.page_snapshot
    ADD CONSTRAINT page_snapshot_page_id_extractor_version_text_sha256_key UNIQUE (page_id, extractor_version, text_sha256);

ALTER TABLE ONLY catalog.page_snapshot
    ADD CONSTRAINT page_snapshot_page_id_id_key UNIQUE (page_id, id);

ALTER TABLE ONLY catalog.page_snapshot
    ADD CONSTRAINT page_snapshot_pkey PRIMARY KEY (id);

ALTER TABLE ONLY catalog.reference_price
    ADD CONSTRAINT reference_price_id_model_id_currency_key UNIQUE (id, model_id, currency);

ALTER TABLE ONLY catalog.reference_price
    ADD CONSTRAINT reference_price_pkey PRIMARY KEY (id);

ALTER TABLE ONLY catalog.reference_price
    ADD CONSTRAINT reference_price_supersedes_key UNIQUE (supersedes);

ALTER TABLE ONLY catalog.review
    ADD CONSTRAINT review_pkey PRIMARY KEY (id);

ALTER TABLE ONLY catalog.revision
    ADD CONSTRAINT revision_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY catalog.sync_state
    ADD CONSTRAINT sync_state_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY catalog.watched_page
    ADD CONSTRAINT watched_page_pkey PRIMARY KEY (id);

CREATE INDEX model_slug_prefix ON catalog.model USING btree (slug text_pattern_ops);

CREATE INDEX name_model ON catalog.name USING btree (model_id);

CREATE UNIQUE INDEX reference_price_first ON catalog.reference_price USING btree (model_id, currency) WHERE (supersedes IS NULL);

CREATE INDEX review_dismissed_page ON catalog.review USING btree (page_id, snapshot_id) WHERE (status = 'dismissed'::text);

CREATE UNIQUE INDEX review_open_page ON catalog.review USING btree (page_id) WHERE (status = 'open'::text);

CREATE UNIQUE INDEX watched_page_active_url ON catalog.watched_page USING btree (url) WHERE enabled;

CREATE INDEX watched_page_due ON catalog.watched_page USING btree (next_check_at) WHERE enabled;

CREATE TRIGGER refuse_page_extractor_downgrade BEFORE UPDATE OF current_snapshot_id ON catalog.watched_page FOR EACH ROW EXECUTE FUNCTION catalog.refuse_page_extractor_downgrade();

ALTER TABLE ONLY catalog.alias
    ADD CONSTRAINT alias_model_id_fkey FOREIGN KEY (model_id) REFERENCES catalog.model(id);

ALTER TABLE ONLY catalog.model
    ADD CONSTRAINT model_author_fkey FOREIGN KEY (author) REFERENCES catalog.author(slug);

ALTER TABLE ONLY catalog.name
    ADD CONSTRAINT name_model_id_fkey FOREIGN KEY (model_id) REFERENCES catalog.model(id);

ALTER TABLE ONLY catalog.page_observation
    ADD CONSTRAINT page_observation_page_id_snapshot_id_fkey FOREIGN KEY (page_id, snapshot_id) REFERENCES catalog.page_snapshot(page_id, id);

ALTER TABLE ONLY catalog.page_snapshot
    ADD CONSTRAINT page_snapshot_page_id_fkey FOREIGN KEY (page_id) REFERENCES catalog.watched_page(id);

ALTER TABLE ONLY catalog.reference_price
    ADD CONSTRAINT reference_price_model_id_fkey FOREIGN KEY (model_id) REFERENCES catalog.model(id);

ALTER TABLE ONLY catalog.reference_price
    ADD CONSTRAINT reference_price_supersedes_model_id_currency_fkey FOREIGN KEY (supersedes, model_id, currency) REFERENCES catalog.reference_price(id, model_id, currency);

ALTER TABLE ONLY catalog.review
    ADD CONSTRAINT review_page_id_fkey FOREIGN KEY (page_id) REFERENCES catalog.watched_page(id);

ALTER TABLE ONLY catalog.review
    ADD CONSTRAINT review_page_id_previous_snapshot_id_fkey FOREIGN KEY (page_id, previous_snapshot_id) REFERENCES catalog.page_snapshot(page_id, id);

ALTER TABLE ONLY catalog.review
    ADD CONSTRAINT review_page_id_snapshot_id_fkey FOREIGN KEY (page_id, snapshot_id) REFERENCES catalog.page_snapshot(page_id, id);

ALTER TABLE ONLY catalog.watched_page
    ADD CONSTRAINT watched_page_author_fkey FOREIGN KEY (author) REFERENCES catalog.author(slug);

ALTER TABLE ONLY catalog.watched_page
    ADD CONSTRAINT watched_page_id_current_snapshot_id_fkey FOREIGN KEY (id, current_snapshot_id) REFERENCES catalog.page_snapshot(page_id, id);

ALTER TABLE ONLY catalog.watched_page
    ADD CONSTRAINT watched_page_origin_fkey FOREIGN KEY (origin) REFERENCES catalog.page_site(origin);









CREATE POLICY platform_insert ON catalog.alias FOR INSERT WITH CHECK (platform.in_platform_scope());

CREATE POLICY platform_insert ON catalog.author FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.change FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.model FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.name FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.page_observation FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.page_site FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.page_snapshot FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.reference_price FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.review FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON catalog.watched_page FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_read ON catalog.change FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_read ON catalog.page_observation FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_read ON catalog.page_site FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_read ON catalog.page_snapshot FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_read ON catalog.review FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_read ON catalog.sync_state FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_read ON catalog.watched_page FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON catalog.author FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON catalog.model FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON catalog.page_site FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON catalog.review FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON catalog.revision FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON catalog.sync_state FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON catalog.watched_page FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY public_read ON catalog.alias FOR SELECT USING (platform.in_any_scope());

CREATE POLICY public_read ON catalog.author FOR SELECT USING (( SELECT platform.in_any_scope() AS in_any_scope));

CREATE POLICY public_read ON catalog.model FOR SELECT USING ((( SELECT platform.in_any_scope() AS in_any_scope) AND ((status <> 'draft'::text) OR ( SELECT platform.in_platform_scope() AS in_platform_scope))));

CREATE POLICY public_read ON catalog.name FOR SELECT USING ((( SELECT platform.in_any_scope() AS in_any_scope) AND (( SELECT platform.in_platform_scope() AS in_platform_scope) OR (EXISTS ( SELECT
   FROM catalog.model m
  WHERE ((m.id = name.model_id) AND (m.status = 'listed'::text)))))));

CREATE POLICY public_read ON catalog.reference_price FOR SELECT USING ((( SELECT platform.in_any_scope() AS in_any_scope) AND (( SELECT platform.in_platform_scope() AS in_platform_scope) OR (EXISTS ( SELECT
   FROM catalog.model m
  WHERE ((m.id = reference_price.model_id) AND (m.status = 'listed'::text)))))));

CREATE POLICY public_read ON catalog.revision FOR SELECT USING (( SELECT platform.in_any_scope() AS in_any_scope));






GRANT USAGE ON SCHEMA catalog TO axiom_app;

GRANT SELECT ON TABLE catalog.alias TO axiom_app;

GRANT INSERT(name) ON TABLE catalog.alias TO axiom_app;

GRANT INSERT(rev) ON TABLE catalog.alias TO axiom_app;

GRANT INSERT(model_id) ON TABLE catalog.alias TO axiom_app;

GRANT SELECT ON TABLE catalog.author TO axiom_app;

GRANT INSERT(slug) ON TABLE catalog.author TO axiom_app;

GRANT INSERT(name),UPDATE(name) ON TABLE catalog.author TO axiom_app;

GRANT SELECT ON TABLE catalog.change TO axiom_app;

GRANT INSERT(op_key) ON TABLE catalog.change TO axiom_app;

GRANT INSERT(actor) ON TABLE catalog.change TO axiom_app;

GRANT INSERT(reason) ON TABLE catalog.change TO axiom_app;

GRANT INSERT(kind) ON TABLE catalog.change TO axiom_app;

GRANT INSERT(subject) ON TABLE catalog.change TO axiom_app;

GRANT INSERT(before) ON TABLE catalog.change TO axiom_app;

GRANT INSERT(after) ON TABLE catalog.change TO axiom_app;

GRANT INSERT(evidence) ON TABLE catalog.change TO axiom_app;

GRANT SELECT ON TABLE catalog.model TO axiom_app;

GRANT INSERT(author) ON TABLE catalog.model TO axiom_app;

GRANT INSERT(slug) ON TABLE catalog.model TO axiom_app;

GRANT UPDATE(status) ON TABLE catalog.model TO axiom_app;

GRANT INSERT(version_basis) ON TABLE catalog.model TO axiom_app;

GRANT INSERT(price_source),UPDATE(price_source) ON TABLE catalog.model TO axiom_app;

GRANT INSERT(spec),UPDATE(spec) ON TABLE catalog.model TO axiom_app;

GRANT UPDATE(rev) ON TABLE catalog.model TO axiom_app;

GRANT SELECT ON TABLE catalog.name TO axiom_app;

GRANT INSERT(name) ON TABLE catalog.name TO axiom_app;

GRANT INSERT(norm) ON TABLE catalog.name TO axiom_app;

GRANT INSERT(visibility) ON TABLE catalog.name TO axiom_app;

GRANT INSERT(model_id) ON TABLE catalog.name TO axiom_app;

GRANT SELECT,INSERT ON TABLE catalog.page_observation TO axiom_app;

GRANT SELECT,INSERT ON TABLE catalog.page_site TO axiom_app;

GRANT UPDATE(delay_seconds) ON TABLE catalog.page_site TO axiom_app;

GRANT UPDATE(last_request_at) ON TABLE catalog.page_site TO axiom_app;

GRANT UPDATE(next_request_at) ON TABLE catalog.page_site TO axiom_app;

GRANT SELECT,INSERT ON TABLE catalog.page_snapshot TO axiom_app;

GRANT SELECT ON TABLE catalog.reference_price TO axiom_app;

GRANT INSERT(model_id) ON TABLE catalog.reference_price TO axiom_app;

GRANT INSERT(currency) ON TABLE catalog.reference_price TO axiom_app;

GRANT INSERT(source) ON TABLE catalog.reference_price TO axiom_app;

GRANT INSERT(schedule) ON TABLE catalog.reference_price TO axiom_app;

GRANT INSERT(schedule_sha256) ON TABLE catalog.reference_price TO axiom_app;

GRANT INSERT(supersedes) ON TABLE catalog.reference_price TO axiom_app;

GRANT SELECT,INSERT ON TABLE catalog.review TO axiom_app;

GRANT UPDATE(snapshot_id) ON TABLE catalog.review TO axiom_app;

GRANT UPDATE(previous_snapshot_id) ON TABLE catalog.review TO axiom_app;

GRANT UPDATE(removed) ON TABLE catalog.review TO axiom_app;

GRANT UPDATE(added) ON TABLE catalog.review TO axiom_app;

GRANT UPDATE(content_seq) ON TABLE catalog.review TO axiom_app;

GRANT UPDATE(status) ON TABLE catalog.review TO axiom_app;

GRANT UPDATE(resolution) ON TABLE catalog.review TO axiom_app;

GRANT SELECT ON TABLE catalog.revision TO axiom_app;

GRANT UPDATE(n) ON TABLE catalog.revision TO axiom_app;

GRANT SELECT ON TABLE catalog.sync_state TO axiom_app;

GRANT UPDATE(last_success_at) ON TABLE catalog.sync_state TO axiom_app;

GRANT UPDATE(failing_since) ON TABLE catalog.sync_state TO axiom_app;

GRANT UPDATE(failures) ON TABLE catalog.sync_state TO axiom_app;

GRANT UPDATE(pending) ON TABLE catalog.sync_state TO axiom_app;

GRANT SELECT,INSERT ON TABLE catalog.watched_page TO axiom_app;

GRANT UPDATE(enabled) ON TABLE catalog.watched_page TO axiom_app;

GRANT UPDATE(next_check_at) ON TABLE catalog.watched_page TO axiom_app;

GRANT UPDATE(lease_gen) ON TABLE catalog.watched_page TO axiom_app;

GRANT UPDATE(lease_until) ON TABLE catalog.watched_page TO axiom_app;

GRANT UPDATE(last_ok_at) ON TABLE catalog.watched_page TO axiom_app;

GRANT UPDATE(failures) ON TABLE catalog.watched_page TO axiom_app;

GRANT UPDATE(current_snapshot_id) ON TABLE catalog.watched_page TO axiom_app;


ALTER TABLE ONLY catalog.alias FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.author FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.change FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.model FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.name FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.page_observation FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.page_site FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.page_snapshot FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.reference_price FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.review FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.revision FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.sync_state FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY catalog.watched_page FORCE ROW LEVEL SECURITY;
ALTER TABLE catalog.alias ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.author ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.change ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.model ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.name ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.page_observation ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.page_site ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.page_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.reference_price ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.review ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.revision ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.watched_page ENABLE ROW LEVEL SECURITY;
