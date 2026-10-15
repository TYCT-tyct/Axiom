-- +goose Up

-- The suppliers module's configuration: who we buy from, how we log in, the groups and models they sell, and the
-- endpoints we route to (internal/suppliers/README.md「表」). Sync state, rate buckets, cost schedules and the charge
-- and attempt tables come with the code that writes them.
--
-- Rules for every table here. Rows are never deleted by the application: it is granted no DELETE, and removing a
-- supplier is a status. UPDATE is granted per column, and leaves out what must not change once written: ownership,
-- identity and the address credentials are sent to. Child tables carry no owner of their own, only supplier_id, and
-- their policy asks whether the supplier is visible, so a row cannot be attached to a supplier the scope cannot see
-- and has no label that could disagree with its parent.

GRANT USAGE ON SCHEMA suppliers TO axiom_app;

CREATE TABLE suppliers.suppliers (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- Platform: (NULL, NULL). Site: (site, NULL). Account: (site, account).
    owner_scope text NOT NULL CHECK (owner_scope IN ('platform', 'site', 'account')),
    site_id uuid,
    account_id uuid,
    CONSTRAINT owner_matches_scope CHECK (
        (owner_scope = 'platform' AND site_id IS NULL AND account_id IS NULL)
        OR (owner_scope = 'site' AND site_id IS NOT NULL AND account_id IS NULL)
        OR (owner_scope = 'account' AND site_id IS NOT NULL AND account_id IS NOT NULL)),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    -- The adapter's name. Not an enum: removing an adapter must not need a migration. Startup refuses a live
    -- supplier whose kind no adapter is registered for.
    kind text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9]*$'),
    -- Where credentials are sent: https://host[:port], lower case. Fixed at connection, so a changed address cannot
    -- carry old credentials to a new host.
    origin text NOT NULL CHECK (origin ~ '^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$'),
    -- The username or email we log in as: not a secret, and half of the supplier's identity.
    upstream_account text NOT NULL CHECK (upstream_account <> ''),
    generation text CHECK (generation <> ''),
    detected_version text CHECK (detected_version <> ''),
    native_unit text NOT NULL CHECK (native_unit IN ('quota', 'usd')),
    settlement_currency text NOT NULL CHECK (settlement_currency ~ '^[A-Z]{3}$'),
    mgmt_egress_id text NOT NULL CHECK (mgmt_egress_id <> ''),
    status text NOT NULL DEFAULT 'connecting' CHECK (status IN
        ('connecting', 'active', 'low_balance', 'needs_attention', 'paused', 'removing', 'removed')),
    status_reason text CHECK (status_reason <> ''),
    -- Raised when credentials, pause or removal change, so a sync round that started before is discarded.
    config_gen bigint NOT NULL DEFAULT 0 CHECK (config_gen >= 0),
    -- Signed and in the supplier's native unit at its own precision: both systems let a balance go negative.
    balance_native numeric,
    balance_observed_at timestamptz,
    low_balance_threshold numeric CHECK (low_balance_threshold >= 0),
    UNIQUE (origin, upstream_account)
);

CREATE TABLE suppliers.supplier_logins (
    supplier_id uuid PRIMARY KEY REFERENCES suppliers.suppliers,
    -- Which of the adapter's credential structures the sealed value holds; the adapter decodes it.
    kind text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]*$'),
    -- The four parts of a sealed secret (platform/secret.Sealed), checked for the shape of AES-256-GCM output: a
    -- ciphertext holds at least the 16-byte tag and the nonce is 12 bytes. A value of another shape was not sealed.
    ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) >= 16),
    nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
    wrapped_dek bytea NOT NULL CHECK (octet_length(wrapped_dek) > 0),
    key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295),
    -- Compare-and-swap counter: a refresh writes only over the version it read.
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    expires_at timestamptz
);

CREATE TABLE suppliers.supplier_groups (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    upstream_ref text NOT NULL CHECK (upstream_ref <> ''),
    name text NOT NULL CHECK (name <> ''),
    ratio numeric NOT NULL CHECK (ratio >= 0),
    -- The supplier's raw facts about the group (platform, subscription type, peak pricing, long context).
    facts jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(facts) = 'object'),
    selected boolean NOT NULL DEFAULT false,
    -- What users see in place of the group's name, made when the group is first selected and never changed after.
    codename text UNIQUE CHECK (codename <> ''),
    CONSTRAINT selected_has_codename CHECK (NOT selected OR codename IS NOT NULL),
    present boolean NOT NULL DEFAULT true,
    last_seen_at timestamptz,
    UNIQUE (supplier_id, upstream_ref),
    -- The target of the composite foreign keys below: a group together with its supplier.
    UNIQUE (id, supplier_id)
);

-- +goose StatementBegin
CREATE FUNCTION suppliers.keep_codename() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.codename IS NOT NULL AND NEW.codename IS DISTINCT FROM OLD.codename THEN
        RAISE EXCEPTION 'the codename of a group never changes' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER keep_codename BEFORE UPDATE OF codename ON suppliers.supplier_groups
    FOR EACH ROW EXECUTE FUNCTION suppliers.keep_codename();

CREATE TABLE suppliers.credentials (
    -- Chosen by the caller, not by default: the row id is part of what the sealed value is bound to, so it is known
    -- before the value is sealed.
    id uuid PRIMARY KEY,
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    supplier_group_id uuid NOT NULL,
    FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups (id, supplier_id),
    -- The supplier's own id of the key. The key itself is read from the supplier by this id, never typed in.
    upstream_key_ref text NOT NULL CHECK (upstream_key_ref <> ''),
    -- The key, sealed. A row still being bound has not read it yet; any other row has it.
    -- Sealed as in supplier_logins.
    ciphertext bytea CHECK (octet_length(ciphertext) >= 16),
    nonce bytea CHECK (octet_length(nonce) = 12),
    wrapped_dek bytea CHECK (octet_length(wrapped_dek) > 0),
    key_version bigint CHECK (key_version BETWEEN 1 AND 4294967295),
    CONSTRAINT sealed_together CHECK ((ciphertext IS NULL) = (nonce IS NULL)
        AND (ciphertext IS NULL) = (wrapped_dek IS NULL) AND (ciphertext IS NULL) = (key_version IS NULL)),
    status text NOT NULL DEFAULT 'binding' CHECK (status IN
        ('binding', 'active', 'upstream_disabled', 'quota_exhausted', 'expired', 'disabled')),
    CONSTRAINT sealed_unless_binding CHECK (status = 'binding' OR ciphertext IS NOT NULL),
    cooldown_until timestamptz,
    expires_at timestamptz,
    max_concurrency integer NOT NULL CHECK (max_concurrency >= 1),
    -- NULL: the supplier's own exit.
    egress_id text CHECK (egress_id <> ''),
    UNIQUE (supplier_id, upstream_key_ref)
);

CREATE TABLE suppliers.supplier_models (
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    supplier_group_id uuid NOT NULL,
    FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups (id, supplier_id),
    upstream_name text NOT NULL CHECK (upstream_name <> ''),
    -- The catalog model it maps to; NULL while it waits to be mapped. Another module's id, so no foreign key.
    model_id uuid,
    formats text[] NOT NULL CHECK (cardinality(formats) >= 1 AND formats <@ ARRAY['messages', 'responses', 'chat']),
    present boolean NOT NULL DEFAULT true,
    PRIMARY KEY (supplier_group_id, upstream_name)
);

CREATE TABLE suppliers.supplier_aliases (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    -- NULL: the alias holds for the whole supplier. The key below treats two NULLs as equal.
    supplier_group_id uuid,
    FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups (id, supplier_id),
    upstream_name text NOT NULL CHECK (upstream_name <> ''),
    model_id uuid NOT NULL,
    set_by text NOT NULL CHECK (set_by IN ('user', 'system')),
    UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name)
);

CREATE TABLE suppliers.endpoints (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    supplier_group_id uuid NOT NULL,
    FOREIGN KEY (supplier_group_id, supplier_id) REFERENCES suppliers.supplier_groups (id, supplier_id),
    model_id uuid NOT NULL,
    upstream_name text NOT NULL CHECK (upstream_name <> ''),
    formats text[] NOT NULL CHECK (cardinality(formats) >= 1 AND formats <@ ARRAY['messages', 'responses', 'chat']),
    cost_state text NOT NULL DEFAULT 'unknown' CHECK (cost_state IN ('known', 'unknown', 'conflict')),
    cost_state_reason text CHECK (cost_state_reason <> ''),
    -- The cost schedule in force; its table comes with the sync code, so for now no foreign key.
    cost_schedule_id uuid,
    -- Only an endpoint whose cost we know may be routed to.
    CONSTRAINT known_has_schedule CHECK (cost_state <> 'known' OR cost_schedule_id IS NOT NULL),
    enabled boolean NOT NULL DEFAULT true,
    UNIQUE (supplier_group_id, model_id)
);

-- Row-level security. suppliers decides by its owner; every other table by whether its supplier is visible.
ALTER TABLE suppliers.suppliers ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY owner ON suppliers.suppliers USING (platform.in_scope(site_id, account_id));

ALTER TABLE suppliers.supplier_logins ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.supplier_logins
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = supplier_logins.supplier_id));
ALTER TABLE suppliers.supplier_groups ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.supplier_groups
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = supplier_groups.supplier_id));
ALTER TABLE suppliers.credentials ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.credentials
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = credentials.supplier_id));
ALTER TABLE suppliers.supplier_models ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.supplier_models
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = supplier_models.supplier_id));
ALTER TABLE suppliers.supplier_aliases ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.supplier_aliases
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = supplier_aliases.supplier_id));
ALTER TABLE suppliers.endpoints ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.endpoints
    USING (EXISTS (SELECT FROM suppliers.suppliers s WHERE s.id = endpoints.supplier_id));

-- Privileges: no DELETE anywhere; UPDATE only on the columns that may change.
GRANT SELECT, INSERT ON suppliers.suppliers, suppliers.supplier_logins, suppliers.supplier_groups,
    suppliers.credentials, suppliers.supplier_models, suppliers.supplier_aliases, suppliers.endpoints TO axiom_app;
GRANT UPDATE (name, generation, detected_version, native_unit, settlement_currency, mgmt_egress_id, status,
    status_reason, config_gen, balance_native, balance_observed_at, low_balance_threshold)
    ON suppliers.suppliers TO axiom_app;
GRANT UPDATE (kind, ciphertext, nonce, wrapped_dek, key_version, version, expires_at)
    ON suppliers.supplier_logins TO axiom_app;
GRANT UPDATE (name, ratio, facts, selected, codename, present, last_seen_at)
    ON suppliers.supplier_groups TO axiom_app;
GRANT UPDATE (ciphertext, nonce, wrapped_dek, key_version, status, cooldown_until, expires_at, max_concurrency,
    egress_id) ON suppliers.credentials TO axiom_app;
GRANT UPDATE (model_id, formats, present) ON suppliers.supplier_models TO axiom_app;
GRANT UPDATE (model_id, set_by) ON suppliers.supplier_aliases TO axiom_app;
GRANT UPDATE (upstream_name, formats, cost_state, cost_state_reason, cost_schedule_id, enabled)
    ON suppliers.endpoints TO axiom_app;
