-- +goose Up

-- Paid credit (docs/domains/billing.md「额度批次」「账本」): a top-up's cash, the fee income in it and the exchange
-- gain or loss, and the batch that records what was paid for it.
--
-- An entry's currency: NULL is the site's own credits, which every row before this release is and every book but those
-- of a payment in another currency stays; otherwise the currency of a payment that is not the site's. amount is in
-- billionths of one unit of that currency either way, so a transaction balances in each currency on its own, and the
-- user's balance is only ever in the site's credits (internal/billing/README.md「结算与账本」). entries is the largest
-- table of the ledger: the column has no default and its constraints are added NOT VALID, so nothing here scans or
-- rewrites it; new rows are checked, the rows already there all have a NULL currency and a book of the narrower list.
--
-- Both tables change in one file, against the one-table-per-file habit of 00007: every statement on entries changes
-- only the catalog, and credit_batches holds one row per credit, so its check is validated at once.
ALTER TABLE billing.entries
    ADD COLUMN currency text,
    ADD CONSTRAINT entries_currency_check
        CHECK (currency IS NULL OR (currency ~ '^[A-Z]{3}$' AND book <> 'user_balance')) NOT VALID,
    DROP CONSTRAINT entries_book_check,
    ADD CONSTRAINT entries_book_check CHECK (book IN ('user_balance', 'revenue', 'platform_borne', 'supplier_cost',
        'supplier_prepaid', 'gift_expense', 'credit_expired', 'cash', 'fee_income', 'exchange_gain_loss')) NOT VALID;

-- A paid batch records what was paid for it, in minor units of the payment's currency (fee is the part of paid that is
-- the channel's fee the user paid), where it was received (a merchant account of payments, or an offline channel an
-- administrator names), and the reference exchange rate version it was converted at, NULL when the payment is in the
-- site's currency. A gift has none of them. amount stays the credit the batch holds. Refunds are taken in proportion to
-- paid - fee over amount (docs/domains/billing.md「额度批次」).
ALTER TABLE billing.credit_batches
    DROP CONSTRAINT credit_batches_source_check,
    ADD COLUMN currency text,
    ADD COLUMN paid bigint,
    ADD COLUMN fee bigint,
    ADD COLUMN merchant_id uuid,
    ADD COLUMN offline_channel text,
    ADD COLUMN rate_id uuid,
    ADD CONSTRAINT credit_batches_source_check CHECK (
        (source = 'gift' AND currency IS NULL AND paid IS NULL AND fee IS NULL AND merchant_id IS NULL
            AND offline_channel IS NULL AND rate_id IS NULL)
        OR (source = 'paid' AND currency IS NOT NULL AND paid IS NOT NULL AND fee IS NOT NULL
            AND currency ~ '^[A-Z]{3}$' AND paid > 0 AND fee >= 0 AND fee < paid
            AND (merchant_id IS NULL) <> (offline_channel IS NULL)
            AND char_length(btrim(coalesce(offline_channel, 'x'))) BETWEEN 1 AND 200));
