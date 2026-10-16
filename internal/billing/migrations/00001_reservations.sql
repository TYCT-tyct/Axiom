-- +goose Up

-- What reservations need (internal/billing/README.md「准入里的预留」「持有者与截止时间」「写入的数据」): the holders,
-- each account's balance row, the spend counters of workspaces and keys, and the reservations. Money is in billionths
-- of a credit (docs/domains/billing.md「钱的表示」). Other modules' rows are referred to by ID only, never by foreign
-- key (docs/architecture.md「模块边界规则」). The application gets no DELETE anywhere, and UPDATE only on the columns
-- the README lets it change.

-- A holder is one start of a core process. It writes reservations while it is neither reaped nor expired; a reaped
-- holder stays reaped, which the reaper's FOR UPDATE and the writers' FOR KEY SHARE make safe to rely on.
CREATE TABLE billing.holders (
    id uuid PRIMARY KEY,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    reaped_at timestamptz
);

-- The balance row is the one point every write of an account's money goes through (docs/domains/billing.md
-- 「预留与结算」): balance is the ledger's total, floor how far below zero it may go, reserved the pending reservations.
CREATE TABLE billing.balances (
    account_id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    balance bigint NOT NULL DEFAULT 0,
    floor bigint NOT NULL DEFAULT 0 CHECK (floor <= 0),
    reserved bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0)
);

-- What a workspace or a key has spent and holds reserved, per UTC day (window_start is the day's start). A limit sums
-- the days of its window, whatever window the limit had before. Rows change only under the account's balance row lock.
CREATE TABLE billing.spend (
    subject uuid NOT NULL, -- a workspace or a key
    window_start timestamptz NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    spent bigint NOT NULL DEFAULT 0 CHECK (spent >= 0),
    reserved bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    PRIMARY KEY (subject, window_start)
);

-- A request's reservation. segment counts the extensions written, a guard for compare and write, 0 for the tombstone SettleOrphan writes when the
-- admission never committed; amount is cumulative and only grows while pending. The windows are the ones admission
-- locked (its UTC day), which extensions and settlement write back to. Settlement and cleanup add their states.
CREATE TABLE billing.reservations (
    request_id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    holder_id uuid NOT NULL REFERENCES billing.holders,
    workspace_window timestamptz NOT NULL,
    key_window timestamptz NOT NULL,
    segment integer NOT NULL CHECK (segment >= 0),
    amount bigint NOT NULL CHECK (amount >= 0),
    state text NOT NULL CHECK (state IN ('pending', 'released')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deadline timestamptz NOT NULL,
    CHECK (segment > 0 OR state = 'released' AND amount = 0)
);
CREATE INDEX pending_by_holder ON billing.reservations (holder_id) WHERE state = 'pending';
CREATE INDEX pending_by_deadline ON billing.reservations (deadline) WHERE state = 'pending';

-- The window of a limit that holds the moment at: the UTC day, week or month for accounts' daily, weekly and monthly,
-- from its start up to the next one; every day for a limit that never resets. The arithmetic is on UTC wall-clock
-- time, so the session's time zone cannot move a boundary.
CREATE FUNCTION billing.window_from(reset text, at timestamptz) RETURNS timestamptz
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE reset
        WHEN 'daily' THEN date_trunc('day', at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
        WHEN 'weekly' THEN date_trunc('week', at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
        WHEN 'monthly' THEN date_trunc('month', at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
        ELSE '-infinity' END;

CREATE FUNCTION billing.window_to(reset text, at timestamptz) RETURNS timestamptz
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE reset
        WHEN 'daily' THEN (date_trunc('day', at AT TIME ZONE 'UTC') + interval '1 day') AT TIME ZONE 'UTC'
        WHEN 'weekly' THEN (date_trunc('week', at AT TIME ZONE 'UTC') + interval '1 week') AT TIME ZONE 'UTC'
        WHEN 'monthly' THEN (date_trunc('month', at AT TIME ZONE 'UTC') + interval '1 month') AT TIME ZONE 'UTC'
        ELSE 'infinity' END;

-- One policy per command, never FOR ALL (see the accounts migration). A holder is no tenant's: any scope reads it and
-- takes FOR KEY SHARE on it, which needs an UPDATE policy to pass, but only the platform scope writes it.
ALTER TABLE billing.holders ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.holders FOR SELECT USING (platform.in_any_scope());
CREATE POLICY inserts ON billing.holders FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON billing.holders FOR UPDATE USING (platform.in_any_scope())
    WITH CHECK (platform.in_platform_scope());

ALTER TABLE billing.balances ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.balances FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.balances FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON billing.balances FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));

ALTER TABLE billing.spend ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.spend FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.spend FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON billing.spend FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));

-- A reservation updates only while pending: nothing changes it after it ends, in any scope.
ALTER TABLE billing.reservations ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.reservations FOR SELECT USING (platform.in_scope(site_id, account_id));
CREATE POLICY inserts ON billing.reservations FOR INSERT WITH CHECK (platform.in_scope(site_id, account_id));
CREATE POLICY updates ON billing.reservations FOR UPDATE
    USING (platform.in_scope(site_id, account_id) AND state = 'pending')
    WITH CHECK (platform.in_scope(site_id, account_id));

GRANT USAGE ON SCHEMA billing TO axiom_app;
GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA billing TO axiom_app;
GRANT UPDATE (expires_at, reaped_at) ON billing.holders TO axiom_app;
GRANT UPDATE (reserved) ON billing.balances TO axiom_app;
GRANT UPDATE (reserved) ON billing.spend TO axiom_app;
GRANT UPDATE (segment, amount, state) ON billing.reservations TO axiom_app;
