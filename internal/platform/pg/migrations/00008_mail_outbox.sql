-- +goose Up

-- The outbox of internal/platform/mail: one row per message to send, written in the tenant's scope by the transaction
-- that causes it, and read by a worker that starts with nothing but the row's id (internal/platform/mail/README.md
-- 「队列与重试」). The message stays here and never in the queue's args: a job row belongs to no tenant and so no policy
-- covers it, while this table's policy stops a caller from writing a message for a tenant outside its scope and stops
-- a reader from reading one. The row is deleted when it has been sent or refused for good.
--
-- No foreign key to platform.tenants, unlike the audit log: a message is queued in the transaction that causes it, and
-- the policy already limits the row to a tenant in the caller's scope. The registry would add a second prerequisite to
-- sending mail — a tenant registered first — without keeping out anything the policy lets in.
CREATE TABLE platform.mail_outbox (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    site_id uuid NOT NULL,
    -- The account a message is for; the site itself sends the messages that belong to no account yet.
    account_id uuid,
    recipient text NOT NULL CHECK (recipient <> ''),
    subject text NOT NULL CHECK (subject <> ''),
    text_body text NOT NULL CHECK (text_body <> ''),
    html_body text NOT NULL CHECK (html_body <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One policy reads, writes and deletes: a transaction sees and writes the messages of the tenants in its scope, and
-- the tenant columns are what both USING and WITH CHECK compare, so a caller cannot queue a message for another
-- tenant. No UPDATE: a queued message is sent as written or deleted, never edited.
ALTER TABLE platform.mail_outbox ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON platform.mail_outbox USING (platform.in_scope(site_id, account_id));

GRANT SELECT, INSERT (site_id, account_id, recipient, subject, text_body, html_body), DELETE
    ON platform.mail_outbox TO axiom_app;

-- A worker starts with the id the queue carries and nothing else, so it must learn the row's tenant before any scope
-- is set. The lookup does that and nothing more: the role reads the two tenant columns through a policy of its own,
-- and the function returns them, so the payload is read by the application in the tenant's scope (the pattern of
-- internal/accounts' 00002_lookup.sql).
GRANT USAGE ON SCHEMA platform TO axiom_lookup;
GRANT SELECT (site_id, account_id) ON platform.mail_outbox TO axiom_lookup;
CREATE POLICY lookup ON platform.mail_outbox FOR SELECT TO axiom_lookup USING (true);

-- The tenant of the queued message with this id; no row (sent, refused, or deleted) gives an empty result.
-- +goose StatementBegin
CREATE FUNCTION platform.mail_tenant(id uuid)
    RETURNS TABLE (site_id uuid, account_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
    AS $$
        SELECT m.site_id, m.account_id FROM platform.mail_outbox m WHERE m.id = mail_tenant.id
    $$;
-- +goose StatementEnd

-- Only the application runs it. Handing a function to axiom_lookup takes CREATE on the schema for the new owner,
-- granted for that statement alone.
REVOKE EXECUTE ON FUNCTION platform.mail_tenant FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform.mail_tenant TO axiom_app;
GRANT CREATE ON SCHEMA platform TO axiom_lookup;
ALTER FUNCTION platform.mail_tenant OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA platform FROM axiom_lookup;
