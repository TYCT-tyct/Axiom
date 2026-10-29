-- +goose Up
CREATE TABLE accounts.notification_preferences (
    site_id uuid NOT NULL, account_id uuid NOT NULL, user_id uuid PRIMARY KEY,
    low_balance boolean, top_up boolean, factor_changed boolean, threshold bigint CHECK (threshold >= 0),
    webhook_url text NOT NULL DEFAULT '', language text NOT NULL CHECK (language IN ('en','zh')),
    FOREIGN KEY (site_id,account_id,user_id) REFERENCES accounts.users(site_id,account_id,id)
);
CREATE TABLE accounts.user_notices (
    id uuid PRIMARY KEY DEFAULT uuidv7(), site_id uuid NOT NULL, account_id uuid NOT NULL, user_id uuid NOT NULL,
    source text NOT NULL, kind text NOT NULL CHECK (kind IN ('low_balance','top_up','factor_changed')),
    value bigint NOT NULL, at timestamptz NOT NULL DEFAULT clock_timestamp(),
    email_done boolean NOT NULL DEFAULT false, email_failed boolean NOT NULL DEFAULT false, dispatched boolean NOT NULL DEFAULT false, webhook_url text NOT NULL DEFAULT '',
    webhook_state text NOT NULL DEFAULT 'none' CHECK (webhook_state IN ('none','queued','delivered','failed')),
    UNIQUE(user_id,kind,source),
    FOREIGN KEY (site_id,account_id,user_id) REFERENCES accounts.users(site_id,account_id,id)
);
CREATE INDEX user_notices_pending ON accounts.user_notices(id) WHERE NOT dispatched;
CREATE INDEX user_notices_retention ON accounts.user_notices(at) WHERE dispatched AND webhook_state <> 'queued';
CREATE TABLE accounts.announcements (
    id uuid PRIMARY KEY, site_id uuid NOT NULL REFERENCES accounts.sites(id), title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body text NOT NULL CHECK (length(body) BETWEEN 1 AND 20000), starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
    withdrawn_at timestamptz, CHECK (ends_at > starts_at)
);
CREATE INDEX announcements_active ON accounts.announcements(site_id,ends_at,starts_at) WHERE withdrawn_at IS NULL;
ALTER TABLE accounts.notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.notification_preferences FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.notification_preferences FOR SELECT USING (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])));
CREATE POLICY inserts ON accounts.notification_preferences FOR INSERT WITH CHECK (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])));
CREATE POLICY updates ON accounts.notification_preferences FOR UPDATE USING (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))) WITH CHECK (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])));
GRANT SELECT, INSERT ON accounts.notification_preferences TO axiom_app;
ALTER TABLE accounts.user_notices ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.user_notices FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.user_notices FOR SELECT USING (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])));
CREATE POLICY inserts ON accounts.user_notices FOR INSERT WITH CHECK (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])));
CREATE POLICY updates ON accounts.user_notices FOR UPDATE USING (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))) WITH CHECK (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])));
GRANT SELECT, INSERT ON accounts.user_notices TO axiom_app;
ALTER TABLE accounts.announcements ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.announcements FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.announcements FOR SELECT USING (((SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[]) OR site_id = (SELECT platform.home_site())));
CREATE POLICY inserts ON accounts.announcements FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON accounts.announcements FOR UPDATE USING ((SELECT platform.in_platform_scope()) AND withdrawn_at IS NULL) WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON accounts.announcements TO axiom_app;
GRANT UPDATE (low_balance,top_up,factor_changed,threshold,webhook_url,language) ON accounts.notification_preferences TO axiom_app;
GRANT UPDATE (email_done,email_failed,dispatched,webhook_url,webhook_state) ON accounts.user_notices TO axiom_app;
GRANT UPDATE (withdrawn_at) ON accounts.announcements TO axiom_app;
GRANT DELETE ON accounts.user_notices TO axiom_app;
CREATE POLICY expiry ON accounts.user_notices FOR DELETE USING (
 (SELECT platform.in_platform_scope()) AND dispatched AND webhook_state <> 'queued' AND at < statement_timestamp()-interval '30 days');

-- Keep every factor mutation, including CLI and old rolling-release writers, atomic with its notice.
-- +goose StatementBegin
CREATE FUNCTION accounts.factor_notice() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    INSERT INTO accounts.user_notices(site_id,account_id,user_id,source,kind,value)
    VALUES(NEW.site_id,NEW.account_id,NEW.id,NEW.factor_version::text,'factor_changed',NEW.factor_version);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER factor_notice AFTER UPDATE OF factor_version ON accounts.users
FOR EACH ROW WHEN (NEW.factor_version <> OLD.factor_version) EXECUTE FUNCTION accounts.factor_notice();
