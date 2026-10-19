-- +goose Up

-- The income of credit that lapsed is a book of its own (docs/domains/billing.md「账本」). entries is the largest table
-- of the ledger, so the wider constraint is added NOT VALID, which checks new rows without scanning it; 00015
-- validates it without blocking writes.
ALTER TABLE billing.entries
    DROP CONSTRAINT entries_book_check,
    ADD CONSTRAINT entries_book_check CHECK (book IN ('user_balance', 'revenue', 'platform_borne', 'supplier_cost',
        'supplier_prepaid', 'gift_expense', 'credit_expired')) NOT VALID;
