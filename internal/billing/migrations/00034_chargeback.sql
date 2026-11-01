-- +goose NO TRANSACTION
-- +goose Up
-- A chargeback takes credit and cash back without a freeze, so it needs its own cumulative column: reusing refunded
-- would break "refunded <= frozen", and spending it through consumed would let another batch pay for it.
ALTER TABLE billing.credit_batches
    ADD COLUMN IF NOT EXISTS charged_back bigint NOT NULL DEFAULT 0;
GRANT UPDATE (charged_back) ON billing.credit_batches TO axiom_app;
ALTER TABLE billing.credit_batches
    DROP CONSTRAINT IF EXISTS credit_batches_charged_back_check,
    DROP CONSTRAINT IF EXISTS credit_batches_freezes_check,
    ADD CONSTRAINT credit_batches_charged_back_check CHECK (
        charged_back >= 0 AND refunded + charged_back <= amount) NOT VALID,
    ADD CONSTRAINT credit_batches_freezes_check CHECK (
        freeze_total >= 0 AND unfreeze_total >= 0 AND refunded >= 0
        AND unfreeze_total <= freeze_total AND refunded <= freeze_total - unfreeze_total
        AND consumed <= amount - expired
        AND freeze_total - unfreeze_total <= amount - expired - consumed
        AND (source = 'paid' OR freeze_total = 0 AND refund_with_fee IS NULL)) NOT VALID;
ALTER TABLE billing.credit_batches VALIDATE CONSTRAINT credit_batches_charged_back_check;
ALTER TABLE billing.credit_batches VALIDATE CONSTRAINT credit_batches_freezes_check;

-- The transaction kinds return cash and reverse fee income like a refund. Add them NOT VALID first so existing rows are
-- not scanned under a lock; VALIDATE then checks them without blocking writes. Old nodes never write these kinds, so a
-- rolling upgrade needs no coordination here.
ALTER TABLE billing.transactions DROP CONSTRAINT IF EXISTS transactions_kind_check;
ALTER TABLE billing.transactions
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost',
        'cost_adjust', 'expire', 'freeze', 'unfreeze', 'refund', 'chargeback', 'chargeback_reverse')) NOT VALID;
ALTER TABLE billing.transactions VALIDATE CONSTRAINT transactions_kind_check;

-- +goose Down
ALTER TABLE billing.credit_batches
    DROP CONSTRAINT IF EXISTS credit_batches_charged_back_check,
    DROP CONSTRAINT IF EXISTS credit_batches_freezes_check,
    ADD CONSTRAINT credit_batches_freezes_check CHECK (
        freeze_total >= 0 AND unfreeze_total >= 0 AND refunded >= 0
        AND unfreeze_total <= freeze_total AND refunded <= freeze_total - unfreeze_total
        AND consumed <= amount - expired
        AND freeze_total - unfreeze_total <= amount - expired - consumed
        AND (source = 'paid' OR freeze_total = 0 AND refund_with_fee IS NULL)) NOT VALID;
ALTER TABLE billing.credit_batches VALIDATE CONSTRAINT credit_batches_freezes_check;
ALTER TABLE billing.credit_batches DROP COLUMN IF EXISTS charged_back;
ALTER TABLE billing.transactions DROP CONSTRAINT IF EXISTS transactions_kind_check;
ALTER TABLE billing.transactions
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost',
        'cost_adjust', 'expire', 'freeze', 'unfreeze', 'refund')) NOT VALID;
ALTER TABLE billing.transactions VALIDATE CONSTRAINT transactions_kind_check;
