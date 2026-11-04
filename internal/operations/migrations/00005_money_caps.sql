-- +goose Up

-- The cumulative caps on the money the agent moves (docs/domains/operations.md「资金操作上限」). Every amount is an
-- integer in 10⁻⁹ of the policy's currency. A scope is a site, or the platform (site_id NULL) for what no site owns,
-- such as a platform supplier.

-- What the owner allows, per scope and category. The autonomous limits decide whether the agent's proposal waits for
-- the owner; the hard limits refuse it outright, approval or not. No row is no money: a proposal without a policy is
-- refused. The currency is fixed when the policy is made, so a counter never mixes two; `rev` grows with every change,
-- and a proposal waiting on the owner is refused when the policy it was weighed against has changed.
CREATE TABLE operations.cap_policies (
    site_id uuid,
    category text NOT NULL CHECK (category IN ('money.top_up')),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    single_max bigint NOT NULL CHECK (single_max >= 0),
    daily_max bigint NOT NULL CHECK (daily_max >= 0),
    weekly_max bigint NOT NULL CHECK (weekly_max >= 0),
    object_daily_max bigint NOT NULL CHECK (object_daily_max >= 0),
    daily_count_max integer NOT NULL CHECK (daily_count_max >= 0),
    hard_single_max bigint NOT NULL CHECK (hard_single_max >= single_max),
    hard_daily_max bigint NOT NULL CHECK (hard_daily_max >= daily_max),
    hard_weekly_max bigint NOT NULL CHECK (hard_weekly_max >= weekly_max),
    rev bigint NOT NULL CHECK (rev >= 1),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (category, site_id)
);

-- What is reserved and committed in a fixed UTC window: the category's day and ISO week, and each object's day. Rows
-- start at zero and only the reservations of money_actions move them, so they can be recomputed from it.
CREATE TABLE operations.cap_counters (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    category text NOT NULL,
    window_kind text NOT NULL CHECK (window_kind IN ('day', 'week')),
    window_start timestamptz NOT NULL,
    object_kind text NOT NULL DEFAULT '',
    object_id text NOT NULL DEFAULT '',
    site_id uuid,
    reserved bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    committed bigint NOT NULL DEFAULT 0 CHECK (committed >= 0),
    count integer NOT NULL DEFAULT 0 CHECK (count >= 0),
    CHECK (object_kind = '' OR window_kind = 'day'),
    UNIQUE NULLS NOT DISTINCT (category, window_kind, window_start, object_kind, object_id, site_id)
);

-- One money operation's reservation, with what the server computed it from, so the audit can recompute it: the amount
-- in the object's native unit, the unit value, the exchange rate to the policy's currency and the result, rounded up.
-- It is reserved until the agent confirms the money arrived (committed) or it ends (released): the proposal is
-- refused, expires, is withdrawn, or its windows end. The application may change only its state and confirmation.
CREATE TABLE operations.money_actions (
    operation_id uuid PRIMARY KEY REFERENCES operations.operations (id),
    category text NOT NULL CHECK (category IN ('money.top_up')),
    site_id uuid,
    object_kind text NOT NULL CHECK (object_kind ~ '^[a-z][a-z0-9_]{0,63}$'),
    object_id text NOT NULL CHECK (char_length(object_id) BETWEEN 1 AND 128),
    amount bigint NOT NULL CHECK (amount > 0),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    basis json NOT NULL CHECK (octet_length(basis::text) <= 4096),
    policy_rev bigint NOT NULL,
    day_start timestamptz NOT NULL,
    week_start timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('reserved', 'committed', 'released')),
    proof text CHECK (char_length(proof) BETWEEN 1 AND 500),
    confirmed_by uuid REFERENCES operations.operations (id),
    settled_at timestamptz,
    CHECK ((state = 'committed') = (proof IS NOT NULL AND confirmed_by IS NOT NULL)),
    CHECK ((state = 'reserved') = (settled_at IS NULL))
);
-- The sweep of the reservations past the end of their windows.
CREATE INDEX money_actions_due ON operations.money_actions (expires_at) WHERE state = 'reserved';

-- A top-up is the first money operation.
ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting', 'top_up'));

-- Platform data, like the operations themselves.
ALTER TABLE operations.cap_policies ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.cap_policies FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.cap_policies FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.cap_policies FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
ALTER TABLE operations.cap_counters ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.cap_counters FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.cap_counters FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.cap_counters FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
ALTER TABLE operations.money_actions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.money_actions FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.money_actions FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.money_actions FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON operations.cap_policies, operations.cap_counters, operations.money_actions TO axiom_app;
GRANT UPDATE (single_max, daily_max, weekly_max, object_daily_max, daily_count_max, hard_single_max,
    hard_daily_max, hard_weekly_max, rev, updated_at) ON operations.cap_policies TO axiom_app;
GRANT UPDATE (reserved, committed, count) ON operations.cap_counters TO axiom_app;
GRANT UPDATE (state, proof, confirmed_by, settled_at) ON operations.money_actions TO axiom_app;

-- +goose Down
ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting'));
DROP TABLE operations.money_actions;
DROP TABLE operations.cap_counters;
DROP TABLE operations.cap_policies;
