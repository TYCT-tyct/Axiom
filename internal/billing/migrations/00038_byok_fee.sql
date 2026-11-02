-- +goose Up

-- The share of a model's normal sale price Axiom charges when the request is served by the account's own provider key
-- (BYOK, docs/domains/billing.md「定价」). It is site data, not a constant: a site may waive the fee or charge another
-- rate. 500 basis points is 5%, OpenRouter's rate for BYOK usage (2026-11-02).
ALTER TABLE billing.price_policies ADD COLUMN byok_fee_bps integer NOT NULL DEFAULT 500
    CHECK (byok_fee_bps BETWEEN 0 AND 10000);

-- +goose Down
-- The fee is part of the price a call was charged at only through the schedule the routing snapshot locked; the column
-- itself can go without rewriting any published version.
ALTER TABLE billing.price_policies DROP COLUMN byok_fee_bps;
