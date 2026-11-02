-- +goose Up
-- Subscription refunds (docs/domains/billing.md「订阅套餐」): an operator may take back a paid grant's term, in whole
-- or in part, and the ledger reverses what was not used plus the revenue of what was. A grant records the first window
-- it wrote so a refund knows its block; a window is voided rather than deleted so a settled charge and its corrections
-- keep their row.
ALTER TABLE billing.subscription_grants
    ADD COLUMN first_window integer,
    ADD COLUMN refunded_value bigint NOT NULL DEFAULT 0 CHECK (refunded_value >= 0),
    ADD COLUMN refunded_paid bigint NOT NULL DEFAULT 0 CHECK (refunded_paid >= 0),
    ADD COLUMN refunded_fee bigint NOT NULL DEFAULT 0 CHECK (refunded_fee >= 0),
    ADD COLUMN refunded_windows integer NOT NULL DEFAULT 0 CHECK (refunded_windows >= 0);
-- Grants written before this migration did not record where their block started. Their windows are contiguous and
-- grants are appended in creation order, so the running total of the earlier grants is the answer.
UPDATE billing.subscription_grants AS g
SET first_window = x.first_window
FROM (
    SELECT id, 1 + coalesce(sum(windows) OVER (
            PARTITION BY subscription_id ORDER BY created_at, id ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0)
        AS first_window
    FROM billing.subscription_grants
) AS x
WHERE g.id = x.id;
-- first_window stays nullable for the rolling release: a node of the previous release inserts grants without it.
-- New grants always record it; refunds derive it for a grant written by an old node (GrantFirstWindow). A later
-- migration may tighten the column after every node writes it.
ALTER TABLE billing.subscription_grants
    ADD CONSTRAINT subscription_grants_first_window_check CHECK (first_window IS NULL OR first_window >= 1),
    ADD CONSTRAINT subscription_grants_refunded_check CHECK (
        refunded_paid <= coalesce(paid, 0) AND refunded_fee <= coalesce(fee, 0)),
    ADD CONSTRAINT subscription_grants_refunded_windows_limit CHECK (refunded_windows <= windows);
DROP POLICY IF EXISTS updates ON billing.subscription_grants;
CREATE POLICY updates ON billing.subscription_grants FOR UPDATE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
)
    WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
GRANT UPDATE (refunded_value, refunded_paid, refunded_fee, refunded_windows) ON billing.subscription_grants TO axiom_app;

ALTER TABLE billing.subscription_windows ADD COLUMN voided_at timestamptz;
GRANT UPDATE (voided_at) ON billing.subscription_windows TO axiom_app;

-- One operator refund: the refund's ID is the idempotency key, and the amounts are what the ledger posted. The grant
-- and subscription columns are copied so a later grant or subscription change does not rewrite history.
CREATE TABLE IF NOT EXISTS billing.subscription_refunds (
    id uuid PRIMARY KEY,
    grant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    credits bigint NOT NULL CHECK (credits > 0),      -- site credits refunded
    paid bigint NOT NULL CHECK (paid > 0),            -- payment currency minor units returned
    fee bigint NOT NULL CHECK (fee >= 0),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    windows integer NOT NULL CHECK (windows >= 0),    -- windows voided by this refund
    unused bigint NOT NULL CHECK (unused >= 0),       -- liability taken back
    revoked boolean NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL CHECK (reason <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE billing.subscription_refunds ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON billing.subscription_refunds;
CREATE POLICY reads ON billing.subscription_refunds FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS inserts ON billing.subscription_refunds;
CREATE POLICY inserts ON billing.subscription_refunds FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS updates ON billing.subscription_refunds;
CREATE POLICY updates ON billing.subscription_refunds FOR UPDATE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
)
    WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
GRANT SELECT, INSERT, UPDATE (revoked) ON billing.subscription_refunds TO axiom_app;

-- A fund operation needs a second person: the request records who asked, and approval records who applied it. The
-- actual refund row uses the request's ID only when approved.
CREATE TABLE IF NOT EXISTS billing.subscription_refund_requests (
    id uuid PRIMARY KEY,
    grant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    credits bigint NOT NULL CHECK (credits > 0),
    site_currency text NOT NULL CHECK (site_currency ~ '^[A-Z]{3}$'),
    requested_by uuid NOT NULL,
    approved_by uuid,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'rejected')),
    reason text NOT NULL CHECK (reason <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    CHECK (approved_by IS NULL OR approved_by <> requested_by),
    CHECK (state = 'pending' OR approved_by IS NOT NULL)
);
ALTER TABLE billing.subscription_refund_requests ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reads ON billing.subscription_refund_requests;
CREATE POLICY reads ON billing.subscription_refund_requests FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS inserts ON billing.subscription_refund_requests;
CREATE POLICY inserts ON billing.subscription_refund_requests FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
DROP POLICY IF EXISTS updates ON billing.subscription_refund_requests;
CREATE POLICY updates ON billing.subscription_refund_requests FOR UPDATE USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
)
    WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
GRANT SELECT, INSERT, UPDATE (approved_by, state, finished_at) ON billing.subscription_refund_requests TO axiom_app;

ALTER TABLE billing.transactions
    DROP CONSTRAINT IF EXISTS transactions_kind_check,
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost',
        'cost_adjust', 'expire', 'freeze', 'unfreeze', 'refund', 'chargeback', 'chargeback_reverse',
        'plan_credit', 'plan_expire', 'plan_refund')) NOT VALID;
ALTER TABLE billing.transactions VALIDATE CONSTRAINT transactions_kind_check;

-- +goose Down
ALTER TABLE billing.transactions
    DROP CONSTRAINT IF EXISTS transactions_kind_check,
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost',
        'cost_adjust', 'expire', 'freeze', 'unfreeze', 'refund', 'chargeback', 'chargeback_reverse',
        'plan_credit', 'plan_expire')) NOT VALID;
DROP TABLE IF EXISTS billing.subscription_refund_requests;
DROP TABLE IF EXISTS billing.subscription_refunds;
ALTER TABLE billing.subscription_windows DROP COLUMN IF EXISTS voided_at;
ALTER TABLE billing.subscription_grants
    DROP COLUMN IF EXISTS first_window,
    DROP COLUMN IF EXISTS refunded_value,
    DROP COLUMN IF EXISTS refunded_paid,
    DROP COLUMN IF EXISTS refunded_fee,
    DROP COLUMN IF EXISTS refunded_windows;
