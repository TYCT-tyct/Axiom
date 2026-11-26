-- +goose Up

-- What is not secret about one credential (docs/domains/suppliers-direct.md「凭证：形式、字段和设置」): the form of the
-- credential when its type has more than one, and the values of the type's text fields, such as OpenAI's organization
-- or Azure's api-version. Each value is a string; the type that stores a row checks its members, and a node refuses a
-- supplier whose rows its type would not have written. NULL is no setting. A replaced cloud credential that returns to
-- its row writes the new credential's settings there.
ALTER TABLE suppliers.credentials
    ADD COLUMN settings jsonb CONSTRAINT credentials_settings_check CHECK (jsonb_typeof(settings) = 'object');

GRANT UPDATE (settings) ON TABLE suppliers.credentials TO axiom_app;
