-- +goose Up

-- Locale is a labeled dimension of a legal document, not yet a separate version stream. This migration is the expand
-- half of an expand/contract rollout: old nodes still run INSERT ... ON CONFLICT (user_id, kind, version), and old
-- admins still publish with the legacy (site_id, kind, version) key. Keeping those constraints and allocating one
-- version number per kind across locales means every old statement keeps working while new nodes read and write
-- locale. Once no old node remains, a later cleanup migration can drop the legacy constraints and let each locale
-- number its versions independently.
--
-- Existing rows were the single, bilingual demo text; they become Chinese (zh) rows until the operator publishes the
-- split texts.
ALTER TABLE accounts.legal_documents
    ADD COLUMN locale text NOT NULL DEFAULT 'zh' CHECK (locale IN ('zh', 'en'));
ALTER TABLE accounts.legal_acceptances
    ADD COLUMN locale text NOT NULL DEFAULT 'zh' CHECK (locale IN ('zh', 'en'));

-- The new foreign key needs a unique key on the locale-aware document tuple. The old primary key and the old
-- acceptance unique constraint stay in place for old writers.
ALTER TABLE accounts.legal_documents
    ADD CONSTRAINT legal_documents_locale_key UNIQUE (site_id, kind, locale, version);
ALTER TABLE accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_user_kind_locale_version_key UNIQUE (user_id, kind, locale, version);
ALTER TABLE accounts.legal_acceptances
    ADD CONSTRAINT legal_acceptances_document_locale_fkey
        FOREIGN KEY (site_id, kind, locale, version)
        REFERENCES accounts.legal_documents (site_id, kind, locale, version);

-- An old node omits locale and gets the column default zh. Resolve that insert to the locale of the document it names,
-- so an old page accepting the current English document records English rather than a zh acceptance of an English
-- version. New nodes already send the locale; resolving it to the same document is idempotent. While the legacy
-- primary key keeps one document per (site, kind, version), the lookup is unambiguous.
-- +goose StatementBegin
CREATE FUNCTION accounts.set_legal_acceptance_locale() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    NEW.locale := (
        SELECT d.locale
        FROM accounts.legal_documents d
        WHERE d.site_id = NEW.site_id AND d.kind = NEW.kind AND d.version = NEW.version
    );
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER legal_acceptance_locale
    BEFORE INSERT ON accounts.legal_acceptances
    FOR EACH ROW EXECUTE FUNCTION accounts.set_legal_acceptance_locale();

-- +goose Down

-- The old constraints were never removed, so rollback only removes the expansion. Version numbers remained unique
-- across locales, which means no document or acceptance row can collide when the locale columns disappear.
DROP TRIGGER legal_acceptance_locale ON accounts.legal_acceptances;
DROP FUNCTION accounts.set_legal_acceptance_locale();
ALTER TABLE accounts.legal_acceptances DROP CONSTRAINT legal_acceptances_document_locale_fkey;
ALTER TABLE accounts.legal_acceptances DROP CONSTRAINT legal_acceptances_user_kind_locale_version_key;
ALTER TABLE accounts.legal_acceptances DROP COLUMN locale;
ALTER TABLE accounts.legal_documents DROP CONSTRAINT legal_documents_locale_key;
ALTER TABLE accounts.legal_documents DROP COLUMN locale;
