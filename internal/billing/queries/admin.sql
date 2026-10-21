-- What the admin API reads of the ledger.

-- name: ReadCreditBatch :many
-- A gift's currency and offline channel read as empty.
SELECT id, site_id, account_id, source, amount, expires_at, created_at, coalesce(currency, '')::text AS currency, paid, fee,
    merchant_id, coalesce(offline_channel, '')::text AS offline_channel, rate_id
FROM billing.credit_batches WHERE id = @id;
