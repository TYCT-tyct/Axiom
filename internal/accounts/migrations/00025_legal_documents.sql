-- +goose Up

-- The legal documents a site must have accepted (docs/domains/accounts.md「注册同意」): the terms of service and the
-- privacy policy, versioned rather than edited. A version is the row a person accepted, so a row is never changed or
-- deleted: publishing a new version adds a row, and acceptance keeps naming the text that was shown. The text is the
-- site operator's; the code stores it as plain text and never writes terms of its own.
CREATE TABLE accounts.legal_documents (
    site_id uuid NOT NULL REFERENCES accounts.sites,
    kind text NOT NULL CHECK (kind IN ('terms', 'privacy')),
    version integer NOT NULL CHECK (version >= 1),
    body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 65536),
    published_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, kind, version)
);

-- The text is public: the registration page shows it before anyone signs in. Only the platform writes it.
GRANT SELECT, INSERT ON accounts.legal_documents TO axiom_app;

ALTER TABLE accounts.legal_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.legal_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY public_read ON accounts.legal_documents FOR SELECT USING ((SELECT platform.in_any_scope()));
CREATE POLICY platform_insert ON accounts.legal_documents FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));

-- Which version of which document a user accepted, and when. Append-only, so later changes to the document cannot
-- rewrite what a person agreed to; the composite key names one acceptance per user, kind and version, which makes a
-- retried request harmless.
CREATE TABLE accounts.legal_acceptances (
    id uuid PRIMARY KEY,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    version integer NOT NULL CHECK (version >= 1),
    accepted_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, kind, version),
    FOREIGN KEY (site_id, account_id, user_id) REFERENCES accounts.users (site_id, account_id, id),
    FOREIGN KEY (site_id, kind, version) REFERENCES accounts.legal_documents (site_id, kind, version)
);

GRANT SELECT, INSERT ON accounts.legal_acceptances TO axiom_app;

ALTER TABLE accounts.legal_acceptances ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.legal_acceptances FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.legal_acceptances FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);
CREATE POLICY inserts ON accounts.legal_acceptances FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[])
);

-- +goose Down
DROP TABLE accounts.legal_acceptances;
DROP TABLE accounts.legal_documents;
