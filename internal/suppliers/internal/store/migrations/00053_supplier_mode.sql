-- +goose Up

-- How Axiom reaches a supplier (docs/domains/suppliers.md「直连接口」): a relay or pool it logs in to, a provider's own API
-- reached with pasted keys, or a cloud platform. Only a relay has a balance to read; the others are routable without one.
-- An account's own key (BYOK) is a provider's own API. Neither column changes once the supplier is made, so the app
-- role gets no UPDATE on them.
ALTER TABLE suppliers.suppliers
    ADD COLUMN mode text NOT NULL DEFAULT 'relay' CHECK (mode IN ('relay', 'direct', 'cloud')),
    ADD COLUMN api_format text CHECK (api_format IN ('openai', 'anthropic', 'gemini')),
    ADD CONSTRAINT api_format_is_direct CHECK (api_format IS NULL OR mode = 'direct');

UPDATE suppliers.suppliers SET mode = 'direct' WHERE owner_scope = 'account';
