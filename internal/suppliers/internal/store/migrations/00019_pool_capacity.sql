-- +goose Up

-- What the management API of a pool (a sub2api we administer) last said of one of its groups: how many accounts are
-- assigned, how many of them the pool marks failed or has taken out of scheduling, and how much of their quota window is
-- left (docs/domains/suppliers.md「号池」). It is observed state, not part of the supplier's listing: the sync writes it
-- beside the groups, and routing reads the low flag with the rest of its snapshot. All NULL until a read has reported it.
ALTER TABLE suppliers.supplier_groups
    ADD COLUMN pool_accounts integer,
    ADD COLUMN pool_failed integer,
    ADD COLUMN pool_usable integer,
    ADD COLUMN pool_headroom integer,
    ADD COLUMN pool_capacity integer,
    ADD COLUMN pool_low boolean,
    ADD COLUMN pool_observed_at timestamptz,
    ADD CONSTRAINT pool_capacity_complete CHECK (
        (pool_accounts IS NULL AND pool_failed IS NULL AND pool_usable IS NULL AND pool_headroom IS NULL
            AND pool_capacity IS NULL AND pool_low IS NULL AND pool_observed_at IS NULL)
        OR (pool_accounts > 0
            AND pool_failed BETWEEN 0 AND pool_accounts
            AND pool_usable BETWEEN 0 AND pool_accounts
            AND (pool_headroom IS NULL) = (pool_capacity IS NULL)
            AND (pool_headroom IS NULL OR pool_headroom BETWEEN 0 AND 100)
            AND (pool_capacity IS NULL OR pool_capacity BETWEEN 0 AND 100)
            AND pool_low IS NOT NULL
            AND (NOT pool_low OR pool_capacity IS NOT NULL)
            AND pool_observed_at IS NOT NULL));

GRANT UPDATE (pool_accounts, pool_failed, pool_usable, pool_headroom, pool_capacity, pool_low, pool_observed_at)
    ON suppliers.supplier_groups TO axiom_app;

-- +goose Down

ALTER TABLE suppliers.supplier_groups DROP CONSTRAINT pool_capacity_complete;
ALTER TABLE suppliers.supplier_groups
    DROP COLUMN pool_accounts,
    DROP COLUMN pool_failed,
    DROP COLUMN pool_usable,
    DROP COLUMN pool_headroom,
    DROP COLUMN pool_capacity,
    DROP COLUMN pool_low,
    DROP COLUMN pool_observed_at;
