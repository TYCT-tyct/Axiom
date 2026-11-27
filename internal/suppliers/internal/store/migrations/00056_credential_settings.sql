-- +goose Up

-- What is not secret about one credential (docs/domains/suppliers-direct.md「凭证：形式、字段和设置」): the form of the
-- credential when its type has more than one, and the values of the type's text fields, such as OpenAI's organization
-- or Azure's api-version. Each value is a string; the type that stores a row checks its members, and a node refuses a
-- supplier whose rows its type would not have written. NULL is no setting. A replaced cloud credential that returns to
-- its row writes the new credential's settings there. Existing Bedrock credentials get their form in 00058. The
-- constraints are added NOT VALID, so adding them scans nothing, and 00059 validates them.
ALTER TABLE suppliers.credentials
    ADD COLUMN settings jsonb,
    ADD CONSTRAINT credentials_settings_check CHECK (jsonb_typeof(settings) = 'object') NOT VALID;

-- A pasted key's own balance, for a type that reads one (docs/domains/suppliers-direct.md「模型重读与余额」): each key is
-- a wallet of its own. The amount is in the currency of the price entry its supplier is connected at, and observed_at
-- is when it was read; both NULL for a key whose balance was never read, and for every other credential.
ALTER TABLE suppliers.credentials
    ADD COLUMN balance_native numeric,
    ADD COLUMN balance_observed_at timestamptz,
    ADD CONSTRAINT credentials_balance_native_check
        CHECK (balance_native <> ALL (ARRAY['NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric])) NOT VALID,
    ADD CONSTRAINT credentials_balance_observed_check CHECK ((balance_native IS NULL) = (balance_observed_at IS NULL)) NOT VALID;

GRANT UPDATE (settings, balance_native, balance_observed_at) ON TABLE suppliers.credentials TO axiom_app;
