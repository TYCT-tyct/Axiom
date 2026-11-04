-- +goose Up

-- Legal texts are published in each supported locale as its own version stream: a correction to the English text does
-- not force a new Chinese version, and an acceptance names the exact locale and version that was shown. Existing rows
-- were the single, bilingual demo text; they become Chinese (zh) rows until the operator publishes the split texts.
ALTER TABLE accounts.legal_documents
    ADD COLUMN locale text NOT NULL DEFAULT 'zh' CHECK (locale IN ('zh', 'en'));

-- Both constraints depend on the old primary key, so remove them before changing it.
ALTER TABLE accounts.legal_acceptances
    DROP CONSTRAINT legal_acceptances_user_id_kind_version_key;
ALTER TABLE accounts.legal_acceptances
    DROP CONSTRAINT legal_acceptances_site_id_kind_version_fkey;
ALTER TABLE accounts.legal_documents
    DROP CONSTRAINT legal_documents_pkey;

ALTER TABLE accounts.legal_documents
    ADD CONSTRAINT legal_documents_pkey PRIMARY KEY (site_id, kind, locale, version);

ALTER TABLE accounts.legal_acceptances
    ADD COLUMN locale text NOT NULL DEFAULT 'zh' CHECK (locale IN ('zh', 'en'));

ALTER TABLE accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_user_kind_locale_version_key UNIQUE (user_id, kind, locale, version);
ALTER TABLE accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_document_fkey
        FOREIGN KEY (site_id, kind, locale, version)
        REFERENCES accounts.legal_documents (site_id, kind, locale, version);

-- +goose Down

ALTER TABLE accounts.legal_acceptances DROP CONSTRAINT legal_acceptances_document_fkey;
ALTER TABLE accounts.legal_acceptances DROP CONSTRAINT legal_acceptances_user_kind_locale_version_key;
ALTER TABLE accounts.legal_acceptances DROP COLUMN locale;
ALTER TABLE accounts.legal_documents DROP CONSTRAINT legal_documents_pkey;
ALTER TABLE accounts.legal_documents DROP COLUMN locale;
ALTER TABLE accounts.legal_documents ADD PRIMARY KEY (site_id, kind, version);
ALTER TABLE accounts.legal_acceptances ADD UNIQUE (user_id, kind, version);
ALTER TABLE accounts.legal_acceptances
    ADD FOREIGN KEY (site_id, kind, version)
        REFERENCES accounts.legal_documents (site_id, kind, version);
