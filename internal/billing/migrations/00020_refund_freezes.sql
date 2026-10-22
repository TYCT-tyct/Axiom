-- +goose NO TRANSACTION
-- +goose Up

-- Do not retain one table's DDL lock while waiting for a writer of another table. Each ALTER is atomic; the guards
-- allow this nontransactional migration to resume after a failure.
-- Constant defaults keep old rows at the replay's initial state without rewriting them.
ALTER TABLE billing.balances
    ADD COLUMN IF NOT EXISTS frozen bigint NOT NULL DEFAULT 0,
    DROP CONSTRAINT IF EXISTS balances_frozen_check,
    ADD CONSTRAINT balances_frozen_check CHECK (frozen >= 0) NOT VALID;
GRANT UPDATE (frozen) ON billing.balances TO axiom_app;

ALTER TABLE billing.transactions
    DROP CONSTRAINT IF EXISTS transactions_kind_check,
    ADD CONSTRAINT transactions_kind_check CHECK (kind IN ('credit', 'settle', 'reverse', 'attempt_cost',
        'cost_adjust', 'expire', 'freeze', 'unfreeze', 'refund')) NOT VALID;

ALTER TABLE billing.credit_batches
    ADD COLUMN IF NOT EXISTS freeze_total bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS unfreeze_total bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refunded bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refund_with_fee boolean,
    DROP CONSTRAINT IF EXISTS credit_batches_within_amount,
    DROP CONSTRAINT IF EXISTS credit_batches_freezes_check,
    ADD CONSTRAINT credit_batches_freezes_check CHECK (
        freeze_total >= 0 AND unfreeze_total >= 0 AND refunded >= 0
        AND unfreeze_total <= freeze_total AND refunded <= freeze_total - unfreeze_total
        AND consumed <= amount - expired
        AND freeze_total - unfreeze_total <= amount - expired - consumed
        AND (source = 'paid' OR freeze_total = 0 AND refund_with_fee IS NULL)) NOT VALID;
GRANT UPDATE (freeze_total, unfreeze_total, refunded, refund_with_fee) ON billing.credit_batches TO axiom_app;

ALTER TABLE billing.balances VALIDATE CONSTRAINT balances_frozen_check;
ALTER TABLE billing.transactions VALIDATE CONSTRAINT transactions_kind_check;
ALTER TABLE billing.credit_batches VALIDATE CONSTRAINT credit_batches_freezes_check;
