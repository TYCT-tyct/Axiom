-- +goose Up

-- What the allocation has put on a batch: consumed by charges, expired by a write-off. Both start at zero, which is
-- what a batch is before the allocation has replayed the account, so no row needs filling in, and a constant default
-- does not rewrite the table. What is left of a batch is amount - consumed - expired. The constraints are added NOT
-- VALID and validated by 00016.
ALTER TABLE billing.credit_batches
    ADD COLUMN consumed bigint NOT NULL DEFAULT 0,
    ADD COLUMN expired bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT credit_batches_consumed_check CHECK (consumed >= 0) NOT VALID,
    ADD CONSTRAINT credit_batches_expired_check CHECK (expired >= 0) NOT VALID,
    ADD CONSTRAINT credit_batches_within_amount CHECK (consumed + expired <= amount) NOT VALID;

CREATE POLICY updates ON billing.credit_batches FOR UPDATE USING (platform.in_scope(site_id, account_id))
    WITH CHECK (platform.in_scope(site_id, account_id));
GRANT UPDATE (consumed, expired) ON billing.credit_batches TO axiom_app;
