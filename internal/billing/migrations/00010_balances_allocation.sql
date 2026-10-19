-- +goose Up

-- Where the allocation of an account stands (internal/billing/README.md「额度批次的分配与到期」). The last transaction
-- replayed is named by its key: seq (0 for a transaction from before the sequence), created_at and id, the order the
-- replay reads in. unfunded is what charges took beyond every batch, the balance below zero. An account never replayed
-- has the defaults, which name nothing before any transaction.
ALTER TABLE billing.balances
    ADD COLUMN allocated_seq bigint NOT NULL DEFAULT 0,
    ADD COLUMN allocated_at timestamptz NOT NULL DEFAULT 'epoch',
    ADD COLUMN allocated_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    ADD COLUMN unfunded bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT balances_unfunded_check CHECK (unfunded >= 0) NOT VALID;

GRANT UPDATE (allocated_seq, allocated_at, allocated_id, unfunded) ON billing.balances TO axiom_app;
