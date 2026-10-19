-- +goose Up

-- The constraints 00007 to 00010 added NOT VALID, now checked against the rows already there. VALIDATE takes a lock
-- that lets reads and writes go on.
ALTER TABLE billing.transactions VALIDATE CONSTRAINT transactions_kind_check;
ALTER TABLE billing.entries VALIDATE CONSTRAINT entries_book_check;
ALTER TABLE billing.credit_batches VALIDATE CONSTRAINT credit_batches_consumed_check;
ALTER TABLE billing.credit_batches VALIDATE CONSTRAINT credit_batches_expired_check;
ALTER TABLE billing.credit_batches VALIDATE CONSTRAINT credit_batches_within_amount;
ALTER TABLE billing.balances VALIDATE CONSTRAINT balances_unfunded_check;
