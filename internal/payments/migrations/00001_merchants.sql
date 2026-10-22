-- +goose Up

-- Merchant accounts, their payment methods, their daily usage and the site's payment settings (README.md「数据」).
-- No foreign key leaves the module; the composite keys to merchants (id, site_id) keep a child row on its merchant's
-- site, which RLS alone cannot: a row's policy sees only that row, so an account of site A could otherwise create a
-- usage row for site B's merchant under site A and block B's own upsert of that day.

-- Site public configuration and the shared counter are read through platform.sees_site: the platform, a scope
-- listing the site, or an account scope whose home site it is (internal/platform/README.md「范围」).
GRANT USAGE ON SCHEMA payments TO axiom_app;

-- Never deleted, only disabled: orders keep finding their merchant.
CREATE TABLE payments.merchants (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{0,31}$'),
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    enabled boolean NOT NULL,
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    ttl_seconds integer NOT NULL CHECK (ttl_seconds BETWEEN 60 AND 86400),
    refund_enabled boolean NOT NULL,
    user_refund boolean NOT NULL,
    sort integer NOT NULL,
    rev bigint NOT NULL CHECK (rev > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (id, site_id)
);
CREATE INDEX merchants_by_site ON payments.merchants (site_id, sort);

-- settings is canonical JSON kept as text: its SHA-256 is sealed with the secrets, and jsonb would re-serialize it.
CREATE TABLE payments.merchant_configs (
    merchant_id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    settings text NOT NULL CHECK (octet_length(settings) BETWEEN 2 AND 65536),
    ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) > 0),
    nonce bytea NOT NULL CHECK (octet_length(nonce) > 0),
    wrapped_dek bytea NOT NULL CHECK (octet_length(wrapped_dek) > 0),
    key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295),
    rev bigint NOT NULL CHECK (rev > 0),
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants (id, site_id)
);

-- Amounts are minor units of the merchant's currency, totals the user pays.
CREATE TABLE payments.merchant_methods (
    merchant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    method text NOT NULL CHECK (method ~ '^[a-z][a-z0-9_.]{0,31}$'),
    upstream text NOT NULL CHECK (char_length(upstream) BETWEEN 1 AND 64),
    fee_ppm integer NOT NULL CHECK (fee_ppm BETWEEN 0 AND 500000),
    fee_fixed bigint NOT NULL CHECK (fee_fixed >= 0),
    single_min bigint CHECK (single_min > 0),
    single_max bigint CHECK (single_max > 0),
    daily_limit bigint CHECK (daily_limit > 0),
    enabled boolean NOT NULL,
    PRIMARY KEY (merchant_id, method),
    CHECK (single_min <= single_max),
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants (id, site_id)
);
CREATE INDEX merchant_methods_by_site ON payments.merchant_methods (site_id, method);

-- One row per merchant and UTC day: the serialization point of the daily limit, which an account-scoped order locks
-- and adds to without reading other accounts' orders (README.md「决策与理由」).
CREATE TABLE payments.merchant_usage (
    merchant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    day date NOT NULL,
    used bigint NOT NULL CHECK (used >= 0),
    PRIMARY KEY (merchant_id, day),
    FOREIGN KEY (merchant_id, site_id) REFERENCES payments.merchants (id, site_id)
);

-- NULL is the default in code (README.md「数据」: only departures from the defaults are stored).
CREATE TABLE payments.settings (
    site_id uuid PRIMARY KEY,
    open boolean,
    credits_min bigint CHECK (credits_min > 0),
    credits_max bigint CHECK (credits_max > 0),
    strategy text CHECK (strategy IN ('round_robin', 'least_used')),
    CHECK (credits_min <= credits_max)
);

ALTER TABLE payments.merchants ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.merchant_configs ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.merchant_methods ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.merchant_usage ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.settings ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;

-- Site public configuration: the top-up page of an account reads its site's rows; only the site or the platform writes.
CREATE POLICY reads ON payments.merchants FOR SELECT USING (platform.sees_site(site_id));
CREATE POLICY inserts ON payments.merchants FOR INSERT WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY updates ON payments.merchants FOR UPDATE USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));
GRANT SELECT, INSERT, UPDATE ON payments.merchants TO axiom_app;

CREATE POLICY reads ON payments.merchant_methods FOR SELECT USING (platform.sees_site(site_id));
CREATE POLICY inserts ON payments.merchant_methods FOR INSERT WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY updates ON payments.merchant_methods FOR UPDATE USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY deletes ON payments.merchant_methods FOR DELETE USING (platform.in_scope(site_id, NULL));
GRANT SELECT, INSERT, UPDATE, DELETE ON payments.merchant_methods TO axiom_app;

CREATE POLICY reads ON payments.settings FOR SELECT USING (platform.sees_site(site_id));
CREATE POLICY inserts ON payments.settings FOR INSERT WITH CHECK (platform.in_scope(site_id, NULL));
CREATE POLICY updates ON payments.settings FOR UPDATE USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));
GRANT SELECT, INSERT, UPDATE ON payments.settings TO axiom_app;

-- Site data: never the account scope.
CREATE POLICY site ON payments.merchant_configs USING (platform.in_scope(site_id, NULL))
    WITH CHECK (platform.in_scope(site_id, NULL));
GRANT SELECT, INSERT, UPDATE ON payments.merchant_configs TO axiom_app;

-- Shared counter: read, created and added to by the site's own scopes; ON CONFLICT's update is held to both checks.
CREATE POLICY reads ON payments.merchant_usage FOR SELECT USING (platform.sees_site(site_id));
CREATE POLICY inserts ON payments.merchant_usage FOR INSERT WITH CHECK (platform.sees_site(site_id));
CREATE POLICY updates ON payments.merchant_usage FOR UPDATE USING (platform.sees_site(site_id))
    WITH CHECK (platform.sees_site(site_id));
GRANT SELECT, INSERT (merchant_id, site_id, day, used), UPDATE (used) ON payments.merchant_usage TO axiom_app;
