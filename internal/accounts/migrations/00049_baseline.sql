-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;

-- +goose StatementBegin
CREATE FUNCTION accounts.clear_session_recovery_codes() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    NEW.recovery_codes_ciphertext := NULL;
    NEW.recovery_codes_nonce := NULL;
    RETURN NEW;
END $$;


ALTER FUNCTION accounts.clear_session_recovery_codes() OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE accounts.site_domains (
    domain text NOT NULL,
    site_id uuid NOT NULL,
    surface text NOT NULL,
    origin text NOT NULL,
    disabled_at timestamptz,
    CONSTRAINT site_domains_check CHECK (((origin = ('https://'::text || domain)) OR (origin ~ '^http://localhost(:[0-9]{1,5})?$'::text))),
    CONSTRAINT site_domains_domain_check CHECK (((domain = lower(domain)) AND (domain <> ''::text))),
    CONSTRAINT site_domains_surface_check CHECK ((surface = ANY (ARRAY['console'::text, 'api'::text, 'admin'::text])))
);



ALTER TABLE accounts.site_domains OWNER TO axiom_owner;

CREATE TABLE accounts.sites (
    id uuid NOT NULL,
    parent_site_id uuid,
    owner_account_id uuid,
    is_main boolean GENERATED ALWAYS AS ((parent_site_id IS NULL)) STORED NOT NULL,
    parent_is_main boolean GENERATED ALWAYS AS (true) STORED NOT NULL,
    name text NOT NULL,
    status text NOT NULL,
    key_prefix text NOT NULL,
    registration text NOT NULL,
    default_tier_id uuid NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    logo_hash bytea,
    brand_color text,
    login_domain text,
    CONSTRAINT sites_brand_color_check CHECK (((brand_color IS NULL) OR (brand_color ~ '^#[0-9a-f]{6}$'::text))),
    CONSTRAINT sites_check CHECK (((parent_site_id IS NULL) = (owner_account_id IS NULL))),
    CONSTRAINT sites_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT sites_key_prefix_check CHECK ((key_prefix ~ '^[a-z0-9-]{1,15}-$'::text)),
    CONSTRAINT sites_login_domain_check CHECK (((login_domain IS NULL) OR ((login_domain = lower(login_domain)) AND (login_domain <> ''::text)))),
    CONSTRAINT sites_logo_hash_check CHECK (((logo_hash IS NULL) OR (octet_length(logo_hash) = 32))),
    CONSTRAINT sites_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT sites_registration_check CHECK ((registration = ANY (ARRAY['invite'::text, 'closed'::text, 'open'::text]))),
    CONSTRAINT sites_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);



ALTER TABLE accounts.sites OWNER TO axiom_owner;

-- +goose StatementBegin
CREATE FUNCTION accounts.domain_site(domain text, surface text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    RETURN (SELECT d.site_id FROM (accounts.site_domains d JOIN accounts.sites s ON ((s.id = d.site_id))) WHERE ((d.domain = domain_site.domain) AND (d.surface = domain_site.surface) AND (d.disabled_at IS NULL) AND (s.status = 'active'::text)));


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.factor_notice() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    INSERT INTO accounts.user_notices(site_id,account_id,user_id,source,kind,value)
    VALUES(NEW.site_id,NEW.account_id,NEW.id,NEW.factor_version::text,'factor_changed',NEW.factor_version);
    RETURN NEW;
END $$;


ALTER FUNCTION accounts.factor_notice() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.invite_tenant(domain text, code text) RETURNS TABLE(site_id uuid, inviter_user_id uuid, default_tier_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT s.id, u.id, s.default_tier_id FROM accounts.sites s
        JOIN accounts.users u ON u.site_id = s.id
        WHERE s.id = accounts.domain_site(invite_tenant.domain, 'console') AND s.registration = 'invite'
            AND u.invite_code = invite_tenant.code AND u.status = 'active'
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.key_tenant(site uuid, hashes bytea[]) RETURNS TABLE(key_id uuid, account_id uuid, workspace_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT k.id, k.account_id, k.workspace_id FROM accounts.api_keys k
        WHERE k.site_id = key_tenant.site AND k.secret_hash = ANY (key_tenant.hashes) AND k.deleted_at IS NULL
        LIMIT 1
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.login_tenant(domain text, email text) RETURNS TABLE(user_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT u.id, u.account_id FROM accounts.users u
        WHERE u.email = login_tenant.email AND u.site_id IN (
            accounts.domain_site(login_tenant.domain, 'console'), accounts.domain_site(login_tenant.domain, 'admin'))
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.passkey_tenant(credential_id bytea) RETURNS TABLE(site_id uuid, account_id uuid, user_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT c.site_id, c.account_id, c.user_id FROM accounts.passkey_credentials c
        WHERE c.credential_id = passkey_tenant.credential_id
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.password_reset_request_tenant(id uuid) RETURNS TABLE(site_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT r.site_id, NULL::uuid FROM accounts.password_reset_requests r
        WHERE r.id = password_reset_request_tenant.id
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.password_reset_tenant(domain text, token_hash bytea) RETURNS TABLE(id uuid, user_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT t.id, t.user_id, t.account_id FROM accounts.password_reset_tokens t
        WHERE t.token_hash = password_reset_tenant.token_hash
            AND t.site_id = accounts.domain_site(password_reset_tenant.domain, 'console')
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.refuse_console_password_with_passkey() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    IF NEW.surface = 'console' AND NEW.level = 'password' AND EXISTS (
        SELECT 1 FROM accounts.passkey_credentials p
        WHERE p.site_id = NEW.site_id AND p.account_id = NEW.account_id AND p.user_id = NEW.user_id
    ) THEN
        RAISE EXCEPTION 'a console password session cannot skip a passkey' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;


ALTER FUNCTION accounts.refuse_console_password_with_passkey() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.refuse_totp_with_unproved_passkey() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    IF OLD.mfa_enabled_at IS NULL AND NEW.mfa_enabled_at IS NOT NULL
        AND EXISTS (SELECT 1 FROM accounts.passkey_credentials p WHERE p.user_id = NEW.id)
        AND coalesce(current_setting('axiom.passkey_proved', true), '') <> NEW.id::text THEN
        RAISE EXCEPTION 'TOTP cannot be enabled for a passkey user without proving the passkey'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;


ALTER FUNCTION accounts.refuse_totp_with_unproved_passkey() OWNER TO axiom_owner;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.registration_request_tenant(id uuid) RETURNS TABLE(site_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT r.site_id, NULL::uuid FROM accounts.registration_requests r
        WHERE r.id = registration_request_tenant.id
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.registration_site(domain text) RETURNS TABLE(site_id uuid, default_tier_id uuid, registration text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT s.id, s.default_tier_id, s.registration FROM accounts.sites s
        WHERE s.id = accounts.domain_site(registration_site.domain, 'console') AND s.status = 'active'
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.registration_tenant(domain text, token_hash bytea) RETURNS TABLE(id uuid, site_id uuid, email text, unexpired boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT r.id, r.site_id, r.email, r.expires_at > clock_timestamp()
        FROM accounts.registration_requests r
        JOIN accounts.sites s ON s.id = r.site_id AND s.status = 'active'
        WHERE r.token_hash = registration_tenant.token_hash
            AND r.site_id = accounts.domain_site(registration_tenant.domain, 'console')
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.session_tenant(domain text, surface text, token_hash bytea) RETURNS TABLE(session_id uuid, user_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
        SELECT s.id, s.user_id, s.account_id FROM accounts.sessions s
        WHERE s.token_hash = session_tenant.token_hash AND s.surface = session_tenant.surface
            AND s.site_id = accounts.domain_site(session_tenant.domain, session_tenant.surface)
    $$;


-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts.set_legal_acceptance_locale() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
    NEW.locale := (
        SELECT d.locale
        FROM accounts.legal_documents d
        WHERE d.site_id = NEW.site_id AND d.kind = NEW.kind AND d.version = NEW.version
    );
    RETURN NEW;
END;
$$;


ALTER FUNCTION accounts.set_legal_acceptance_locale() OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE accounts.accounts (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    status text NOT NULL,
    tier_id uuid NOT NULL,
    max_keys integer,
    key_creations_per_hour integer,
    rate_requests integer,
    billing_preference text,
    max_workspaces integer,
    CONSTRAINT accounts_billing_preference_check CHECK ((billing_preference = ANY (ARRAY['subscription_first'::text, 'wallet_first'::text, 'subscription_only'::text, 'wallet_only'::text]))),
    CONSTRAINT accounts_key_creations_per_hour_check CHECK (((key_creations_per_hour >= 1) AND (key_creations_per_hour <= 10000))),
    CONSTRAINT accounts_kind_check CHECK ((kind = ANY (ARRAY['personal'::text, 'organization'::text]))),
    CONSTRAINT accounts_max_keys_check CHECK (((max_keys >= 1) AND (max_keys <= 10000))),
    CONSTRAINT accounts_max_workspaces_check CHECK (((max_workspaces >= 1) AND (max_workspaces <= 1000))),
    CONSTRAINT accounts_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT accounts_rate_requests_check CHECK (((rate_requests >= 0) AND (rate_requests <= 1000000000))),
    CONSTRAINT accounts_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);



ALTER TABLE accounts.accounts OWNER TO axiom_owner;

CREATE TABLE accounts.agent_credential_tokens (
    secret_hash bytea NOT NULL,
    secret_version bigint NOT NULL,
    credential_id uuid NOT NULL,
    last4 text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    retires_at timestamptz,
    CONSTRAINT agent_credential_tokens_last4_check CHECK ((last4 ~ '^[A-Za-z0-9]{4}$'::text)),
    CONSTRAINT agent_credential_tokens_secret_hash_check CHECK ((octet_length(secret_hash) = 32)),
    CONSTRAINT agent_credential_tokens_secret_version_check CHECK ((secret_version >= 1))
);



ALTER TABLE accounts.agent_credential_tokens OWNER TO axiom_owner;

CREATE TABLE accounts.agent_credentials (
    id uuid NOT NULL,
    name text NOT NULL,
    permissions text[] NOT NULL,
    sites uuid[],
    platform boolean NOT NULL,
    proposer_vendors text[] NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_used_at timestamptz,
    CONSTRAINT agent_credentials_check CHECK ((expires_at > created_at)),
    CONSTRAINT agent_credentials_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT agent_credentials_permissions_check CHECK ((('operations.read'::text = ANY (permissions)) AND (NOT (permissions && ARRAY['operations.manage'::text, 'operations.approve'::text, 'platform_grants.manage'::text])))),
    CONSTRAINT agent_credentials_proposer_vendors_check CHECK ((cardinality(proposer_vendors) <= 20)),
    CONSTRAINT agent_credentials_sites_check CHECK (((cardinality(sites) >= 1) AND (cardinality(sites) <= 100)))
);



ALTER TABLE accounts.agent_credentials OWNER TO axiom_owner;

CREATE TABLE accounts.announcements (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    withdrawn_at timestamptz,
    CONSTRAINT announcements_body_check CHECK (((length(body) >= 1) AND (length(body) <= 20000))),
    CONSTRAINT announcements_check CHECK ((ends_at > starts_at)),
    CONSTRAINT announcements_title_check CHECK (((length(title) >= 1) AND (length(title) <= 200)))
);



ALTER TABLE accounts.announcements OWNER TO axiom_owner;

CREATE TABLE accounts.api_keys (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    prefix text NOT NULL,
    last4 text NOT NULL,
    secret_hash bytea NOT NULL,
    secret_version bigint NOT NULL,
    created_by uuid NOT NULL,
    expires_at timestamptz,
    status text NOT NULL,
    spend_limit bigint,
    limit_reset text,
    preset_id uuid,
    models text[],
    ip_allow cidr[],
    deleted_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    management_window timestamptz,
    management_requests integer DEFAULT 0 NOT NULL,
    rate_requests integer,
    last_used_at timestamptz,
    CONSTRAINT api_keys_check CHECK (((limit_reset IS NULL) OR ((limit_reset = ANY (ARRAY['daily'::text, 'weekly'::text, 'monthly'::text])) AND (spend_limit IS NOT NULL)))),
    CONSTRAINT api_keys_check1 CHECK (((kind = 'call'::text) OR (expires_at IS NOT NULL))),
    CONSTRAINT api_keys_kind_check CHECK ((kind = ANY (ARRAY['call'::text, 'management'::text]))),
    CONSTRAINT api_keys_last4_check CHECK ((last4 ~ '^[A-Za-z0-9]{4}$'::text)),
    CONSTRAINT api_keys_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT api_keys_prefix_check CHECK ((prefix ~ '^[a-z0-9-]{1,15}-$'::text)),
    CONSTRAINT api_keys_preset_call CHECK (((kind = 'call'::text) OR (preset_id IS NULL))),
    CONSTRAINT api_keys_rate_requests_check CHECK (((rate_requests >= 0) AND (rate_requests <= 1000000000))),
    CONSTRAINT api_keys_secret_hash_check CHECK ((octet_length(secret_hash) = 32)),
    CONSTRAINT api_keys_secret_version_check CHECK ((secret_version >= 1)),
    CONSTRAINT api_keys_spend_limit_check CHECK ((spend_limit >= 0)),
    CONSTRAINT api_keys_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);



ALTER TABLE accounts.api_keys OWNER TO axiom_owner;

CREATE TABLE accounts.email_changes (
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    email text NOT NULL,
    token_hash bytea NOT NULL,
    factor_version bigint NOT NULL,
    language text NOT NULL,
    expires_at timestamptz NOT NULL,
    CONSTRAINT email_changes_email_check CHECK (((char_length(email) >= 3) AND (char_length(email) <= 254))),
    CONSTRAINT email_changes_language_check CHECK ((language = ANY (ARRAY['zh'::text, 'en'::text]))),
    CONSTRAINT email_changes_token_hash_check CHECK ((octet_length(token_hash) = 32))
);



ALTER TABLE accounts.email_changes OWNER TO axiom_owner;

CREATE TABLE accounts.legal_acceptances (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    version integer NOT NULL,
    accepted_at timestamptz DEFAULT now() NOT NULL,
    locale text DEFAULT 'zh'::text NOT NULL,
    CONSTRAINT legal_acceptances_locale_check CHECK ((locale = ANY (ARRAY['zh'::text, 'en'::text]))),
    CONSTRAINT legal_acceptances_version_check CHECK ((version >= 1))
);



ALTER TABLE accounts.legal_acceptances OWNER TO axiom_owner;

CREATE TABLE accounts.legal_documents (
    site_id uuid NOT NULL,
    kind text NOT NULL,
    version integer NOT NULL,
    body text NOT NULL,
    published_at timestamptz DEFAULT now() NOT NULL,
    locale text DEFAULT 'zh'::text NOT NULL,
    CONSTRAINT legal_documents_body_check CHECK (((char_length(body) >= 1) AND (char_length(body) <= 65536))),
    CONSTRAINT legal_documents_kind_check CHECK ((kind = ANY (ARRAY['terms'::text, 'privacy'::text]))),
    CONSTRAINT legal_documents_locale_check CHECK ((locale = ANY (ARRAY['zh'::text, 'en'::text]))),
    CONSTRAINT legal_documents_version_check CHECK ((version >= 1))
);



ALTER TABLE accounts.legal_documents OWNER TO axiom_owner;

CREATE TABLE accounts.members (
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    account_kind text NOT NULL,
    user_id uuid NOT NULL,
    user_account_id uuid NOT NULL,
    role_id uuid NOT NULL,
    personal_owner uuid GENERATED ALWAYS AS (
CASE
    WHEN (account_kind = 'personal'::text) THEN account_id
    ELSE NULL::uuid
END) STORED
);



ALTER TABLE accounts.members OWNER TO axiom_owner;

CREATE TABLE accounts.notification_preferences (
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    low_balance boolean NOT NULL,
    top_up boolean NOT NULL,
    factor_changed boolean NOT NULL,
    threshold bigint NOT NULL,
    webhook_url text DEFAULT ''::text NOT NULL,
    language text NOT NULL,
    spend_anomaly boolean DEFAULT true NOT NULL,
    CONSTRAINT notification_preferences_language_check CHECK ((language = ANY (ARRAY['en'::text, 'zh'::text]))),
    CONSTRAINT notification_preferences_threshold_check CHECK ((threshold >= 0))
);



ALTER TABLE accounts.notification_preferences OWNER TO axiom_owner;

CREATE TABLE accounts.oauth_flows (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    provider text NOT NULL,
    intent text NOT NULL,
    account_id uuid,
    user_id uuid,
    session_id uuid,
    state_hash bytea NOT NULL,
    nonce_hash bytea,
    verifier text NOT NULL,
    redirect_uri text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CONSTRAINT oauth_flows_check CHECK (((intent = 'link'::text) = (user_id IS NOT NULL))),
    CONSTRAINT oauth_flows_check1 CHECK (((intent = 'link'::text) = (account_id IS NOT NULL))),
    CONSTRAINT oauth_flows_check2 CHECK (((intent = 'link'::text) = (session_id IS NOT NULL))),
    CONSTRAINT oauth_flows_intent_check CHECK ((intent = ANY (ARRAY['sign_in'::text, 'link'::text]))),
    CONSTRAINT oauth_flows_nonce_hash_check CHECK (((nonce_hash IS NULL) OR (octet_length(nonce_hash) = 32))),
    CONSTRAINT oauth_flows_provider_check CHECK ((provider = ANY (ARRAY['github'::text, 'google'::text, 'linuxdo'::text]))),
    CONSTRAINT oauth_flows_redirect_uri_check CHECK (((char_length(redirect_uri) >= 1) AND (char_length(redirect_uri) <= 512))),
    CONSTRAINT oauth_flows_state_hash_check CHECK ((octet_length(state_hash) = 32)),
    CONSTRAINT oauth_flows_verifier_check CHECK (((char_length(verifier) >= 32) AND (char_length(verifier) <= 128)))
);



ALTER TABLE accounts.oauth_flows OWNER TO axiom_owner;

CREATE TABLE accounts.passkey_credentials (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    credential_id bytea NOT NULL,
    public_key bytea NOT NULL,
    sign_count bigint DEFAULT 0 NOT NULL,
    transports text[] DEFAULT '{}'::text[] NOT NULL,
    backup_eligible boolean DEFAULT false NOT NULL,
    backup_state boolean DEFAULT false NOT NULL,
    name text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    last_used_at timestamptz,
    CONSTRAINT passkey_credentials_credential_id_check CHECK (((octet_length(credential_id) >= 1) AND (octet_length(credential_id) <= 1023))),
    CONSTRAINT passkey_credentials_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT passkey_credentials_public_key_check CHECK (((octet_length(public_key) >= 1) AND (octet_length(public_key) <= 1024))),
    CONSTRAINT passkey_credentials_sign_count_check CHECK (((sign_count >= 0) AND (sign_count <= '4294967295'::bigint)))
);



ALTER TABLE accounts.passkey_credentials OWNER TO axiom_owner;

CREATE TABLE accounts.password_reset_requests (
    site_id uuid NOT NULL,
    email_hash bytea NOT NULL,
    id uuid NOT NULL,
    email text,
    domain text NOT NULL,
    language text NOT NULL,
    expires_at timestamptz DEFAULT (now() + '00:30:00'::interval) NOT NULL,
    CONSTRAINT password_reset_requests_email_hash_check CHECK ((octet_length(email_hash) = 32)),
    CONSTRAINT password_reset_requests_language_check CHECK ((language = ANY (ARRAY['zh'::text, 'en'::text])))
);



ALTER TABLE accounts.password_reset_requests OWNER TO axiom_owner;

CREATE TABLE accounts.password_reset_tokens (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    factor_version bigint NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CONSTRAINT password_reset_tokens_token_hash_check CHECK ((octet_length(token_hash) = 32))
);



ALTER TABLE accounts.password_reset_tokens OWNER TO axiom_owner;

CREATE TABLE accounts.platform_grants (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    site_is_main boolean GENERATED ALWAYS AS (true) STORED NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role_id uuid NOT NULL,
    granted_by uuid,
    revoked_at timestamptz
);



ALTER TABLE accounts.platform_grants OWNER TO axiom_owner;

CREATE TABLE accounts.presets (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    config jsonb NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    deleted_at timestamptz,
    CONSTRAINT presets_config_check CHECK (((jsonb_typeof(config) = 'object'::text) AND (octet_length((config)::text) <= 16384))),
    CONSTRAINT presets_name_check CHECK (((length(name) >= 1) AND (length(name) <= 100))),
    CONSTRAINT presets_slug_check CHECK ((slug ~ '^[a-zA-Z0-9_-]{1,100}$'::text))
);



ALTER TABLE accounts.presets OWNER TO axiom_owner;

CREATE TABLE accounts.recovery_codes (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    factor_version bigint NOT NULL,
    code_hash bytea NOT NULL,
    used_at timestamptz,
    CONSTRAINT recovery_codes_code_hash_check CHECK ((octet_length(code_hash) = 32))
);



ALTER TABLE accounts.recovery_codes OWNER TO axiom_owner;

CREATE TABLE accounts.registration_requests (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    email_hash bytea NOT NULL,
    email text NOT NULL,
    tier_id uuid NOT NULL,
    domain text NOT NULL,
    language text NOT NULL,
    token_hash bytea,
    expires_at timestamptz DEFAULT (now() + '24:00:00'::interval) NOT NULL,
    CONSTRAINT registration_requests_email_check CHECK (((char_length(email) >= 3) AND (char_length(email) <= 254))),
    CONSTRAINT registration_requests_email_hash_check CHECK ((octet_length(email_hash) = 32)),
    CONSTRAINT registration_requests_language_check CHECK ((language = ANY (ARRAY['zh'::text, 'en'::text]))),
    CONSTRAINT registration_requests_token_hash_check CHECK (((token_hash IS NULL) OR (octet_length(token_hash) = 32)))
);



ALTER TABLE accounts.registration_requests OWNER TO axiom_owner;

CREATE TABLE accounts.roles (
    id uuid NOT NULL,
    name text NOT NULL,
    permissions text[] NOT NULL,
    builtin boolean NOT NULL,
    additional_permissions text[] DEFAULT '{}'::text[] NOT NULL,
    CONSTRAINT roles_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100)))
);



ALTER TABLE accounts.roles OWNER TO axiom_owner;

CREATE TABLE accounts.security_failures (
    site_id uuid NOT NULL,
    started_at timestamptz DEFAULT date_trunc('minute'::text, statement_timestamp()) NOT NULL,
    failures bigint NOT NULL,
    completed_peak bigint DEFAULT 0 NOT NULL,
    sampled_at timestamptz DEFAULT '-infinity'::timestamptz NOT NULL,
    reported boolean DEFAULT false NOT NULL,
    CONSTRAINT security_failures_completed_peak_check CHECK ((completed_peak >= 0)),
    CONSTRAINT security_failures_failures_check CHECK ((failures >= 0))
);



ALTER TABLE accounts.security_failures OWNER TO axiom_owner;

CREATE TABLE accounts.sessions (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    surface text NOT NULL,
    token_hash bytea NOT NULL,
    csrf_token text NOT NULL,
    level text NOT NULL,
    factor_version bigint NOT NULL,
    mfa_attempts integer DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    last_seen_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    idle_timeout interval NOT NULL,
    revoked_at timestamptz,
    ip inet,
    user_agent text,
    recovery_codes_ciphertext bytea,
    recovery_codes_nonce bytea,
    CONSTRAINT sessions_csrf_token_check CHECK (((char_length(csrf_token) >= 32) AND (char_length(csrf_token) <= 128))),
    CONSTRAINT sessions_level_check CHECK ((level = ANY (ARRAY['pending_mfa'::text, 'password'::text, 'mfa'::text]))),
    CONSTRAINT sessions_mfa_attempts_check CHECK ((mfa_attempts >= 0)),
    CONSTRAINT sessions_recovery_codes_together CHECK ((num_nulls(recovery_codes_ciphertext, recovery_codes_nonce) = ANY (ARRAY[0, 2]))),
    CONSTRAINT sessions_surface_check CHECK ((surface = ANY (ARRAY['console'::text, 'admin'::text]))),
    CONSTRAINT sessions_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT sessions_user_agent_check CHECK ((char_length(user_agent) <= 512))
);



ALTER TABLE accounts.sessions OWNER TO axiom_owner;

CREATE TABLE accounts.site_billing_preferences (
    site_id uuid NOT NULL,
    preference text DEFAULT 'subscription_first'::text NOT NULL,
    CONSTRAINT site_billing_preferences_check CHECK ((preference = ANY (ARRAY['subscription_first'::text, 'wallet_first'::text, 'subscription_only'::text, 'wallet_only'::text])))
);



ALTER TABLE accounts.site_billing_preferences OWNER TO axiom_owner;

CREATE TABLE accounts.site_logos (
    site_id uuid NOT NULL,
    hash bytea NOT NULL,
    content_type text NOT NULL,
    width integer NOT NULL,
    height integer NOT NULL,
    bytes bytea NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT site_logos_bytes_check CHECK (((octet_length(bytes) >= 1) AND (octet_length(bytes) <= 262144))),
    CONSTRAINT site_logos_content_type_check CHECK ((content_type = ANY (ARRAY['image/svg+xml'::text, 'image/png'::text, 'image/webp'::text]))),
    CONSTRAINT site_logos_hash_check CHECK ((octet_length(hash) = 32)),
    CONSTRAINT site_logos_height_check CHECK (((height >= 0) AND (height <= 1024))),
    CONSTRAINT site_logos_width_check CHECK (((width >= 0) AND (width <= 1024)))
);



ALTER TABLE accounts.site_logos OWNER TO axiom_owner;

CREATE TABLE accounts.site_oauth_providers (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    provider text NOT NULL,
    client_id text NOT NULL,
    client_secret_ciphertext bytea NOT NULL,
    client_secret_nonce bytea NOT NULL,
    client_secret_wrapped_dek bytea NOT NULL,
    client_secret_key_version integer NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT site_oauth_providers_client_id_check CHECK (((char_length(client_id) >= 1) AND (char_length(client_id) <= 255))),
    CONSTRAINT site_oauth_providers_client_secret_key_version_check CHECK (((client_secret_key_version >= 1) AND (client_secret_key_version <= 2147483647))),
    CONSTRAINT site_oauth_providers_provider_check CHECK ((provider = ANY (ARRAY['github'::text, 'google'::text, 'linuxdo'::text])))
);



ALTER TABLE accounts.site_oauth_providers OWNER TO axiom_owner;

CREATE TABLE accounts.site_settings (
    site_id uuid NOT NULL,
    console_idle_timeout interval,
    console_max_age interval,
    admin_idle_timeout interval,
    admin_max_age interval,
    new_key_daily_limit bigint,
    ip_attempt_burst integer,
    ip_attempts_per_minute integer,
    email_sign_in_failures integer,
    user_code_failures integer,
    challenge_code_attempts integer,
    max_keys_per_account integer,
    key_creations_per_hour integer,
    registration_human_check boolean,
    registration_human_check_bits integer,
    registration_domain_burst integer,
    registration_domain_per_hour integer,
    registration_domain_allow text[],
    registration_domain_deny text[],
    passkeys_enabled boolean,
    model_prices_public boolean,
    max_workspaces_per_account integer,
    CONSTRAINT site_settings_admin_idle_timeout_check CHECK (((admin_idle_timeout >= '00:05:00'::interval) AND (admin_idle_timeout <= '12:00:00'::interval))),
    CONSTRAINT site_settings_admin_max_age_check CHECK (((admin_max_age >= '00:05:00'::interval) AND (admin_max_age <= '1 day'::interval))),
    CONSTRAINT site_settings_challenge_code_attempts_check CHECK (((challenge_code_attempts >= 1) AND (challenge_code_attempts <= 100))),
    CONSTRAINT site_settings_console_idle_timeout_check CHECK (((console_idle_timeout >= '00:05:00'::interval) AND (console_idle_timeout <= '30 days'::interval))),
    CONSTRAINT site_settings_console_max_age_check CHECK (((console_max_age >= '00:05:00'::interval) AND (console_max_age <= '90 days'::interval))),
    CONSTRAINT site_settings_email_sign_in_failures_check CHECK (((email_sign_in_failures >= 1) AND (email_sign_in_failures <= 1000))),
    CONSTRAINT site_settings_ip_attempt_burst_check CHECK (((ip_attempt_burst >= 1) AND (ip_attempt_burst <= 10000))),
    CONSTRAINT site_settings_ip_attempts_per_minute_check CHECK (((ip_attempts_per_minute >= 1) AND (ip_attempts_per_minute <= 10000))),
    CONSTRAINT site_settings_key_creations_per_hour_check CHECK (((key_creations_per_hour >= 1) AND (key_creations_per_hour <= 10000))),
    CONSTRAINT site_settings_max_keys_per_account_check CHECK (((max_keys_per_account >= 1) AND (max_keys_per_account <= 10000))),
    CONSTRAINT site_settings_max_workspaces_per_account_check CHECK (((max_workspaces_per_account >= 1) AND (max_workspaces_per_account <= 1000))),
    CONSTRAINT site_settings_new_key_daily_limit_check CHECK (((new_key_daily_limit >= 0) AND (new_key_daily_limit <= '1000000000000000'::bigint))),
    CONSTRAINT site_settings_registration_domain_allow_check CHECK (((registration_domain_allow IS NULL) OR (cardinality(registration_domain_allow) <= 100))),
    CONSTRAINT site_settings_registration_domain_burst_check CHECK (((registration_domain_burst >= 1) AND (registration_domain_burst <= 10000))),
    CONSTRAINT site_settings_registration_domain_deny_check CHECK (((registration_domain_deny IS NULL) OR (cardinality(registration_domain_deny) <= 100))),
    CONSTRAINT site_settings_registration_domain_per_hour_check CHECK (((registration_domain_per_hour >= 1) AND (registration_domain_per_hour <= 100000))),
    CONSTRAINT site_settings_registration_human_check_bits_check CHECK (((registration_human_check_bits >= 12) AND (registration_human_check_bits <= 24))),
    CONSTRAINT site_settings_user_code_failures_check CHECK (((user_code_failures >= 1) AND (user_code_failures <= 1000)))
);



ALTER TABLE accounts.site_settings OWNER TO axiom_owner;

CREATE TABLE accounts.tiers (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    name text NOT NULL,
    rate_requests integer,
    CONSTRAINT tiers_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT tiers_rate_requests_check CHECK (((rate_requests >= 0) AND (rate_requests <= 1000000000)))
);



ALTER TABLE accounts.tiers OWNER TO axiom_owner;

CREATE TABLE accounts.user_identities (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    subject text NOT NULL,
    display_name text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT user_identities_display_name_check CHECK (((char_length(display_name) >= 1) AND (char_length(display_name) <= 100))),
    CONSTRAINT user_identities_provider_check CHECK ((provider = ANY (ARRAY['github'::text, 'google'::text, 'linuxdo'::text]))),
    CONSTRAINT user_identities_subject_check CHECK (((char_length(subject) >= 1) AND (char_length(subject) <= 255)))
);



ALTER TABLE accounts.user_identities OWNER TO axiom_owner;

CREATE TABLE accounts.user_notices (
    id uuid DEFAULT uuidv7() NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    source text NOT NULL,
    kind text NOT NULL,
    value bigint NOT NULL,
    at timestamptz DEFAULT clock_timestamp() NOT NULL,
    email_done boolean DEFAULT false NOT NULL,
    email_failed boolean DEFAULT false NOT NULL,
    dispatched boolean DEFAULT false NOT NULL,
    webhook_url text DEFAULT ''::text NOT NULL,
    webhook_state text DEFAULT 'none'::text NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    CONSTRAINT user_notices_kind_check CHECK ((kind = ANY (ARRAY['low_balance'::text, 'top_up'::text, 'factor_changed'::text, 'spend_anomaly'::text, 'site_payment_changed'::text]))),
    CONSTRAINT user_notices_webhook_state_check CHECK ((webhook_state = ANY (ARRAY['none'::text, 'queued'::text, 'delivered'::text, 'failed'::text])))
);



ALTER TABLE accounts.user_notices OWNER TO axiom_owner;

CREATE TABLE accounts.users (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    account_kind text GENERATED ALWAYS AS ('personal'::text) STORED NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    factor_version bigint NOT NULL,
    status text NOT NULL,
    invite_code text NOT NULL,
    inviter_user_id uuid,
    totp_ciphertext bytea,
    totp_nonce bytea,
    totp_wrapped_dek bytea,
    totp_key_version bigint,
    totp_last_step bigint DEFAULT 0 NOT NULL,
    mfa_enabled_at timestamptz,
    email_verified_at timestamptz,
    CONSTRAINT users_check CHECK ((inviter_user_id <> id)),
    CONSTRAINT users_check1 CHECK ((num_nulls(totp_ciphertext, totp_nonce, totp_wrapped_dek, totp_key_version) = ANY (ARRAY[0, 4]))),
    CONSTRAINT users_email_check CHECK (((email = lower(email)) AND ((char_length(email) >= 3) AND (char_length(email) <= 254)) AND (email ~~ '%_@_%'::text))),
    CONSTRAINT users_factor_version_check CHECK ((factor_version >= 1)),
    CONSTRAINT users_invite_code_check CHECK ((invite_code ~ '^[a-z0-9]{6,32}$'::text)),
    CONSTRAINT users_password_hash_check CHECK ((password_hash ~~ '$argon2id$%'::text)),
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text]))),
    CONSTRAINT users_totp_key_version_check CHECK (((totp_key_version >= 1) AND (totp_key_version <= '4294967295'::bigint))),
    CONSTRAINT users_totp_last_step_check CHECK ((totp_last_step >= 0)),
    CONSTRAINT users_totp_nonce_check CHECK ((octet_length(totp_nonce) = 12)),
    CONSTRAINT users_totp_wrapped_dek_check CHECK ((octet_length(totp_wrapped_dek) = 60))
);



ALTER TABLE accounts.users OWNER TO axiom_owner;

CREATE TABLE accounts.webauthn_flows (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    kind text NOT NULL,
    account_id uuid,
    user_id uuid,
    session_id uuid,
    challenge text NOT NULL,
    relying_party_id text NOT NULL,
    origin text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    allowed_credential_ids bytea[] DEFAULT '{}'::bytea[] NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CONSTRAINT webauthn_flows_challenge_check CHECK (((char_length(challenge) >= 16) AND (char_length(challenge) <= 256))),
    CONSTRAINT webauthn_flows_check CHECK (((kind = 'sign_in'::text) = (user_id IS NULL))),
    CONSTRAINT webauthn_flows_check1 CHECK (((kind = 'sign_in'::text) = (account_id IS NULL))),
    CONSTRAINT webauthn_flows_check2 CHECK (((kind = 'sign_in'::text) = (session_id IS NULL))),
    CONSTRAINT webauthn_flows_kind_check CHECK ((kind = ANY (ARRAY['register'::text, 'sign_in'::text, 'mfa'::text]))),
    CONSTRAINT webauthn_flows_name_check CHECK ((char_length(name) <= 100)),
    CONSTRAINT webauthn_flows_origin_check CHECK (((char_length(origin) >= 1) AND (char_length(origin) <= 512))),
    CONSTRAINT webauthn_flows_relying_party_id_check CHECK (((char_length(relying_party_id) >= 1) AND (char_length(relying_party_id) <= 253)))
);



ALTER TABLE accounts.webauthn_flows OWNER TO axiom_owner;

CREATE TABLE accounts.workspaces (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    name text NOT NULL,
    status text NOT NULL,
    is_default boolean NOT NULL,
    budget bigint,
    budget_reset text,
    deleted_at timestamptz,
    CONSTRAINT default_kept CHECK ((NOT (is_default AND (deleted_at IS NOT NULL)))),
    CONSTRAINT workspaces_budget_check CHECK ((budget >= 0)),
    CONSTRAINT workspaces_check CHECK (((budget_reset IS NULL) OR ((budget_reset = ANY (ARRAY['daily'::text, 'weekly'::text, 'monthly'::text])) AND (budget IS NOT NULL)))),
    CONSTRAINT workspaces_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT workspaces_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);



ALTER TABLE accounts.workspaces OWNER TO axiom_owner;

INSERT INTO accounts.roles (id, name, permissions, builtin, additional_permissions) VALUES (uuidv7(), 'owner', '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view}', true, '{balance.top_up}');
INSERT INTO accounts.roles (id, name, permissions, builtin, additional_permissions) VALUES (uuidv7(), 'admin', '{api_keys.view,api_keys.manage,workspaces.manage,calls.view,balance.view,sites.manage,platform_grants.manage,users.view,credits.grant,suppliers.view,suppliers.manage,catalog.view,catalog.manage,pricing.view,pricing.manage}', true, '{balance.top_up,users.manage,calls.search,calls.inspect,audit.view,operations.manage,operations.approve}');
INSERT INTO accounts.roles (id, name, permissions, builtin, additional_permissions) VALUES (uuidv7(), 'member', '{api_keys.view,api_keys.manage,calls.view}', true, '{}');
INSERT INTO accounts.roles (id, name, permissions, builtin, additional_permissions) VALUES (uuidv7(), 'finance', '{api_keys.view,balance.view,users.view}', true, '{balance.top_up}');
INSERT INTO accounts.roles (id, name, permissions, builtin, additional_permissions) VALUES (uuidv7(), 'viewer', '{api_keys.view,calls.view,balance.view,users.view,suppliers.view,catalog.view,pricing.view}', true, '{}');
INSERT INTO accounts.roles (id, name, permissions, builtin, additional_permissions) VALUES (uuidv7(), 'support', '{api_keys.view,calls.view,users.view}', true, '{calls.search}');

ALTER TABLE ONLY accounts.accounts
    ADD CONSTRAINT accounts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.accounts
    ADD CONSTRAINT accounts_site_id_id_key UNIQUE (site_id, id);

ALTER TABLE ONLY accounts.accounts
    ADD CONSTRAINT accounts_site_id_id_kind_key UNIQUE (site_id, id, kind);

ALTER TABLE ONLY accounts.agent_credential_tokens
    ADD CONSTRAINT agent_credential_tokens_pkey PRIMARY KEY (secret_hash);

ALTER TABLE ONLY accounts.agent_credentials
    ADD CONSTRAINT agent_credentials_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.announcements
    ADD CONSTRAINT announcements_pkey PRIMARY KEY (id);

ALTER TABLE accounts.api_keys
    ADD CONSTRAINT api_keys_management_requests_check CHECK (((management_requests >= 0) AND (management_requests <= 61))) NOT VALID;

ALTER TABLE ONLY accounts.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.api_keys
    ADD CONSTRAINT api_keys_secret_hash_key UNIQUE (secret_hash);

ALTER TABLE ONLY accounts.email_changes
    ADD CONSTRAINT email_changes_pkey PRIMARY KEY (user_id);

ALTER TABLE ONLY accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_user_id_kind_version_key UNIQUE (user_id, kind, version);

ALTER TABLE ONLY accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_user_kind_locale_version_key UNIQUE (user_id, kind, locale, version);

ALTER TABLE ONLY accounts.legal_documents
    ADD CONSTRAINT legal_documents_locale_key UNIQUE (site_id, kind, locale, version);

ALTER TABLE ONLY accounts.legal_documents
    ADD CONSTRAINT legal_documents_pkey PRIMARY KEY (site_id, kind, version);

ALTER TABLE ONLY accounts.members
    ADD CONSTRAINT members_pkey PRIMARY KEY (account_id, user_id);

ALTER TABLE ONLY accounts.notification_preferences
    ADD CONSTRAINT notification_preferences_pkey PRIMARY KEY (user_id);

ALTER TABLE ONLY accounts.oauth_flows
    ADD CONSTRAINT oauth_flows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.oauth_flows
    ADD CONSTRAINT oauth_flows_state_hash_key UNIQUE (state_hash);

ALTER TABLE ONLY accounts.passkey_credentials
    ADD CONSTRAINT passkey_credentials_credential_id_key UNIQUE (credential_id);

ALTER TABLE ONLY accounts.passkey_credentials
    ADD CONSTRAINT passkey_credentials_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.password_reset_requests
    ADD CONSTRAINT password_reset_requests_id_key UNIQUE (id);

ALTER TABLE ONLY accounts.password_reset_requests
    ADD CONSTRAINT password_reset_requests_pkey PRIMARY KEY (site_id, email_hash);

ALTER TABLE ONLY accounts.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY accounts.platform_grants
    ADD CONSTRAINT platform_grants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.presets
    ADD CONSTRAINT presets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.recovery_codes
    ADD CONSTRAINT recovery_codes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.recovery_codes
    ADD CONSTRAINT recovery_codes_user_id_code_hash_key UNIQUE (user_id, code_hash);

ALTER TABLE ONLY accounts.registration_requests
    ADD CONSTRAINT registration_requests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.registration_requests
    ADD CONSTRAINT registration_requests_site_id_email_hash_key UNIQUE (site_id, email_hash);

ALTER TABLE ONLY accounts.registration_requests
    ADD CONSTRAINT registration_requests_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY accounts.roles
    ADD CONSTRAINT roles_name_key UNIQUE (name);

ALTER TABLE ONLY accounts.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.security_failures
    ADD CONSTRAINT security_failures_pkey PRIMARY KEY (site_id);

ALTER TABLE ONLY accounts.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.sessions
    ADD CONSTRAINT sessions_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY accounts.site_billing_preferences
    ADD CONSTRAINT site_billing_preferences_pkey PRIMARY KEY (site_id);

ALTER TABLE ONLY accounts.site_domains
    ADD CONSTRAINT site_domains_pkey PRIMARY KEY (domain);

ALTER TABLE ONLY accounts.site_domains
    ADD CONSTRAINT site_domains_site_domain_key UNIQUE (site_id, domain);

ALTER TABLE ONLY accounts.site_logos
    ADD CONSTRAINT site_logos_pkey PRIMARY KEY (site_id, hash);

ALTER TABLE ONLY accounts.site_oauth_providers
    ADD CONSTRAINT site_oauth_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.site_oauth_providers
    ADD CONSTRAINT site_oauth_providers_site_id_provider_key UNIQUE (site_id, provider);

ALTER TABLE ONLY accounts.site_settings
    ADD CONSTRAINT site_settings_pkey PRIMARY KEY (site_id);

ALTER TABLE ONLY accounts.sites
    ADD CONSTRAINT sites_id_is_main_key UNIQUE (id, is_main);

ALTER TABLE ONLY accounts.sites
    ADD CONSTRAINT sites_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.tiers
    ADD CONSTRAINT tiers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.tiers
    ADD CONSTRAINT tiers_site_id_id_key UNIQUE (site_id, id);

ALTER TABLE ONLY accounts.user_identities
    ADD CONSTRAINT user_identities_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.user_identities
    ADD CONSTRAINT user_identities_site_id_provider_subject_key UNIQUE (site_id, provider, subject);

ALTER TABLE ONLY accounts.user_identities
    ADD CONSTRAINT user_identities_user_id_provider_key UNIQUE (user_id, provider);

ALTER TABLE ONLY accounts.user_notices
    ADD CONSTRAINT user_notices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.user_notices
    ADD CONSTRAINT user_notices_user_id_kind_source_key UNIQUE (user_id, kind, source);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_account_id_key UNIQUE (account_id);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_site_id_account_id_id_key UNIQUE (site_id, account_id, id);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_site_id_email_key UNIQUE (site_id, email);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_site_id_id_key UNIQUE (site_id, id);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_site_id_invite_code_key UNIQUE (site_id, invite_code);

ALTER TABLE ONLY accounts.webauthn_flows
    ADD CONSTRAINT webauthn_flows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.workspaces
    ADD CONSTRAINT workspaces_pkey PRIMARY KEY (id);

ALTER TABLE ONLY accounts.workspaces
    ADD CONSTRAINT workspaces_site_id_account_id_id_key UNIQUE (site_id, account_id, id);

CREATE UNIQUE INDEX agent_credential_tokens_current ON accounts.agent_credential_tokens USING btree (credential_id) WHERE (retires_at IS NULL);

CREATE INDEX announcements_active ON accounts.announcements USING btree (site_id, ends_at, starts_at) WHERE (withdrawn_at IS NULL);

CREATE INDEX api_keys_by_preset ON accounts.api_keys USING btree (preset_id, account_id) INCLUDE (site_id) WHERE ((deleted_at IS NULL) AND (preset_id <> '00000000-0000-0000-0000-000000000000'::uuid));

CREATE INDEX api_keys_created_by_account ON accounts.api_keys USING btree (account_id, created_at);

CREATE INDEX api_keys_live_of_account ON accounts.api_keys USING btree (account_id, id DESC) WHERE (deleted_at IS NULL);

CREATE INDEX api_keys_made_by_account ON accounts.api_keys USING btree (account_id, created_at) WHERE (kind = 'call'::text);

CREATE INDEX api_keys_of_account ON accounts.api_keys USING btree (account_id, id DESC) WHERE ((kind = 'call'::text) AND (deleted_at IS NULL));

CREATE INDEX email_changes_expiry ON accounts.email_changes USING btree (expires_at);

CREATE INDEX oauth_flows_expiry ON accounts.oauth_flows USING btree (expires_at);

CREATE UNIQUE INDEX one_default_workspace ON accounts.workspaces USING btree (account_id) WHERE is_default;

CREATE UNIQUE INDEX one_main_site ON accounts.sites USING btree (is_main) WHERE is_main;

CREATE INDEX passkey_credentials_user ON accounts.passkey_credentials USING btree (user_id);

CREATE INDEX password_reset_requests_expiry ON accounts.password_reset_requests USING btree (expires_at);

CREATE INDEX password_reset_tokens_expiry ON accounts.password_reset_tokens USING btree (expires_at);

CREATE UNIQUE INDEX platform_grants_held ON accounts.platform_grants USING btree (user_id, role_id) WHERE (revoked_at IS NULL);

CREATE UNIQUE INDEX presets_live_slug ON accounts.presets USING btree (account_id, slug) WHERE (deleted_at IS NULL);

CREATE INDEX presets_page ON accounts.presets USING btree (account_id, updated_at DESC, id DESC) WHERE (deleted_at IS NULL);

CREATE INDEX registration_requests_expiry ON accounts.registration_requests USING btree (expires_at);

CREATE INDEX security_failures_expiry ON accounts.security_failures USING btree (started_at, site_id) WHERE ((failures > 0) OR (completed_peak > 0) OR reported);

CREATE INDEX sessions_live_of_user ON accounts.sessions USING btree (user_id, surface, created_at DESC) WHERE (revoked_at IS NULL);

CREATE INDEX user_notices_pending ON accounts.user_notices USING btree (id) WHERE (NOT dispatched);

CREATE INDEX user_notices_retention ON accounts.user_notices USING btree (at) WHERE (dispatched AND (webhook_state <> 'queued'::text));

CREATE INDEX users_email_prefix ON accounts.users USING btree (email text_pattern_ops);

CREATE INDEX users_of_inviter ON accounts.users USING btree (site_id, inviter_user_id, id DESC) WHERE (inviter_user_id IS NOT NULL);

CREATE INDEX webauthn_flows_expiry ON accounts.webauthn_flows USING btree (expires_at);

CREATE UNIQUE INDEX workspace_names ON accounts.workspaces USING btree (account_id, lower(name)) WHERE (deleted_at IS NULL);

CREATE TRIGGER clear_session_recovery_codes BEFORE UPDATE OF revoked_at ON accounts.sessions FOR EACH ROW WHEN ((new.revoked_at IS NOT NULL)) EXECUTE FUNCTION accounts.clear_session_recovery_codes();

CREATE TRIGGER factor_notice AFTER UPDATE OF factor_version ON accounts.users FOR EACH ROW WHEN ((new.factor_version <> old.factor_version)) EXECUTE FUNCTION accounts.factor_notice();

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF client_secret_key_version ON accounts.site_oauth_providers FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('client_secret_key_version');

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF totp_key_version ON accounts.users FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('totp_key_version');

CREATE TRIGGER legal_acceptance_locale BEFORE INSERT ON accounts.legal_acceptances FOR EACH ROW EXECUTE FUNCTION accounts.set_legal_acceptance_locale();

CREATE TRIGGER refuse_console_password_with_passkey BEFORE INSERT ON accounts.sessions FOR EACH ROW EXECUTE FUNCTION accounts.refuse_console_password_with_passkey();

CREATE TRIGGER refuse_totp_with_unproved_passkey BEFORE UPDATE ON accounts.users FOR EACH ROW EXECUTE FUNCTION accounts.refuse_totp_with_unproved_passkey();

ALTER TABLE ONLY accounts.accounts
    ADD CONSTRAINT accounts_site_id_tier_id_fkey FOREIGN KEY (site_id, tier_id) REFERENCES accounts.tiers(site_id, id);

ALTER TABLE ONLY accounts.agent_credential_tokens
    ADD CONSTRAINT agent_credential_tokens_credential_id_fkey FOREIGN KEY (credential_id) REFERENCES accounts.agent_credentials(id);

ALTER TABLE ONLY accounts.announcements
    ADD CONSTRAINT announcements_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.api_keys
    ADD CONSTRAINT api_keys_site_id_account_id_workspace_id_fkey FOREIGN KEY (site_id, account_id, workspace_id) REFERENCES accounts.workspaces(site_id, account_id, id);

ALTER TABLE ONLY accounts.api_keys
    ADD CONSTRAINT api_keys_site_id_created_by_fkey FOREIGN KEY (site_id, created_by) REFERENCES accounts.users(site_id, id);

ALTER TABLE ONLY accounts.email_changes
    ADD CONSTRAINT email_changes_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_document_locale_fkey FOREIGN KEY (site_id, kind, locale, version) REFERENCES accounts.legal_documents(site_id, kind, locale, version);

ALTER TABLE ONLY accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_site_id_kind_version_fkey FOREIGN KEY (site_id, kind, version) REFERENCES accounts.legal_documents(site_id, kind, version);

ALTER TABLE ONLY accounts.legal_documents
    ADD CONSTRAINT legal_documents_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.members
    ADD CONSTRAINT members_role_id_fkey FOREIGN KEY (role_id) REFERENCES accounts.roles(id);

ALTER TABLE ONLY accounts.members
    ADD CONSTRAINT members_site_id_account_id_account_kind_fkey FOREIGN KEY (site_id, account_id, account_kind) REFERENCES accounts.accounts(site_id, id, kind);

ALTER TABLE ONLY accounts.members
    ADD CONSTRAINT members_site_id_personal_owner_user_id_fkey FOREIGN KEY (site_id, personal_owner, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.members
    ADD CONSTRAINT members_site_id_user_account_id_user_id_fkey FOREIGN KEY (site_id, user_account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.notification_preferences
    ADD CONSTRAINT notification_preferences_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.oauth_flows
    ADD CONSTRAINT oauth_flows_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.oauth_flows
    ADD CONSTRAINT oauth_flows_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.passkey_credentials
    ADD CONSTRAINT passkey_credentials_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.password_reset_requests
    ADD CONSTRAINT password_reset_requests_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.platform_grants
    ADD CONSTRAINT platform_grants_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES accounts.users(id);

ALTER TABLE ONLY accounts.platform_grants
    ADD CONSTRAINT platform_grants_role_id_fkey FOREIGN KEY (role_id) REFERENCES accounts.roles(id);

ALTER TABLE ONLY accounts.platform_grants
    ADD CONSTRAINT platform_grants_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.platform_grants
    ADD CONSTRAINT platform_grants_site_id_site_is_main_fkey FOREIGN KEY (site_id, site_is_main) REFERENCES accounts.sites(id, is_main);

ALTER TABLE ONLY accounts.presets
    ADD CONSTRAINT presets_site_id_account_id_fkey FOREIGN KEY (site_id, account_id) REFERENCES accounts.accounts(site_id, id);

ALTER TABLE ONLY accounts.recovery_codes
    ADD CONSTRAINT recovery_codes_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.registration_requests
    ADD CONSTRAINT registration_requests_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.registration_requests
    ADD CONSTRAINT registration_requests_site_id_tier_id_fkey FOREIGN KEY (site_id, tier_id) REFERENCES accounts.tiers(site_id, id);

ALTER TABLE ONLY accounts.security_failures
    ADD CONSTRAINT security_failures_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.sessions
    ADD CONSTRAINT sessions_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.site_billing_preferences
    ADD CONSTRAINT site_billing_preferences_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.site_domains
    ADD CONSTRAINT site_domains_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.site_logos
    ADD CONSTRAINT site_logos_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.site_oauth_providers
    ADD CONSTRAINT site_oauth_providers_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.site_settings
    ADD CONSTRAINT site_settings_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.sites
    ADD CONSTRAINT sites_id_default_tier_id_fkey FOREIGN KEY (id, default_tier_id) REFERENCES accounts.tiers(site_id, id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE ONLY accounts.sites
    ADD CONSTRAINT sites_login_domain_fk FOREIGN KEY (id, login_domain) REFERENCES accounts.site_domains(site_id, domain);

ALTER TABLE ONLY accounts.sites
    ADD CONSTRAINT sites_logo FOREIGN KEY (id, logo_hash) REFERENCES accounts.site_logos(site_id, hash);

ALTER TABLE ONLY accounts.sites
    ADD CONSTRAINT sites_parent_site_id_owner_account_id_fkey FOREIGN KEY (parent_site_id, owner_account_id) REFERENCES accounts.accounts(site_id, id);

ALTER TABLE ONLY accounts.sites
    ADD CONSTRAINT sites_parent_site_id_parent_is_main_fkey FOREIGN KEY (parent_site_id, parent_is_main) REFERENCES accounts.sites(id, is_main);

ALTER TABLE ONLY accounts.tiers
    ADD CONSTRAINT tiers_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.user_identities
    ADD CONSTRAINT user_identities_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.user_notices
    ADD CONSTRAINT user_notices_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_site_id_account_id_account_kind_fkey FOREIGN KEY (site_id, account_id, account_kind) REFERENCES accounts.accounts(site_id, id, kind);

ALTER TABLE ONLY accounts.users
    ADD CONSTRAINT users_site_id_inviter_user_id_fkey FOREIGN KEY (site_id, inviter_user_id) REFERENCES accounts.users(site_id, id);

ALTER TABLE ONLY accounts.webauthn_flows
    ADD CONSTRAINT webauthn_flows_site_id_account_id_user_id_fkey FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users(site_id, account_id, id);

ALTER TABLE ONLY accounts.webauthn_flows
    ADD CONSTRAINT webauthn_flows_site_id_fkey FOREIGN KEY (site_id) REFERENCES accounts.sites(id);

ALTER TABLE ONLY accounts.workspaces
    ADD CONSTRAINT workspaces_site_id_account_id_fkey FOREIGN KEY (site_id, account_id) REFERENCES accounts.accounts(site_id, id);






CREATE POLICY deletes ON accounts.email_changes FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY deletes ON accounts.oauth_flows FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY deletes ON accounts.passkey_credentials FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY deletes ON accounts.registration_requests FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY deletes ON accounts.site_billing_preferences FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY deletes ON accounts.site_oauth_providers FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY deletes ON accounts.user_identities FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY deletes ON accounts.webauthn_flows FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));


CREATE POLICY expiry ON accounts.user_notices FOR DELETE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) AND dispatched AND (webhook_state <> 'queued'::text) AND (at < (statement_timestamp() - '30 days'::interval))));

CREATE POLICY inserts ON accounts.accounts FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.agent_credential_tokens FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON accounts.agent_credentials FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON accounts.announcements FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON accounts.api_keys FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.email_changes FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.legal_acceptances FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.members FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.notification_preferences FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.oauth_flows FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.passkey_credentials FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.platform_grants FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON accounts.presets FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.recovery_codes FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.registration_requests FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.roles FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY inserts ON accounts.sessions FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.site_billing_preferences FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.site_domains FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.site_logos FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.site_oauth_providers FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.site_settings FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.sites FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.tiers FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.user_identities FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.user_notices FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.users FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY inserts ON accounts.webauthn_flows FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY inserts ON accounts.workspaces FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));



CREATE POLICY lookup ON accounts.api_keys FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.passkey_credentials FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.password_reset_requests FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.password_reset_tokens FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.registration_requests FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.sessions FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.site_domains FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.sites FOR SELECT TO axiom_lookup USING (true);

CREATE POLICY lookup ON accounts.users FOR SELECT TO axiom_lookup USING (true);








CREATE POLICY platform_insert ON accounts.legal_documents FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_insert ON accounts.security_failures FOR INSERT WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_update ON accounts.security_failures FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope));


CREATE POLICY public_read ON accounts.legal_documents FOR SELECT USING (( SELECT platform.in_any_scope() AS in_any_scope));

CREATE POLICY reads ON accounts.accounts FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.agent_credential_tokens FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON accounts.agent_credentials FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON accounts.announcements FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id = ( SELECT platform.home_site() AS home_site))));

CREATE POLICY reads ON accounts.api_keys FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.email_changes FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.legal_acceptances FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.members FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.notification_preferences FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.oauth_flows FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.passkey_credentials FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.platform_grants FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.presets FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.recovery_codes FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.registration_requests FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.roles FOR SELECT USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reads ON accounts.sessions FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.site_billing_preferences FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (site_id IN ( SELECT a.site_id
   FROM accounts.accounts a
  WHERE (a.id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))))));

CREATE POLICY reads ON accounts.site_domains FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.site_logos FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.site_oauth_providers FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.site_settings FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.sites FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.tiers FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (id = ANY (ARRAY( SELECT a.tier_id
   FROM accounts.accounts a
  WHERE (a.id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))))));

CREATE POLICY reads ON accounts.user_identities FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.user_notices FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.users FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY reads ON accounts.webauthn_flows FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY reads ON accounts.workspaces FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));



CREATE POLICY request_expiry ON accounts.password_reset_requests FOR DELETE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))) AND (expires_at <= clock_timestamp())));

CREATE POLICY request_insert ON accounts.password_reset_requests FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY request_select ON accounts.password_reset_requests FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY request_update ON accounts.password_reset_requests FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));










CREATE POLICY tenant_read ON accounts.security_failures FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));


CREATE POLICY token_expiry ON accounts.password_reset_tokens FOR DELETE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))) AND (expires_at <= clock_timestamp())));

CREATE POLICY token_insert ON accounts.password_reset_tokens FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY token_select ON accounts.password_reset_tokens FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY token_update ON accounts.password_reset_tokens FOR UPDATE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))) AND (used_at IS NULL))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.accounts FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.agent_credential_tokens FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON accounts.agent_credentials FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) AND (revoked_at IS NULL))) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON accounts.announcements FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) AND (withdrawn_at IS NULL))) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON accounts.api_keys FOR UPDATE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))) AND (deleted_at IS NULL))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.email_changes FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.notification_preferences FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.oauth_flows FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.passkey_credentials FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.platform_grants FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) AND (revoked_at IS NULL))) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON accounts.presets FOR UPDATE USING (((deleted_at IS NULL) AND (( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.recovery_codes FOR UPDATE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))) AND (used_at IS NULL))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.registration_requests FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.roles FOR UPDATE USING (( SELECT platform.in_platform_scope() AS in_platform_scope)) WITH CHECK (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY updates ON accounts.sessions FOR UPDATE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))) AND (revoked_at IS NULL))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.site_billing_preferences FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.site_domains FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.site_oauth_providers FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.site_settings FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.sites FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.tiers FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.user_notices FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.users FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY updates ON accounts.webauthn_flows FOR UPDATE USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[]))));

CREATE POLICY updates ON accounts.workspaces FOR UPDATE USING (((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))) AND (deleted_at IS NULL))) WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));






GRANT USAGE ON SCHEMA accounts TO axiom_app;
GRANT USAGE ON SCHEMA accounts TO axiom_lookup;

GRANT SELECT,INSERT ON TABLE accounts.site_domains TO axiom_app;

GRANT SELECT(domain) ON TABLE accounts.site_domains TO axiom_lookup;

GRANT SELECT(site_id) ON TABLE accounts.site_domains TO axiom_lookup;

GRANT SELECT(surface) ON TABLE accounts.site_domains TO axiom_lookup;

GRANT UPDATE(disabled_at) ON TABLE accounts.site_domains TO axiom_app;
GRANT SELECT(disabled_at) ON TABLE accounts.site_domains TO axiom_lookup;

GRANT SELECT,INSERT ON TABLE accounts.sites TO axiom_app;

GRANT SELECT(id) ON TABLE accounts.sites TO axiom_lookup;

GRANT UPDATE(name) ON TABLE accounts.sites TO axiom_app;

GRANT UPDATE(status) ON TABLE accounts.sites TO axiom_app;
GRANT SELECT(status) ON TABLE accounts.sites TO axiom_lookup;

GRANT UPDATE(key_prefix) ON TABLE accounts.sites TO axiom_app;

GRANT UPDATE(registration) ON TABLE accounts.sites TO axiom_app;
GRANT SELECT(registration) ON TABLE accounts.sites TO axiom_lookup;

GRANT UPDATE(default_tier_id) ON TABLE accounts.sites TO axiom_app;
GRANT SELECT(default_tier_id) ON TABLE accounts.sites TO axiom_lookup;

GRANT UPDATE(logo_hash) ON TABLE accounts.sites TO axiom_app;

GRANT UPDATE(brand_color) ON TABLE accounts.sites TO axiom_app;

GRANT UPDATE(login_domain) ON TABLE accounts.sites TO axiom_app;

REVOKE ALL ON FUNCTION accounts.domain_site(domain text, surface text) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.domain_site(domain text, surface text) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.invite_tenant(domain text, code text) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.invite_tenant(domain text, code text) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.key_tenant(site uuid, hashes bytea[]) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.key_tenant(site uuid, hashes bytea[]) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.login_tenant(domain text, email text) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.login_tenant(domain text, email text) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.passkey_tenant(credential_id bytea) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.passkey_tenant(credential_id bytea) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.password_reset_request_tenant(id uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.password_reset_request_tenant(id uuid) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.password_reset_tenant(domain text, token_hash bytea) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.password_reset_tenant(domain text, token_hash bytea) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.registration_request_tenant(id uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.registration_request_tenant(id uuid) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.registration_site(domain text) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.registration_site(domain text) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.registration_tenant(domain text, token_hash bytea) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.registration_tenant(domain text, token_hash bytea) TO axiom_app;

REVOKE ALL ON FUNCTION accounts.session_tenant(domain text, surface text, token_hash bytea) FROM PUBLIC;
GRANT ALL ON FUNCTION accounts.session_tenant(domain text, surface text, token_hash bytea) TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(name) ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(status) ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(tier_id) ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(max_keys) ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(key_creations_per_hour) ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(rate_requests) ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(billing_preference) ON TABLE accounts.accounts TO axiom_app;

GRANT UPDATE(max_workspaces) ON TABLE accounts.accounts TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.agent_credential_tokens TO axiom_app;

GRANT UPDATE(retires_at) ON TABLE accounts.agent_credential_tokens TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.agent_credentials TO axiom_app;

GRANT UPDATE(revoked_at) ON TABLE accounts.agent_credentials TO axiom_app;

GRANT UPDATE(last_used_at) ON TABLE accounts.agent_credentials TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.announcements TO axiom_app;

GRANT UPDATE(withdrawn_at) ON TABLE accounts.announcements TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.api_keys TO axiom_app;

GRANT SELECT(id) ON TABLE accounts.api_keys TO axiom_lookup;

GRANT SELECT(site_id) ON TABLE accounts.api_keys TO axiom_lookup;

GRANT SELECT(account_id) ON TABLE accounts.api_keys TO axiom_lookup;

GRANT SELECT(workspace_id) ON TABLE accounts.api_keys TO axiom_lookup;

GRANT UPDATE(name) ON TABLE accounts.api_keys TO axiom_app;

GRANT SELECT(secret_hash) ON TABLE accounts.api_keys TO axiom_lookup;

GRANT UPDATE(status) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(spend_limit) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(limit_reset) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(preset_id) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(models) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(ip_allow) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(deleted_at) ON TABLE accounts.api_keys TO axiom_app;
GRANT SELECT(deleted_at) ON TABLE accounts.api_keys TO axiom_lookup;

GRANT UPDATE(updated_at) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(management_window) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(management_requests) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(rate_requests) ON TABLE accounts.api_keys TO axiom_app;

GRANT UPDATE(last_used_at) ON TABLE accounts.api_keys TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.email_changes TO axiom_app;

GRANT UPDATE(email) ON TABLE accounts.email_changes TO axiom_app;

GRANT UPDATE(token_hash) ON TABLE accounts.email_changes TO axiom_app;

GRANT UPDATE(factor_version) ON TABLE accounts.email_changes TO axiom_app;

GRANT UPDATE(language) ON TABLE accounts.email_changes TO axiom_app;

GRANT UPDATE(expires_at) ON TABLE accounts.email_changes TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.legal_acceptances TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.legal_documents TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.members TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.notification_preferences TO axiom_app;

GRANT UPDATE(low_balance) ON TABLE accounts.notification_preferences TO axiom_app;

GRANT UPDATE(top_up) ON TABLE accounts.notification_preferences TO axiom_app;

GRANT UPDATE(factor_changed) ON TABLE accounts.notification_preferences TO axiom_app;

GRANT UPDATE(threshold) ON TABLE accounts.notification_preferences TO axiom_app;

GRANT UPDATE(webhook_url) ON TABLE accounts.notification_preferences TO axiom_app;

GRANT UPDATE(language) ON TABLE accounts.notification_preferences TO axiom_app;

GRANT UPDATE(spend_anomaly) ON TABLE accounts.notification_preferences TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.oauth_flows TO axiom_app;

GRANT UPDATE(consumed_at) ON TABLE accounts.oauth_flows TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.passkey_credentials TO axiom_app;

GRANT SELECT(site_id) ON TABLE accounts.passkey_credentials TO axiom_lookup;

GRANT SELECT(account_id) ON TABLE accounts.passkey_credentials TO axiom_lookup;

GRANT SELECT(user_id) ON TABLE accounts.passkey_credentials TO axiom_lookup;

GRANT SELECT(credential_id) ON TABLE accounts.passkey_credentials TO axiom_lookup;

GRANT UPDATE(sign_count) ON TABLE accounts.passkey_credentials TO axiom_app;

GRANT UPDATE(backup_state) ON TABLE accounts.passkey_credentials TO axiom_app;

GRANT UPDATE(name) ON TABLE accounts.passkey_credentials TO axiom_app;

GRANT UPDATE(last_used_at) ON TABLE accounts.passkey_credentials TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.password_reset_requests TO axiom_app;

GRANT SELECT(site_id) ON TABLE accounts.password_reset_requests TO axiom_lookup;

GRANT UPDATE(id) ON TABLE accounts.password_reset_requests TO axiom_app;
GRANT SELECT(id) ON TABLE accounts.password_reset_requests TO axiom_lookup;

GRANT UPDATE(email) ON TABLE accounts.password_reset_requests TO axiom_app;

GRANT UPDATE(domain) ON TABLE accounts.password_reset_requests TO axiom_app;

GRANT UPDATE(language) ON TABLE accounts.password_reset_requests TO axiom_app;

GRANT UPDATE(expires_at) ON TABLE accounts.password_reset_requests TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.password_reset_tokens TO axiom_app;

GRANT SELECT(id) ON TABLE accounts.password_reset_tokens TO axiom_lookup;

GRANT SELECT(site_id) ON TABLE accounts.password_reset_tokens TO axiom_lookup;

GRANT SELECT(account_id) ON TABLE accounts.password_reset_tokens TO axiom_lookup;

GRANT SELECT(user_id) ON TABLE accounts.password_reset_tokens TO axiom_lookup;

GRANT SELECT(token_hash) ON TABLE accounts.password_reset_tokens TO axiom_lookup;

GRANT UPDATE(used_at) ON TABLE accounts.password_reset_tokens TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.platform_grants TO axiom_app;

GRANT UPDATE(revoked_at) ON TABLE accounts.platform_grants TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.presets TO axiom_app;

GRANT UPDATE(name) ON TABLE accounts.presets TO axiom_app;

GRANT UPDATE(config) ON TABLE accounts.presets TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE accounts.presets TO axiom_app;

GRANT UPDATE(deleted_at) ON TABLE accounts.presets TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.recovery_codes TO axiom_app;

GRANT UPDATE(used_at) ON TABLE accounts.recovery_codes TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.registration_requests TO axiom_app;

GRANT SELECT(id) ON TABLE accounts.registration_requests TO axiom_lookup;

GRANT SELECT(site_id) ON TABLE accounts.registration_requests TO axiom_lookup;

GRANT SELECT(email) ON TABLE accounts.registration_requests TO axiom_lookup;

GRANT UPDATE(tier_id) ON TABLE accounts.registration_requests TO axiom_app;

GRANT UPDATE(domain) ON TABLE accounts.registration_requests TO axiom_app;

GRANT UPDATE(language) ON TABLE accounts.registration_requests TO axiom_app;

GRANT UPDATE(token_hash) ON TABLE accounts.registration_requests TO axiom_app;
GRANT SELECT(token_hash) ON TABLE accounts.registration_requests TO axiom_lookup;

GRANT UPDATE(expires_at) ON TABLE accounts.registration_requests TO axiom_app;
GRANT SELECT(expires_at) ON TABLE accounts.registration_requests TO axiom_lookup;

GRANT SELECT,INSERT ON TABLE accounts.roles TO axiom_app;

GRANT UPDATE(permissions) ON TABLE accounts.roles TO axiom_app;

GRANT UPDATE(additional_permissions) ON TABLE accounts.roles TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.security_failures TO axiom_app;

GRANT UPDATE(started_at) ON TABLE accounts.security_failures TO axiom_app;

GRANT UPDATE(failures) ON TABLE accounts.security_failures TO axiom_app;

GRANT UPDATE(completed_peak) ON TABLE accounts.security_failures TO axiom_app;

GRANT UPDATE(sampled_at) ON TABLE accounts.security_failures TO axiom_app;

GRANT UPDATE(reported) ON TABLE accounts.security_failures TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.sessions TO axiom_app;

GRANT SELECT(id) ON TABLE accounts.sessions TO axiom_lookup;

GRANT SELECT(site_id) ON TABLE accounts.sessions TO axiom_lookup;

GRANT SELECT(account_id) ON TABLE accounts.sessions TO axiom_lookup;

GRANT SELECT(user_id) ON TABLE accounts.sessions TO axiom_lookup;

GRANT SELECT(surface) ON TABLE accounts.sessions TO axiom_lookup;

GRANT SELECT(token_hash) ON TABLE accounts.sessions TO axiom_lookup;

GRANT UPDATE(mfa_attempts) ON TABLE accounts.sessions TO axiom_app;

GRANT UPDATE(last_seen_at) ON TABLE accounts.sessions TO axiom_app;

GRANT UPDATE(expires_at) ON TABLE accounts.sessions TO axiom_app;

GRANT UPDATE(idle_timeout) ON TABLE accounts.sessions TO axiom_app;

GRANT UPDATE(revoked_at) ON TABLE accounts.sessions TO axiom_app;

GRANT UPDATE(recovery_codes_ciphertext) ON TABLE accounts.sessions TO axiom_app;

GRANT UPDATE(recovery_codes_nonce) ON TABLE accounts.sessions TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.site_billing_preferences TO axiom_app;

GRANT UPDATE(preference) ON TABLE accounts.site_billing_preferences TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.site_logos TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.site_oauth_providers TO axiom_app;

GRANT UPDATE(client_id) ON TABLE accounts.site_oauth_providers TO axiom_app;

GRANT UPDATE(client_secret_ciphertext) ON TABLE accounts.site_oauth_providers TO axiom_app;

GRANT UPDATE(client_secret_nonce) ON TABLE accounts.site_oauth_providers TO axiom_app;

GRANT UPDATE(client_secret_wrapped_dek) ON TABLE accounts.site_oauth_providers TO axiom_app;

GRANT UPDATE(client_secret_key_version) ON TABLE accounts.site_oauth_providers TO axiom_app;

GRANT UPDATE(updated_at) ON TABLE accounts.site_oauth_providers TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(console_idle_timeout) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(console_max_age) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(admin_idle_timeout) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(admin_max_age) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(new_key_daily_limit) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(ip_attempt_burst) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(ip_attempts_per_minute) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(email_sign_in_failures) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(user_code_failures) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(challenge_code_attempts) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(max_keys_per_account) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(key_creations_per_hour) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(registration_human_check) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(registration_human_check_bits) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(registration_domain_burst) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(registration_domain_per_hour) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(registration_domain_allow) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(registration_domain_deny) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(passkeys_enabled) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(model_prices_public) ON TABLE accounts.site_settings TO axiom_app;

GRANT UPDATE(max_workspaces_per_account) ON TABLE accounts.site_settings TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.tiers TO axiom_app;

GRANT UPDATE(name) ON TABLE accounts.tiers TO axiom_app;

GRANT UPDATE(rate_requests) ON TABLE accounts.tiers TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.user_identities TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.user_notices TO axiom_app;

GRANT UPDATE(email_done) ON TABLE accounts.user_notices TO axiom_app;

GRANT UPDATE(email_failed) ON TABLE accounts.user_notices TO axiom_app;

GRANT UPDATE(dispatched) ON TABLE accounts.user_notices TO axiom_app;

GRANT UPDATE(webhook_url) ON TABLE accounts.user_notices TO axiom_app;

GRANT UPDATE(webhook_state) ON TABLE accounts.user_notices TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.users TO axiom_app;

GRANT SELECT(id) ON TABLE accounts.users TO axiom_lookup;

GRANT SELECT(site_id) ON TABLE accounts.users TO axiom_lookup;

GRANT SELECT(account_id) ON TABLE accounts.users TO axiom_lookup;

GRANT SELECT(email) ON TABLE accounts.users TO axiom_lookup;
GRANT UPDATE(email) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(password_hash) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(factor_version) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(status) ON TABLE accounts.users TO axiom_app;
GRANT SELECT(status) ON TABLE accounts.users TO axiom_lookup;

GRANT SELECT(invite_code) ON TABLE accounts.users TO axiom_lookup;

GRANT UPDATE(totp_ciphertext) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(totp_nonce) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(totp_wrapped_dek) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(totp_key_version) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(totp_last_step) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(mfa_enabled_at) ON TABLE accounts.users TO axiom_app;

GRANT UPDATE(email_verified_at) ON TABLE accounts.users TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE accounts.webauthn_flows TO axiom_app;

GRANT UPDATE(consumed_at) ON TABLE accounts.webauthn_flows TO axiom_app;

GRANT SELECT,INSERT ON TABLE accounts.workspaces TO axiom_app;

GRANT UPDATE(name) ON TABLE accounts.workspaces TO axiom_app;

GRANT UPDATE(status) ON TABLE accounts.workspaces TO axiom_app;

GRANT UPDATE(budget) ON TABLE accounts.workspaces TO axiom_app;

GRANT UPDATE(budget_reset) ON TABLE accounts.workspaces TO axiom_app;

GRANT UPDATE(deleted_at) ON TABLE accounts.workspaces TO axiom_app;

ALTER FUNCTION accounts.domain_site(domain text, surface text) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.invite_tenant(domain text, code text) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.key_tenant(site uuid, hashes bytea[]) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.login_tenant(domain text, email text) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.passkey_tenant(credential_id bytea) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.password_reset_request_tenant(id uuid) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.password_reset_tenant(domain text, token_hash bytea) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.registration_request_tenant(id uuid) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.registration_site(domain text) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.registration_tenant(domain text, token_hash bytea) OWNER TO axiom_lookup;
ALTER FUNCTION accounts.session_tenant(domain text, surface text, token_hash bytea) OWNER TO axiom_lookup;

REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;

ALTER TABLE ONLY accounts.site_domains FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.sites FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.agent_credential_tokens FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.agent_credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.announcements FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.api_keys FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.email_changes FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.legal_acceptances FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.legal_documents FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.members FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.notification_preferences FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.oauth_flows FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.passkey_credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.password_reset_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.password_reset_tokens FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.platform_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.presets FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.recovery_codes FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.registration_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.roles FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.security_failures FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.site_billing_preferences FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.site_logos FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.site_oauth_providers FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.site_settings FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.tiers FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.user_identities FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.user_notices FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.users FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.webauthn_flows FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY accounts.workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE accounts.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.agent_credential_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.agent_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.announcements ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.email_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.legal_acceptances ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.legal_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.members ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.oauth_flows ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.passkey_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.password_reset_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.password_reset_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.platform_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.presets ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.recovery_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.registration_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.security_failures ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.site_billing_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.site_domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.site_logos ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.site_oauth_providers ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.site_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.sites ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.tiers ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.user_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.user_notices ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.users ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.webauthn_flows ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.workspaces ENABLE ROW LEVEL SECURITY;
