-- +goose Up

-- Credit a code bought (docs/domains/billing.md「额度批次」「账本」): its own source, booked like a gift, because the
-- site issued the code outside the payment channels and no cash reaches the ledger when it is redeemed. A paid batch
-- would invent cash that the cash book cannot match to a channel, and would make the credit refundable through a
-- channel that never took the money.
--
-- credit_batches holds one row per credit, so its check is validated at once, as 00018 did. Rows already there are
-- gift or paid and satisfy the widened check unchanged.
ALTER TABLE billing.credit_batches
    DROP CONSTRAINT credit_batches_source_check,
    ADD CONSTRAINT credit_batches_source_check CHECK (
        (source IN ('gift', 'redeem') AND currency IS NULL AND paid IS NULL AND fee IS NULL AND merchant_id IS NULL
            AND offline_channel IS NULL AND rate_id IS NULL)
        OR (source = 'paid' AND currency IS NOT NULL AND paid IS NOT NULL AND fee IS NOT NULL
            AND currency ~ '^[A-Z]{3}$' AND paid > 0 AND fee >= 0 AND fee < paid
            AND (merchant_id IS NULL) <> (offline_channel IS NULL)
            AND char_length(btrim(coalesce(offline_channel, 'x'))) BETWEEN 1 AND 200));
