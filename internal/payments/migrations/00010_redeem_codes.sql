-- +goose Up

-- Redeem codes (docs/domains/payments.md「兑换码」, README.md「兑换码」): a site issues a batch of codes, each worth a
-- fixed credit, and an account redeems one into a credit batch of billing. The code itself is never stored, so a
-- database read cannot redeem it: code_hash is SHA-256 of the normalized text, and code_hint its last four characters,
-- for support to find a code the user names. A code credits once: its state moves enabled -> used once, and
-- redeem_redemptions keeps one row per code (unique code_id), whose ID is the credit batch's.

CREATE TABLE payments.redeem_codes (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    code_hash bytea NOT NULL CHECK (octet_length(code_hash) = 32),
    code_hint text NOT NULL CHECK (code_hint ~ '^[0-9A-HJKMNP-TV-Z]{4}$'),
    amount bigint NOT NULL CHECK (amount > 0),
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 200),
    state text NOT NULL DEFAULT 'enabled' CHECK (state IN ('enabled', 'used', 'disabled')),
    -- The last moment the code can be redeemed; NULL never. The credit batch it makes expires then too, so a code's
    -- face value is what it is worth until this instant.
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by uuid NOT NULL,
    redeemed_at timestamptz,
    disabled_at timestamptz,
    disabled_by uuid,
    -- A used code was redeemed, and only a used code; a disabled one was disabled. A state without its stamp, or a
    -- stamp without its state, would make the page and the audit disagree.
    CHECK ((state = 'used') = (redeemed_at IS NOT NULL)),
    CHECK ((state = 'disabled') = (disabled_at IS NOT NULL)),
    UNIQUE (site_id, code_hash)
);
CREATE INDEX redeem_codes_by_site_state ON payments.redeem_codes (site_id, state, created_at DESC, id DESC);
CREATE INDEX redeem_codes_by_site_created ON payments.redeem_codes (site_id, created_at DESC, id DESC);

-- The account's history, one row per redeemed code, kept even after the code row is gone (it is never deleted): the
-- credit's ID is this row's ID, so the ledger, the statement and the support page name the same thing.
CREATE TABLE payments.redeem_redemptions (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    code_id uuid NOT NULL UNIQUE REFERENCES payments.redeem_codes (id),
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    amount bigint NOT NULL CHECK (amount > 0),
    -- The last four characters of the code, so the account can tell its redemptions apart without the code being kept.
    code_hint text NOT NULL CHECK (code_hint ~ '^[0-9A-HJKMNP-TV-Z]{4}$'),
    expires_at timestamptz,
    idem_key text NOT NULL CHECK (octet_length(idem_key) BETWEEN 1 AND 128),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (account_id, idem_key)
);
CREATE INDEX redeem_redemptions_by_account ON payments.redeem_redemptions (account_id, created_at DESC, id DESC);

-- One row per site, limit and subject, the serialization point of the per-account and per-IP attempt limits that span
-- transactions (README.md「决策与理由」). A row exists only after an attempt, so the table is no larger than the attempts
-- in the last window plus the accounts and IPs that ever tried; the window restarts from the first attempt after it.
CREATE TABLE payments.redeem_attempts (
    site_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('account', 'ip')),
    subject text NOT NULL CHECK (octet_length(subject) BETWEEN 1 AND 64),
    window_start timestamptz NOT NULL,
    attempts integer NOT NULL CHECK (attempts >= 0),
    PRIMARY KEY (site_id, kind, subject)
);

ALTER TABLE payments.redeem_codes ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.redeem_redemptions ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE payments.redeem_attempts ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;

-- Site data: a user's account scope never sees a code or its hash. The redemption path runs in the platform scope, as
-- its page's authorization already named the account; only the account's own history is readable by an account scope.
-- Scope reads are policies' own subqueries, so each is an InitPlan once per statement (00005_scope_once.sql).
CREATE POLICY site ON payments.redeem_codes
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    );
GRANT SELECT, INSERT, UPDATE (state, redeemed_at, disabled_at, disabled_by) ON payments.redeem_codes TO axiom_app;

CREATE POLICY account ON payments.redeem_redemptions
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
    );
GRANT SELECT, INSERT ON payments.redeem_redemptions TO axiom_app;

-- Shared counter: read, created and added to by the site's own scopes; ON CONFLICT's update is held to both checks.
CREATE POLICY reads ON payments.redeem_attempts FOR SELECT
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR site_id = (SELECT platform.home_site())
    );
CREATE POLICY inserts ON payments.redeem_attempts FOR INSERT
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR site_id = (SELECT platform.home_site())
    );
CREATE POLICY updates ON payments.redeem_attempts FOR UPDATE
    USING (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    )
    WITH CHECK (
        (SELECT platform.in_platform_scope())
        OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
        OR site_id = (SELECT platform.home_site())
    );
GRANT SELECT, INSERT (site_id, kind, subject, window_start, attempts), UPDATE (window_start, attempts)
    ON payments.redeem_attempts TO axiom_app;
