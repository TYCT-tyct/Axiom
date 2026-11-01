-- +goose NO TRANSACTION
-- +goose Up

-- Subscription plans as a funding source (docs/domains/billing.md「订阅套餐」): the admin's catalog of plans, the
-- account's subscription and its windows, the grants that sold or gave them, and the two columns a reservation needs
-- to stay in the window it was frozen into. Every ALTER is atomic, so this nontransactional migration can resume after
-- a failure; every object is guarded with IF NOT EXISTS and every policy is dropped first, so running the whole file
-- again after a partial success passes. Nothing here rewrites a row: the new reservation columns have constant defaults
-- or stay empty for rows an old node wrote.
CREATE TABLE IF NOT EXISTS billing.subscription_plans (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    name text NOT NULL CHECK (name <> '' AND length(name) <= 200),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    price bigint NOT NULL CHECK (price >= 0),           -- minor units of currency, the catalog price
    quota bigint NOT NULL CHECK (quota > 0),            -- credits in one window
    window_seconds bigint NOT NULL CHECK (window_seconds BETWEEN 3600 AND 9223372036),
    term_seconds bigint NOT NULL CHECK (term_seconds BETWEEN window_seconds AND 9223372036),
    active boolean NOT NULL DEFAULT true,
    rev integer NOT NULL DEFAULT 1 CHECK (rev > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (site_id, name)
);
ALTER TABLE billing.subscription_plans ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON billing.subscription_plans;
CREATE POLICY reads ON billing.subscription_plans FOR SELECT USING ((SELECT platform.in_platform_scope()));
DROP POLICY IF EXISTS inserts ON billing.subscription_plans;
CREATE POLICY inserts ON billing.subscription_plans FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
DROP POLICY IF EXISTS updates ON billing.subscription_plans;
CREATE POLICY updates ON billing.subscription_plans FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON billing.subscription_plans TO axiom_app;
GRANT UPDATE (name, price, quota, window_seconds, term_seconds, active, rev, updated_at) ON billing.subscription_plans TO axiom_app;

-- One account has at most one subscription in force, so its current window is unique; a renewal extends this row.
-- plan_name is the name at grant time: a plan renamed or disabled later does not rewrite what the account bought, and
-- the console reads this row in the account's scope without reading the platform's catalog.
CREATE TABLE IF NOT EXISTS billing.subscriptions (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_name text NOT NULL CHECK (plan_name <> ''),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    current_seq integer NOT NULL DEFAULT 1 CHECK (current_seq >= 1),
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'expired')),
    closed_at timestamptz,                             -- when the remaining liability was written off
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (ends_at > starts_at),
    -- An expired term can still hold reservations made while it was active, so its liability is written off later, when
    -- the last of them has settled (docs/domains/billing.md「订阅套餐」).
    CHECK (closed_at IS NULL OR state = 'expired')
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_subscription ON billing.subscriptions (account_id) WHERE state = 'active';
ALTER TABLE billing.subscriptions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON billing.subscriptions;
CREATE POLICY reads ON billing.subscriptions FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS inserts ON billing.subscriptions;
CREATE POLICY inserts ON billing.subscriptions FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS updates ON billing.subscriptions;
CREATE POLICY updates ON billing.subscriptions FOR UPDATE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
)
    WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
GRANT SELECT, INSERT ON billing.subscriptions TO axiom_app;
GRANT UPDATE (ends_at, current_seq, state, closed_at) ON billing.subscriptions TO axiom_app;

-- A window is one slice of the term; quota, used and reserved are credits. Windows never carry over: a new window
-- starts from zero. used + reserved <= quota is the invariant admission relies on, and reserved only moves under the
-- account's balance row lock.
CREATE TABLE IF NOT EXISTS billing.subscription_windows (
    subscription_id uuid NOT NULL,
    seq integer NOT NULL CHECK (seq >= 1),
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    quota bigint NOT NULL CHECK (quota > 0),
    used bigint NOT NULL DEFAULT 0 CHECK (used >= 0),
    reserved bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    PRIMARY KEY (subscription_id, seq),
    CHECK (ends_at > starts_at),
    CHECK (used + reserved <= quota)
);
ALTER TABLE billing.subscription_windows ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON billing.subscription_windows;
CREATE POLICY reads ON billing.subscription_windows FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS inserts ON billing.subscription_windows;
CREATE POLICY inserts ON billing.subscription_windows FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS updates ON billing.subscription_windows;
CREATE POLICY updates ON billing.subscription_windows FOR UPDATE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
)
    WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
GRANT SELECT, INSERT ON billing.subscription_windows TO axiom_app;
GRANT UPDATE (used, reserved) ON billing.subscription_windows TO axiom_app;

-- What one grant or renewal sold: the plan's terms as they were copied into the windows, the face value credited to
-- the liability (windows × quota), and, for a payment, what was received. The ID is the caller's idempotency key.
CREATE TABLE IF NOT EXISTS billing.subscription_grants (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_name text NOT NULL CHECK (plan_name <> ''),
    source text NOT NULL CHECK (source IN ('gift', 'paid')),
    quota bigint NOT NULL CHECK (quota > 0),
    window_seconds bigint NOT NULL CHECK (window_seconds BETWEEN 3600 AND 9223372036),
    term_seconds bigint NOT NULL CHECK (term_seconds BETWEEN window_seconds AND 9223372036),
    windows integer NOT NULL CHECK (windows >= 1 AND windows <= 400),
    face bigint NOT NULL CHECK (face > 0),               -- windows × quota, the liability credited
    currency text,
    paid bigint,
    fee bigint,
    merchant_id uuid,
    offline_channel text,
    rate_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (
        (source = 'gift' AND currency IS NULL AND paid IS NULL AND fee IS NULL AND merchant_id IS NULL
            AND offline_channel IS NULL AND rate_id IS NULL)
        OR (source = 'paid' AND currency ~ '^[A-Z]{3}$' AND paid > 0 AND fee >= 0 AND fee < paid
            AND ((merchant_id IS NOT NULL) <> (offline_channel IS NOT NULL)))),
    CHECK (face = quota * windows)
);
ALTER TABLE billing.subscription_grants ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON billing.subscription_grants;
CREATE POLICY reads ON billing.subscription_grants FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS inserts ON billing.subscription_grants;
CREATE POLICY inserts ON billing.subscription_grants FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
GRANT SELECT, INSERT ON billing.subscription_grants TO axiom_app;

-- A reservation freezes where its money comes from: the wallet, or a subscription window. Old rows and old nodes
-- write 'wallet' and no subscription, which is exactly how they settle.
ALTER TABLE billing.reservations
    ADD COLUMN IF NOT EXISTS funding text NOT NULL DEFAULT 'wallet',
    ADD COLUMN IF NOT EXISTS subscription_id uuid,
    ADD COLUMN IF NOT EXISTS window_seq integer,
    DROP CONSTRAINT IF EXISTS reservations_funding_check,
    ADD CONSTRAINT reservations_funding_check CHECK (
        (funding = 'wallet' AND subscription_id IS NULL AND window_seq IS NULL)
        OR (funding = 'plan' AND subscription_id IS NOT NULL AND window_seq IS NOT NULL)) NOT VALID;
-- A late settlement may rewrite a released plan hold as a wallet hold, so the app needs those three columns.
GRANT UPDATE (funding, subscription_id, window_seq) ON billing.reservations TO axiom_app;

-- The plan's liability and the discount it was sold at (docs/domains/billing.md「账本」).
ALTER TABLE billing.entries
    DROP CONSTRAINT IF EXISTS entries_book_check,
    ADD CONSTRAINT entries_book_check CHECK (book IN ('user_balance', 'revenue', 'platform_borne', 'supplier_cost',
        'supplier_prepaid', 'gift_expense', 'credit_expired', 'cash', 'fee_income', 'exchange_gain_loss',
        'plan_liability', 'plan_discount')) NOT VALID;

ALTER TABLE billing.transactions
    DROP CONSTRAINT IF EXISTS transactions_kind_check,
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost',
        'cost_adjust', 'expire', 'freeze', 'unfreeze', 'refund', 'chargeback', 'chargeback_reverse',
        'plan_credit', 'plan_expire')) NOT VALID;

ALTER TABLE billing.reservations VALIDATE CONSTRAINT reservations_funding_check;
ALTER TABLE billing.entries VALIDATE CONSTRAINT entries_book_check;
ALTER TABLE billing.transactions VALIDATE CONSTRAINT transactions_kind_check;

-- +goose Down
DROP TABLE IF EXISTS billing.subscription_grants;
DROP TABLE IF EXISTS billing.subscription_windows;
DROP TABLE IF EXISTS billing.subscriptions;
DROP TABLE IF EXISTS billing.subscription_plans;
ALTER TABLE billing.reservations
    DROP CONSTRAINT IF EXISTS reservations_funding_check,
    DROP COLUMN IF EXISTS funding,
    DROP COLUMN IF EXISTS subscription_id,
    DROP COLUMN IF EXISTS window_seq;
